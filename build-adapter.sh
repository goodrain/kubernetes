#!/bin/bash
set -x

docker run -v `pwd`:/go/src/k8s.io/kubernetes -w /go/src/k8s.io/kubernetes golang:1.7.3 go build -ldflags="-w" -o adapter/grproxy ./cmd/kube-proxy  
docker build -t hub.goodrain.com/dc-deploy/adapter:1.6.3 adapter
rm -f adapter/grproxy
docker push hub.goodrain.com/dc-deploy/adapter:1.6.3