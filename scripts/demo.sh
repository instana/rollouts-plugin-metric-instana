#!/usr/bin/env bash
#
# End-to-end demo: Argo Rollouts asks Instana whether to promote a canary,
# and rolls back automatically when the canary is unhealthy.
#
#   ./scripts/demo.sh up      build + install everything, deploy the Rollout
#   ./scripts/demo.sh good    healthy canary  -> analysis passes -> promoted
#   ./scripts/demo.sh bad     degraded canary -> analysis fails  -> AUTO ROLLBACK
#   ./scripts/demo.sh status  watch the rollout
#   ./scripts/demo.sh logs    controller + stub logs
#   ./scripts/demo.sh down    delete the kind cluster
#
# MODE=stub (default)  a stub Instana runs in-cluster; deterministic, no agent,
#                      no instrumentation, no tenant required.
# MODE=live            point the plugin at a real tenant. Requires INSTANA_HOST
#                      and INSTANA_API_TOKEN, an Instana agent on the cluster,
#                      and an instrumented app emitting spans.
#
# Safety: refuses to run against any kube-context that is not this demo's kind
# cluster, unless you set FORCE=1. It will not touch a shared cluster.

set -euo pipefail

CLUSTER="${CLUSTER:-argo-instana-demo}"
CONTEXT="kind-${CLUSTER}"
MODE="${MODE:-stub}"
IMAGE="rollouts-plugin-metric-instana:demo"
BIN="rollouts-plugin-metric-instana"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STUB_HOST="http://stub-instana.argo-rollouts.svc.cluster.local:8080"

info()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
warn()  { printf '\033[1;33m[warn] %s\033[0m\n' "$*"; }
die()   { printf '\033[1;31m[fail] %s\033[0m\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

# Never operate on a cluster we did not create.
guard_context() {
  local current
  current="$(kubectl config current-context 2>/dev/null || echo none)"
  if [[ "$current" != "$CONTEXT" && "${FORCE:-0}" != "1" ]]; then
    die "current context is '${current}', expected '${CONTEXT}'.
     Refusing to touch a cluster this script did not create.
     Run './scripts/demo.sh up' first, or set FORCE=1 if you really mean it."
  fi
}

k() { kubectl --context "$CONTEXT" "$@"; }

# Talk to the stub's admin endpoint from inside the cluster.
stub_set() { # $1 = json body
  k -n argo-rollouts exec deploy/stub-instana -- python -c "
import json,urllib.request
req = urllib.request.Request('http://127.0.0.1:8080/admin/error-rate',
    data=json.dumps($1).encode(), headers={'Content-Type':'application/json'})
print(urllib.request.urlopen(req).read().decode())
"
}

resolve_host() {
  if [[ "$MODE" == "live" ]]; then
    [[ -n "${INSTANA_HOST:-}" ]]      || die "MODE=live requires INSTANA_HOST (export it, e.g. https://acme-prod.instana.io)"
    [[ -n "${INSTANA_API_TOKEN:-}" ]] || die "MODE=live requires INSTANA_API_TOKEN"
    echo "${INSTANA_HOST%/}"
  else
    echo "$STUB_HOST"
  fi
}

# Validate MODE=live inputs before we create or change anything.
preflight_live() {
  [[ "$MODE" == "live" ]] || return 0
  local host; host="$(resolve_host)"

  if command -v curl >/dev/null 2>&1; then
    local code
    code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 \
      -H "Authorization: apiToken ${INSTANA_API_TOKEN}" \
      "${host}/api/application-monitoring/services" || echo 000)
    case "$code" in
      200) info "Instana reachable and token accepted (${host})" ;;
      401) die "Instana rejected the token (401). Check INSTANA_API_TOKEN." ;;
      000) die "Cannot reach ${host}. Check INSTANA_HOST and network." ;;
      *)   warn "Unexpected status ${code} from ${host}; continuing anyway" ;;
    esac
  fi

  cat <<'EOF'

