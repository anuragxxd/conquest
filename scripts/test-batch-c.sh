#!/bin/bash
# Batch C smoke test
# Does NOT use set -e so we can see all output even on failures.

export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/configs/preview-conquest}"
NAMESPACE="services"

echo "=== Step 0: Check pod status ==="
kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/name=conquest 2>&1
echo ""

echo "=== Step 1: Check conquest logs ==="
kubectl -n "$NAMESPACE" logs deployment/conquest --tail=10 2>&1
echo ""

echo "=== Step 2: Port-forward (background) ==="
kubectl -n "$NAMESPACE" port-forward svc/conquest 8080:80 >/dev/null 2>&1 &
PF_PID=$!
sleep 3
echo "Port-forward PID: $PF_PID"

echo "=== Step 3: Health check ==="
curl -s --max-time 5 http://localhost:8080/healthz 2>&1
echo ""
echo ""

echo "=== Step 4: TES service-info ==="
curl -s --max-time 5 http://localhost:8080/ga4gh/tes/v1/service-info 2>&1
echo ""
echo ""

echo "=== Step 5: Create test TES task ==="
RESPONSE=$(curl -s --max-time 10 -X POST http://localhost:8080/ga4gh/tes/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "batch-c-smoke-test",
    "executors": [{
      "image": "alpine:3.21",
      "command": ["sh", "-c", "echo hello-from-conquest && sleep 5 && echo done"]
    }],
    "inputs": [],
    "outputs": [],
    "tags": {
      "test": "batch-c-smoke"
    }
  }' 2>&1)
echo "Create response: $RESPONSE"
TASK_ID=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id','UNKNOWN'))" 2>/dev/null)
echo "Task ID: $TASK_ID"
echo ""

if [ "$TASK_ID" = "UNKNOWN" ] || [ -z "$TASK_ID" ]; then
  echo "FATAL: Could not create task. Aborting."
  kill $PF_PID 2>/dev/null
  exit 1
fi

echo "=== Step 6: Poll task state (up to 100s) ==="
for i in $(seq 1 20); do
  RESP=$(curl -s --max-time 5 "http://localhost:8080/ga4gh/tes/v1/tasks/${TASK_ID}?view=BASIC" 2>&1)
  STATE=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('state','UNKNOWN'))" 2>/dev/null)
  echo "  Poll $i: state=$STATE"
  if [ "$STATE" = "COMPLETE" ] || [ "$STATE" = "EXECUTOR_ERROR" ] || [ "$STATE" = "SYSTEM_ERROR" ] || [ "$STATE" = "CANCELED" ]; then
    echo "  >> Terminal state reached!"
    break
  fi
  sleep 5
done
echo ""

echo "=== Step 7: Final task (FULL view) ==="
curl -s --max-time 5 "http://localhost:8080/ga4gh/tes/v1/tasks/${TASK_ID}?view=FULL" 2>&1 | python3 -m json.tool 2>/dev/null
echo ""

echo "=== Step 8: K8s Job ==="
kubectl -n "$NAMESPACE" get jobs -l conquest.io/backend=k8s 2>&1
echo ""

echo "=== Step 9: K8s Pod ==="
kubectl -n "$NAMESPACE" get pods -l "conquest.io/task-id=$TASK_ID" 2>&1
echo ""

echo "=== Step 10: Container logs ==="
POD=$(kubectl -n "$NAMESPACE" get pods -l "conquest.io/task-id=$TASK_ID" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -n "$POD" ]; then
  echo "Pod: $POD"
  echo "--- executor logs ---"
  kubectl -n "$NAMESPACE" logs "$POD" -c executor 2>&1
  echo "--- stage-in logs ---"
  kubectl -n "$NAMESPACE" logs "$POD" -c stage-in 2>&1
else
  echo "No pod found for task $TASK_ID"
fi
echo ""

echo "=== Step 11: Conquest logs ==="
kubectl -n "$NAMESPACE" logs deployment/conquest --tail=30 2>&1 | grep -E "task state|submitted k8s|reconcil|error|k8s backend|WARN|ERROR" || echo "(no matching logs)"
echo ""

kill $PF_PID 2>/dev/null
echo "=== DONE ==="
