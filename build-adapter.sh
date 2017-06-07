#!/bin/bash
set -x

GOOS=linux go build -ldflags="-w" -o adapter/grproxy ./cmd/kube-proxy
docker build -t hub.goodrain.com/dc-deploy/adapter:1.7.3 adapter
rm -f adapter/grproxy
docker push hub.goodrain.com/dc-deploy/adapter:1.7.3