[warn] MODE=live needs more than a tenant. The canary gate only returns data if:
         1. an Instana agent is running on THIS cluster, and
         2. the demo workload emits APM spans (it must be instrumented), and
         3. Instana stores the `rollouts-pod-template-hash` pod label on spans.

       A bare kind cluster satisfies none of these, so the AnalysisRun will come
       back phase=Error ("returned no groups") and abort the rollout. That is
       the plugin refusing to guess, not a bug.

       To run live, use a cluster that already reports to your tenant, and point
       the Rollout at an instrumented app:

         DEMO_IMAGE=<your-instrumented-image> MODE=live ./scripts/demo.sh up

       Use MODE=stub (the default) for a self-contained demo.

EOF
}

cmd_up() {
  need docker; need kind; need kubectl; need go
  preflight_live

  info "creating kind cluster '${CLUSTER}' (if absent)"
  kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER"
  kubectl config use-context "$CONTEXT" >/dev/null

  # kind nodes run the host architecture, so the plugin and the image carrying
  # it must match it (arm64 on Apple Silicon, amd64 elsewhere).
  local arch; arch="${ARCH:-$(go env GOARCH)}"
  info "building the plugin for linux/${arch}"
  ( cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "dist/${BIN}" . )

  info "packaging the plugin into an image and loading it into kind"
  cp "$ROOT/dist/${BIN}" "$ROOT/demo/${BIN}"
  docker build --platform "linux/${arch}" -q -t "$IMAGE" -f "$ROOT/demo/Dockerfile.plugin" "$ROOT/demo" >/dev/null
  rm -f "$ROOT/demo/${BIN}"
  kind load docker-image "$IMAGE" --name "$CLUSTER"

  info "installing Argo Rollouts"
  k create namespace argo-rollouts --dry-run=client -o yaml | k apply -f -
  k apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml >/dev/null

  host="$(resolve_host)"

  info "registering the plugin with the controller (file:// — the air-gapped path)"
  cat <<YAML | k apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-rollouts-config
  namespace: argo-rollouts
data:
  metricProviderPlugins: |-
    - name: "instana/metrics"
      location: "file:///plugins/${BIN}"
YAML

  info "creating the API token secret"
  local token="${INSTANA_API_TOKEN:-stub-token}"
  k -n argo-rollouts create secret generic instana-api-token \
    --from-literal=token="$token" --dry-run=client -o yaml | k apply -f -

  if [[ "$MODE" == "stub" ]]; then
    info "deploying the stub Instana"
    k apply -f "$ROOT/demo/stub-instana.yaml"
    k -n argo-rollouts rollout status deploy/stub-instana --timeout=120s
  else
    warn "MODE=live: the controller must be able to reach ${host} from inside the cluster"
  fi

  info "mounting the plugin into the controller and wiring the token"
  k -n argo-rollouts patch deploy argo-rollouts --type strategic -p "$(cat <<PATCH
spec:
  template:
    spec:
      # The controller pod enforces runAsNonRoot, so the initContainer must run
      # as a non-root user; fsGroup makes the shared emptyDir writable by it.
      securityContext:
        fsGroup: 999
      volumes:
        - name: plugin-bin
          emptyDir: {}
      initContainers:
        - name: copy-plugin
          image: ${IMAGE}
          imagePullPolicy: Never
          securityContext:
            runAsNonRoot: true
            runAsUser: 999
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
          command: ["sh","-c","cp /plugin/${BIN} /plugins/${BIN} && chmod 0755 /plugins/${BIN}"]
          volumeMounts:
            - name: plugin-bin
              mountPath: /plugins
      containers:
        - name: argo-rollouts
          volumeMounts:
            - name: plugin-bin
              mountPath: /plugins
          env:
            - name: INSTANA_API_TOKEN
              valueFrom:
                secretKeyRef:
                  name: instana-api-token
                  key: token
PATCH
  )"

  info "restarting the controller so it loads the plugin"
  k -n argo-rollouts rollout restart deploy/argo-rollouts
  k -n argo-rollouts rollout status deploy/argo-rollouts --timeout=180s

  info "deploying the AnalysisTemplate and the Rollout"
  k apply -f "$ROOT/demo/rollout.yaml"
  if [[ -n "${DEMO_IMAGE:-}" ]]; then
    info "pointing the Rollout at ${DEMO_IMAGE}"
    k -n demo patch rollout demo --type merge \
      -p "{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"demo\",\"image\":\"${DEMO_IMAGE}\"}]}}}}"
  fi
  sed "s|__INSTANA_HOST__|${host}|g" "$ROOT/demo/analysis-template.yaml" | k apply -f -

  info "waiting for the initial Rollout to settle"
  sleep 5
  cmd_get

  cat <<EOF

