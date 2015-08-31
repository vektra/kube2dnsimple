FROM busybox
MAINTAINER Evan Phoenix <evan@vektra.com>
ADD https://raw.githubusercontent.com/bagder/ca-bundle/master/ca-bundle.crt /etc/ssl/certs/ca-certificates.crt
ADD kube2dnsimple kube2dnsimple
ENTRYPOINT ["/kube2dnsimple"]
