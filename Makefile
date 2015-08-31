.PHONY: all kube2dnsimple container push clean test

TAG = 1.10
PREFIX = vektra

all: container

kube2dnsimple: kube2dnsimple.go
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -a -installsuffix cgo --ldflags '-w' ./kube2dnsimple.go

container: kube2dnsimple
	docker build -t $(PREFIX)/kube2dnsimple:$(TAG) .

push:
	docker push $(PREFIX)/kube2dnsimple:$(TAG)

clean:
	rm -f kube2dnsimple

test: clean
	godep go test -v --vmodule=*=4
