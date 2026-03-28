#!/bin/bash
export KUBECONFIG="/Users/anurag/.kube/configs/preview-conquest"
(kubectl -n services port-forward svc/conquest 9091:80 &) 2>/dev/null
sleep 4
curl -s --max-time 5 http://localhost:9091/healthz || echo '{"error":"no healthz"}'
exit 0
