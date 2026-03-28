#!/bin/bash
# Batch D end-to-end acceptance test
# Run manually — NOT via MCP tools.
#
# Prerequisites:
#   - conquest deployed with v0.3.x images (server + runner)
#   - port-forward active: kubectl -n services port-forward svc/conquest 9090:80
#   - A public GitHub repo with a tiny DSL2 workflow exists
#
# Usage: bash scripts/test-batch-d.sh

set -u

CONQUEST_URL="${CONQUEST_URL:-http://localhost:9090}"
NAMESPACE="${NAMESPACE:-services}"

# Replace these with your actual acceptance workflow repo + pinned SHA.
WORKFLOW_REPO="https://github.com/PacificAnalytics/conquest-acceptance"
WORKFLOW_REVISION="REPLACE_WITH_COMMIT_SHA"

echo "=== Step 0: Health check ==="
curl -s --max-time 5 "${CONQUEST_URL}/healthz"
echo ""

echo "=== Step 1: WES service-info ==="
curl -s --max-time 5 "${CONQUEST_URL}/ga4gh/wes/v1/service-info" | python3 -m json.tool 2>/dev/null
echo ""

echo "=== Step 2: Submit run ==="
RESPONSE=$(curl -s --max-time 10 -X POST "${CONQUEST_URL}/ga4gh/wes/v1/runs" \
  -H 'Content-Type: application/json' \
  -d "{
    \"workflow_type\": \"NFL\",
    \"workflow_type_version\": \"DSL2\",
    \"workflow_url\": \"${WORKFLOW_REPO}\",
    \"workflow_engine_parameters\": {
      \"revision\": \"${WORKFLOW_REVISION}\"
    }
  }")
echo "Submit response: ${RESPONSE}"
RUN_ID=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('run_id','UNKNOWN'))" 2>/dev/null)
echo "Run ID: ${RUN_ID}"
echo ""

if [ "${RUN_ID}" = "UNKNOWN" ] || [ -z "${RUN_ID}" ]; then
  echo "FATAL: Could not create run. Check the response above."
  exit 1
fi

echo "=== Step 3: Poll run state (up to 5 min) ==="
for i in $(seq 1 60); do
  RESP=$(curl -s --max-time 5 "${CONQUEST_URL}/ga4gh/wes/v1/runs/${RUN_ID}/status")
  STATE=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('state','UNKNOWN'))" 2>/dev/null)
  echo "  Poll $i: state=${STATE}"
  if [ "$STATE" = "COMPLETE" ] || [ "$STATE" = "EXECUTOR_ERROR" ] || [ "$STATE" = "SYSTEM_ERROR" ] || [ "$STATE" = "CANCELED" ]; then
    echo "  >> Terminal state reached!"
    break
  fi
  sleep 5
done
echo ""

echo "=== Step 4: Get full run ==="
curl -s --max-time 5 "${CONQUEST_URL}/ga4gh/wes/v1/runs/${RUN_ID}" | python3 -m json.tool 2>/dev/null
echo ""

echo "=== Step 5: List run tasks ==="
curl -s --max-time 5 "${CONQUEST_URL}/ga4gh/wes/v1/runs/${RUN_ID}/tasks" | python3 -m json.tool 2>/dev/null
echo ""

echo "=== Step 6: K8s runner Job ==="
kubectl -n "${NAMESPACE}" get jobs -l "conquest.io/run-id=${RUN_ID},conquest.io/job-type=runner" 2>&1
echo ""

echo "=== Step 7: K8s task Jobs ==="
kubectl -n "${NAMESPACE}" get jobs -l "conquest.io/run-id=${RUN_ID}" 2>&1
echo ""

echo "=== Step 8: Runner pod logs (tail) ==="
RUNNER_POD=$(kubectl -n "${NAMESPACE}" get pods -l "conquest.io/run-id=${RUN_ID},conquest.io/job-type=runner" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -n "${RUNNER_POD}" ]; then
  echo "Runner pod: ${RUNNER_POD}"
  kubectl -n "${NAMESPACE}" logs "${RUNNER_POD}" -c runner --tail=30 2>&1
else
  echo "No runner pod found"
fi
echo ""

echo "=== Step 9: Conquest logs (relevant) ==="
kubectl -n "${NAMESPACE}" logs deployment/conquest --tail=50 2>&1 | grep -E "run submitted|runner|reconcil|task state|error|WARN|ERROR" || echo "(no matching logs)"
echo ""

echo "=== DONE ==="
