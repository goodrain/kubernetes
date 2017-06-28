#!/bin/bash
mkdir /etc/goodrain
wget -q -P /etc/goodrain http://download.goodrain.me/k8s/kube-proxy.kubeconfig

# 启动grproxy
exec /grproxy --depend-service=${DEPEND_SERVICE} --v=2 --bind-address=127.0.0.1 \
  --kubeconfig=/etc/goodrain/kube-proxy.kubeconfig --healthz-port=0 --namespace=${TENANT_ID}
