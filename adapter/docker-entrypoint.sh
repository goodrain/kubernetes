#!/bin/bash


# 启动grproxy
/grproxy --depend-service=${DEPEND_SERVICE} --v=3 --bind-address=127.0.0.1 --master=http://172.30.42.1:8181 --healthz-port=0 --namespace=${TENANT_ID}

