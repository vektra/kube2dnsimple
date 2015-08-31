// kube2dnsimple is a component to automatically update DNSimple according
// to services that are running.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sync"
	"text/template"
	"time"

	"github.com/golang/glog"
	"github.com/vektra/kube2dnsimple/dnsimple"

	kapi "k8s.io/kubernetes/pkg/api"
	kclient "k8s.io/kubernetes/pkg/client/unversioned"
	kcache "k8s.io/kubernetes/pkg/client/unversioned/cache"
	kclientcmd "k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	kframework "k8s.io/kubernetes/pkg/controller/framework"
	kSelector "k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/util"
)

var (
	// TODO: switch to pflag and make - and _ equivalent.
	argDomain          = flag.String("domain", "cluster.local", "domain under which to create names")
	argMutationTimeout = flag.Duration("timeout", 10*time.Second, "crash after retrying for a specified duration")
	argEmail           = flag.String("email", "", "email to authenticate with")
	argToken           = flag.String("token", "", "auth token")
	argTemplate        = flag.String("template", "", "template to use to create name")
	argKubecfgFile     = flag.String("kubecfg_file", "", "Location of kubecfg file for access to kubernetes master service; --kube_master_url overrides the URL part of this; if neither this nor --kube_master_url are provided, defaults to service account tokens")
	argKubeMasterURL   = flag.String("kube_master_url", "", "URL to reach kubernetes master. Env variables in this flag will be expanded.")
)

const (
	// Resync period for the kube controller loop.
	resyncPeriod = 30 * time.Minute

	defaultTemplate = "{{.Service.Name}}.svc.{{.Service.Namespace}}"
)

type kube2dnsimple struct {
	ds *dnsimple.Client

	tmpl *template.Template

	// DNS domain name.
	domain string
	// mutation timeout.
	mutationTimeout time.Duration
	// A cache that contains all the endpoints in the system.
	endpointsStore kcache.Store
	// A cache that contains all the servicess in the system.
	servicesStore kcache.Store
	// Lock for controlling access to headless services.
	mlock sync.Mutex
}

func (ks *kube2dnsimple) removeDNS(service *kapi.Service) error {
	name, err := ks.recordName(service)
	if err != nil {
		return err
	}

	records, _, err := ks.ds.Domains.ListRecords(ks.domain, name, "CNAME")
	if err != nil {
		return err
	}

	for _, rec := range records {
		_, err = ks.ds.Domains.DeleteRecord(ks.domain, rec.Id)
		if err != nil {
			return err
		}
	}

	return nil
}

func (ks *kube2dnsimple) generateRecordsForPortalService(service *kapi.Service) error {
	name, err := ks.recordName(service)
	if err != nil {
		return err
	}

	records, _, err := ks.ds.Domains.ListRecords(ks.domain, name, "CNAME")
	if err != nil {
		return err
	}

	glog.V(2).Infof("%d existing records for %s", len(records), name)
	for _, rec := range records {
		glog.V(5).Infof("=> '%s' '%s' '%s'", rec.Name, rec.Type, rec.Content)
	}

	lbs := service.Status.LoadBalancer.Ingress

	if len(records) > 0 {
		var (
			toAdd    []kapi.LoadBalancerIngress
			toRemove []dnsimple.Record
		)

		for _, lb := range lbs {
			var found bool
			for _, rec := range records {
				if rec.Type == "CNAME" && rec.Content == lb.Hostname {
					found = true
					break
				}
			}

			if !found {
				toAdd = append(toAdd, lb)
			}
		}

		for _, rec := range records {
			var found bool
			for _, lb := range lbs {
				if rec.Type == "CNAME" && rec.Content == lb.Hostname {
					found = true
					break
				}
			}

			if !found {
				toRemove = append(toRemove, rec)
			}
		}

		for _, rec := range toRemove {
			glog.V(2).Infof("Removing unneeded record: %s => %s", rec.Name, rec.Content)

			_, err = ks.ds.Domains.DeleteRecord(ks.domain, rec.Id)
			if err != nil {
				return err
			}
		}

		lbs = toAdd
	}

	if len(lbs) == 0 {
		glog.V(2).Infof("Record '%s' name had no lb updates", name)
		return nil
	}

	for _, lb := range lbs {
		var record dnsimple.Record

		record.Name = name
		record.Type = "CNAME"
		record.Content = lb.Hostname

		glog.V(2).Infof("Creating CNAME %s.%s => %s", name, ks.domain, lb.Hostname)

		_, _, err = ks.ds.Domains.CreateRecord(ks.domain, record)
		return err
	}

	return nil
}

func (ks *kube2dnsimple) addDNS(service *kapi.Service) error {
	if len(service.Spec.Ports) == 0 {
		glog.Fatalf("unexpected service with no ports: %v", service)
	}

	if !kapi.IsServiceIPSet(service) {
		return nil
	}

	return ks.generateRecordsForPortalService(service)
}

