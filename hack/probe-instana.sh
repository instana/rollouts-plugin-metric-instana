#!/usr/bin/env bash
#
# Validates the Instana side of the plugin against a real tenant.
#
# Read-only: it issues metric queries and prints raw responses. It creates
# nothing and changes nothing.
#
# Usage:
#   export INSTANA_HOST=https://<tenant>.instana.io
#   export INSTANA_API_TOKEN=<read-only token>
#   ./hack/probe-instana.sh [label-key] [label-value]
#
# Example, once a Rollout is running:
#   ./hack/probe-instana.sh rollouts-pod-template-hash 6d4b8c9f7

set -euo pipefail

: "${INSTANA_HOST:?set INSTANA_HOST, e.g. https://acme-prod.instana.io}"
: "${INSTANA_API_TOKEN:?set INSTANA_API_TOKEN}"

HOST="${INSTANA_HOST%/}"
KEY="${1:-app}"
VALUE="${2:-}"
AUTH="Authorization: apiToken ${INSTANA_API_TOKEN}"
CALL_GROUPS="/api/application-monitoring/analyze/call-groups"
NOW="$(python3 -c 'import time;print(int(time.time()*1000))')"

hr() { printf '\n=== %s ===\n' "$1"; }

# $1 = optional tagFilterExpression fragment (leading comma included)
call_groups() {
  curl -sS -H "$AUTH" -H 'Content-Type: application/json' -X POST "${HOST}${CALL_GROUPS}" --max-time 60 \
    -d "{\"group\":{\"groupbyTag\":\"kubernetes.pod.label\",\"groupbyTagEntity\":\"DESTINATION\",\"groupbyTagSecondLevelKey\":\"${KEY}\"},
         \"metrics\":[{\"metric\":\"errors\",\"aggregation\":\"MEAN\",\"granularity\":60}],
         \"timeFrame\":{\"to\":${NOW},\"windowSize\":3600000},
         \"pagination\":{\"page\":1,\"pageSize\":20}${1:-}}"
}

summarize() {
  python3 -c "
import sys,json
d=json.load(sys.stdin)
print('  totalHits:', d.get('totalHits'))
for i in d.get('items',[]):
    print('   group:', i.get('name'), '->', list((i.get('metrics') or {}).keys()))
"
}

hr "1. auth"
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "$AUTH" "${HOST}/api/application-monitoring/services")
echo "  GET /api/application-monitoring/services -> ${code}  (401 = bad token)"

hr "2. group by kubernetes.pod.label[${KEY}] (no filter)"
call_groups "" | summarize

hr "3. control: a filter matching nothing MUST return totalHits=0"
call_groups ",\"tagFilterExpression\":{\"type\":\"EXPRESSION\",\"logicalOperator\":\"AND\",\"elements\":[{\"type\":\"TAG_FILTER\",\"name\":\"kubernetes.pod.label\",\"operator\":\"EQUALS\",\"entity\":\"DESTINATION\",\"key\":\"${KEY}\",\"value\":\"ZZZ-does-not-exist-ZZZ\"}]}" | summarize
echo "  ^ if this is NOT 0, the tag filter is being ignored. Stop and investigate."

if [[ -n "$VALUE" ]]; then
  hr "4. filter ${KEY}=${VALUE} (should return exactly 1 group)"
  call_groups ",\"tagFilterExpression\":{\"type\":\"EXPRESSION\",\"logicalOperator\":\"AND\",\"elements\":[{\"type\":\"TAG_FILTER\",\"name\":\"kubernetes.pod.label\",\"operator\":\"EQUALS\",\"entity\":\"DESTINATION\",\"key\":\"${KEY}\",\"value\":\"${VALUE}\"}]}" | summarize
else
  hr "4. SKIPPED: pass a label value to test scoping"
  echo "  For a live rollout, get the canary hash with:"
  echo "    kubectl get rs -l rollouts-pod-template-hash \\"
  echo "      -o jsonpath='{range .items[*]}{.metadata.labels.rollouts-pod-template-hash}{\"\\n\"}{end}'"
fi

hr "what to check"
cat <<'EOF'
  - Step 2 returns groups named after label values, with a series key of the
    form "errors.mean.60" (aggregation LOWER-CASED, granularity appended).
  - Step 3 MUST be totalHits=0. If it isn't, tagFilterExpression is being
    dropped and any canary gate built on it is meaningless.
  - Do NOT use /api/application-monitoring/metrics/services: it accepts and
    then silently ignores tagFilterExpression, returning unfiltered results
    with HTTP 200.
EOF
