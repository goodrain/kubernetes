#!/bin/bash
mkdir /etc/goodrain
wget -q -P /etc/goodrain http://down.goodrain.me/kube-proxy.kubeconfig
# 启动grproxy
/grproxy --depend-service=${DEPEND_SERVICE} --v=2 --bind-address=127.0.0.1 \
  --kubeconfig=/etc/goodrain/kube-proxy.kubeconfig --healthz-port=0 --namespace=${TENANT_ID}