// Implements retry logic for arbitrary mutator. Crashes after retrying for
// mutation_timeout.
func (ks *kube2dnsimple) mutateOrDie(mutator func() error) {
	timeout := time.After(ks.mutationTimeout)
	for {
		select {
		case <-timeout:
			glog.Fatalf("Failed to mutate for %v using mutator: %v", ks.mutationTimeout, mutator)
		default:
			if err := mutator(); err != nil {
				delay := 1 * time.Second
				glog.V(1).Infof("Failed to mutate using mutator: %v due to: %v. Will retry in: %v", mutator, err, delay)
				time.Sleep(delay)
			} else {
				return
			}
		}
	}
}

// Returns a cache.ListWatch that gets all changes to services.
func createServiceLW(kubeClient *kclient.Client) *kcache.ListWatch {
	return kcache.NewListWatchFromClient(kubeClient, "services", kapi.NamespaceAll, kSelector.Everything())
}

// Type to provide additional functionality in the templates
type templateTarget struct {
	Service *kapi.Service
}

func (t *templateTarget) Label(name string) interface{} {
	return t.Service.Labels[name]
}

func (ks *kube2dnsimple) recordName(s *kapi.Service) (string, error) {
	var buf bytes.Buffer

	var tt templateTarget
	tt.Service = s

	err := ks.tmpl.Execute(&buf, &tt)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (ks *kube2dnsimple) newService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		ks.mutateOrDie(func() error { return ks.addDNS(s) })
	}
}

func (ks *kube2dnsimple) removeService(obj interface{}) {
	if s, ok := obj.(*kapi.Service); ok {
		ks.mutateOrDie(func() error { return ks.removeDNS(s) })
	}
}

func (ks *kube2dnsimple) updateService(oldObj, newObj interface{}) {
	// TODO: Avoid unwanted updates.
	ks.removeService(oldObj)
	ks.newService(newObj)
}

func expandKubeMasterURL() (string, error) {
	parsedURL, err := url.Parse(os.ExpandEnv(*argKubeMasterURL))
	if err != nil {
		return "", fmt.Errorf("failed to parse --kube_master_url %s - %v", *argKubeMasterURL, err)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" || parsedURL.Host == ":" {
		return "", fmt.Errorf("invalid --kube_master_url specified %s", *argKubeMasterURL)
	}
	return parsedURL.String(), nil
}

// TODO: evaluate using pkg/client/clientcmd
func newKubeClient() (*kclient.Client, error) {
	var (
		config    *kclient.Config
		err       error
		masterURL string
	)
	// If the user specified --kube_master_url, expand env vars and verify it.
	if *argKubeMasterURL != "" {
		masterURL, err = expandKubeMasterURL()
		if err != nil {
			return nil, err
		}
	}
	if masterURL != "" && *argKubecfgFile == "" {
		// Only --kube_master_url was provided.
		config = &kclient.Config{
			Host:    masterURL,
			Version: "v1",
		}
	} else {
		// We either have:
		//  1) --kube_master_url and --kubecfg_file
		//  2) just --kubecfg_file
		//  3) neither flag
		// In any case, the logic is the same.  If (3), this will automatically
		// fall back on the service account token.
		overrides := &kclientcmd.ConfigOverrides{}
		overrides.ClusterInfo.Server = masterURL                                     // might be "", but that is OK
		rules := &kclientcmd.ClientConfigLoadingRules{ExplicitPath: *argKubecfgFile} // might be "", but that is OK
		if config, err = kclientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig(); err != nil {
			return nil, err
		}
	}

	glog.Infof("Using %s for kubernetes master", config.Host)
	glog.Infof("Using kubernetes API %s", config.Version)
	return kclient.New(config)
}

func watchForServices(kubeClient *kclient.Client, ks *kube2dnsimple) kcache.Store {
	serviceStore, serviceController := kframework.NewInformer(
		createServiceLW(kubeClient),
		&kapi.Service{},
		resyncPeriod,
		kframework.ResourceEventHandlerFuncs{
			AddFunc:    ks.newService,
			DeleteFunc: ks.removeService,
			UpdateFunc: ks.updateService,
		},
	)
	go serviceController.Run(util.NeverStop)
	return serviceStore
}

func main() {
	flag.Parse()

	domain := *argDomain

	ds := dnsimple.NewClient(*argToken, *argEmail)

	templateText := *argTemplate
	if templateText == "" {
		templateText = defaultTemplate
	}

	tmpl, err := template.New("record").Parse(templateText)
	if err != nil {
		glog.Fatalf("Unable to parse template: %s", err)
	}

	ks := kube2dnsimple{
		ds:              ds,
		domain:          domain,
		mutationTimeout: *argMutationTimeout,
		tmpl:            tmpl,
	}

	kubeClient, err := newKubeClient()
	if err != nil {
		glog.Fatalf("Failed to create a kubernetes client: %v", err)
	}

	ks.servicesStore = watchForServices(kubeClient, &ks)

	select {}
}
