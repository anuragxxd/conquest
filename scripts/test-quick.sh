#!/bin/bash
# Quick test — all output to a single file
exec > /Users/anurag/Desktop/pa/workflow-exec/conquest/test-output.log 2>&1

export KUBECONFIG="$HOME/.kube/configs/preview-conquest"
NS="services"
PORT=9090

echo "=== KILL OLD PORT-FORWARDS ==="
pkill -f "port-forward svc/conquest" 2>/dev/null || true
sleep 1

echo "=== PORT FORWARD on $PORT ==="
kubectl -n $NS port-forward svc/conquest ${PORT}:80 &
PF=$!
sleep 3

echo ""
echo "=== HEALTHZ ==="
curl -s --max-time 5 http://localhost:${PORT}/healthz || echo "healthz failed"

echo ""
echo "=== TES SERVICE-INFO ==="
curl -s --max-time 5 http://localhost:${PORT}/ga4gh/tes/v1/service-info || echo "service-info failed"

echo ""
echo "=== CREATE TASK ==="
RESP=$(curl -s --max-time 10 -X POST http://localhost:${PORT}/ga4gh/tes/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{"name":"batch-c-smoke","executors":[{"image":"alpine:3.21","command":["sh","-c","echo hello-from-conquest && sleep 3 && echo done"]}],"inputs":[],"outputs":[],"tags":{"test":"batch-c"}}')
echo "Response: $RESP"
TASK_ID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))" 2>/dev/null)
echo "Task ID: $TASK_ID"

if [ -z "$TASK_ID" ]; then
  echo "FATAL: No task ID"
  kill $PF 2>/dev/null
  exit 1
fi

echo ""
echo "=== POLL TASK STATE ==="
for i in $(seq 1 24); do
  FULL=$(curl -s --max-time 5 "http://localhost:${PORT}/ga4gh/tes/v1/tasks/${TASK_ID}?view=BASIC" 2>/dev/null)
  STATE=$(echo "$FULL" | python3 -c "import sys,json; print(json.load(sys.stdin).get('state','UNKNOWN'))" 2>/dev/null)
  echo "  [$i] state=$STATE"
  case "$STATE" in
    COMPLETE|EXECUTOR_ERROR|SYSTEM_ERROR|CANCELED)
      echo "  >> TERMINAL"
      break
      ;;
  esac
  sleep 5
done

echo ""
echo "=== FINAL TASK (FULL VIEW) ==="
curl -s --max-time 5 "http://localhost:${PORT}/ga4gh/tes/v1/tasks/${TASK_ID}?view=FULL" | python3 -m json.tool 2>/dev/null || echo "json parse failed"

echo ""
echo "=== K8S JOBS ==="
kubectl -n $NS get jobs -l conquest.io/backend=k8s 2>&1 || echo "no jobs"

echo ""
echo "=== K8S POD FOR TASK ==="
kubectl -n $NS get pods -l "conquest.io/task-id=$TASK_ID" 2>&1 || echo "no pod"

echo ""
echo "=== EXECUTOR LOGS ==="
POD=$(kubectl -n $NS get pods -l "conquest.io/task-id=$TASK_ID" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -n "$POD" ]; then
  echo "Pod: $POD"
  echo "--- executor ---"
  kubectl -n $NS logs "$POD" -c executor 2>&1 || echo "(no logs)"
  echo "--- stage-in ---"
  kubectl -n $NS logs "$POD" -c stage-in 2>&1 || echo "(no logs)"
else
  echo "No pod found"
fi

echo ""
echo "=== CONQUEST LOGS (recent) ==="
kubectl -n $NS logs deployment/conquest --tail=30 2>&1

echo ""
kill $PF 2>/dev/null
echo "=== DONE ==="