Ready. Now run:

  ./scripts/demo.sh good    # healthy canary -> promoted
  ./scripts/demo.sh bad     # degraded canary -> automatic rollback

Watch it live in another terminal:

  ./scripts/demo.sh status

EOF
}

cmd_get() {
  guard_context
  if command -v kubectl-argo-rollouts >/dev/null 2>&1; then
    k argo rollouts get rollout demo -n demo || true
  else
    k -n demo get rollout demo -o wide || true
    k -n demo get pods -l app=demo || true
  fi
}

cmd_status() {
  guard_context
  if command -v kubectl-argo-rollouts >/dev/null 2>&1; then
    k argo rollouts get rollout demo -n demo --watch
  else
    warn "kubectl-argo-rollouts plugin not installed; falling back to a plain watch"
    k -n demo get rollout demo -w
  fi
}

cmd_good() {
  guard_context
  if [[ "$MODE" == "stub" ]]; then
    info "telling the stub the canary is healthy (error rate 1%)"
    stub_set '{"rate": 0.01, "latency": 40}'
  fi
  info "rolling out a new image (yellow)"
  k -n demo patch rollout demo --type merge \
    -p '{"spec":{"template":{"spec":{"containers":[{"name":"demo","image":"argoproj/rollouts-demo:yellow"}]}}}}'
  info "analysis should PASS and the canary should be promoted"
  cmd_get
}

cmd_bad() {
  guard_context
  if [[ "$MODE" == "stub" ]]; then
    info "telling the stub the canary is degraded (error rate 90%)"
    stub_set '{"rate": 0.9, "latency": 3000}'
  fi
  info "rolling out a bad image (red)"
  k -n demo patch rollout demo --type merge \
    -p '{"spec":{"template":{"spec":{"containers":[{"name":"demo","image":"argoproj/rollouts-demo:red"}]}}}}'
  cat <<EOF

Watch for the rollout to abort. Expect, in order:
  1. canary scales up, 25% weight
  2. AnalysisRun starts, queries Instana for the canary pod-template-hash
  3. error rate 0.9 fails 'result <= 0.05'
  4. AnalysisRun -> Failed, Rollout -> Degraded, canary scaled back to zero

  ./scripts/demo.sh status
  ./scripts/demo.sh logs

EOF
  cmd_get
}

cmd_logs() {
  guard_context
  info "AnalysisRuns"
  k -n demo get analysisrun -o wide || true
  info "controller logs (plugin + analysis lines)"
  k -n argo-rollouts logs deploy/argo-rollouts --tail=200 \
    | grep -iE 'instana|plugin|analysis|measurement' || warn "nothing matched"
  if [[ "$MODE" == "stub" ]]; then
    info "stub Instana logs (each query the plugin made)"
    k -n argo-rollouts logs deploy/stub-instana --tail=50 || true
  fi
}

cmd_down() {
  need kind
  info "deleting kind cluster '${CLUSTER}'"
  kind delete cluster --name "$CLUSTER"
}

case "${1:-}" in
  up)     cmd_up ;;
  good)   cmd_good ;;
  bad)    cmd_bad ;;
  status) cmd_status ;;
  get)    cmd_get ;;
  logs)   cmd_logs ;;
  down)   cmd_down ;;
  *)      sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//' ; exit 1 ;;
esac
