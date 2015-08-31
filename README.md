# kube2dnsimple
==============

A tool to update Kubernetes services with DNS entries in DNSimple automatically.

Any labels and other service details can be used to construct the desired
DNS name via a template paramater.

This is expected to run in the same cluster as the services it advertises.

### Origin

This started taking `kube2sky`, removing all the skyDNS parts, and adding
the DNSimple parts. The endpoint parts were also removed because the idea
is that DNSimple contains the globally addressable parts only
(ie, the LoadBalancer Ingress points)

## Namespaces

Users are free to use namespaces to construct the DNS names as they wish.

## Template

To give maximum flexibility, the DNS name to update is constructed by applying
a template to a service. The template has 2 toplevel variables available:

`.Service`: this is the `api.Service` struct. The full details are here: http://godoc.org/k8s.io/kubernetes/pkg/api#Service. Some common items to use are `Name`, `Namespace`, and `Labels`.

`.Labels`: A function to fetch a label assigned to the service. This is used because the text/template syntax for retrieving a map value is more restricted than kubernetes.

### Examples

This uses the label of "public-name" if set, otherwise the service's name:

`{{or (.Label "public-name") .Service.Name}}`

The default template is:

`{{.Service.Name}.srv.{{.Service.Namespace}}`

To apply a simple prefix to all values:

`k8s-{{.Service.Name}}`


## Flags

`-domain`: Set the domain under which all DNS names will be hosted. This is
the domain as it's setup in DNSimple.

`-email`: Your DNSimple email address.

`-token`: Your DNSimple API Token.

`-template`: The text/template format template to apply to the service to
construct a name. See Template above.

`-verbose`: Log additional information.

`-timeout`: How long to attempt to update DNSimple before giving up.

`--kube_master_url`: URL of kubernetes master. Required if `--kubecfg_file` is not set.

`--kubecfg_file`: Path to kubecfg file that contains the master URL and tokens to authenticate with the master.

## Docker image

The docker image to use is: `vektra/kube2dnsimple:1.10`.

## Kubernetes definition

Here is a simple definition to get you started:

```
apiVersion: v1
kind: ReplicationController
metadata:
  labels:
    name: dnsimple
  name: dnsimple
spec:
  replicas: 1
  selector:
    component: dnsimple
  template:
    metadata:
      labels:
        app: valar
        component: dnsimple
    spec:
      containers:
      - name: kube2dnsimple
        image: vektra/kube2dnsimple:1.10
        args: ["-alsologtostderr=true", "-v=5", "-domain=myinfradomain.com",
               "-email=foo@bar.com", "-token=aabbcc",
                "-template=k8-{{or (.Label \"public-name\") .Service.Name}}"]
```
