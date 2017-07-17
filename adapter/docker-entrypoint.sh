#!/bin/bash

# 启动grproxy
exec /grproxy --depend-service=${DEPEND_SERVICE} --v=2 --bind-address=127.0.0.1 \
  --kubeconfig=/etc/kubernetes/kube-proxy.kubeconfig --healthz-port=0 --namespace=${TENANT_ID}
