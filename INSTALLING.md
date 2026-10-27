# Installing the Instana metric plugin for Argo Rollouts

Get canary promotion and automatic rollback driven by live Instana metrics.

## Prerequisites

- Argo Rollouts controller **v1.6+** in your cluster
- An Instana tenant with the APM agent reporting spans from your workload
- `kubectl` access to the `argo-rollouts` namespace
- **Go 1.21+** installed on the machine where you run the build script

---

## Step 1 — Build the binary

Clone the repo and run the release script, setting the version you want to ship:

```sh
VERSION=0.1.0 ./scripts/release-manual.sh
```

This produces the binaries and their checksums in `dist/`:

```
dist/rollouts-plugin-metric-instana_<VERSION>_linux_amd64
dist/rollouts-plugin-metric-instana_<VERSION>_linux_arm64
dist/rollouts-plugin-metric-instana_<VERSION>_SHA256SUMS
```

Verify the binary before deploying:

```sh
# Run from inside dist/ — the SUMS file uses bare filenames, not prefixed paths
cd dist && sha256sum --check rollouts-plugin-metric-instana_<VERSION>_SHA256SUMS && cd ..
```

---

## Step 2 — Make the binary available to the controller

Get the sha256 for the binary you built:

```sh
cat dist/rollouts-plugin-metric-instana_<VERSION>_SHA256SUMS
```

Choose **one** of the two methods below, then update the `sha256` and `location`
fields in `examples/argo-rollouts-config.yaml` accordingly before applying.

### Option A — Serve over HTTP (recommended)

Serve the binary from any HTTP server reachable by the controller pod:

```sh
# Example: Python one-liner on a machine reachable from the cluster
cd dist && python3 -m http.server 8888
```

> **Verify reachability first** — the controller pod must be able to reach the server.
> Quick check from any pod in the cluster (replace IP and port as needed):
> ```sh
> kubectl run curl-test --image=curlimages/curl --rm -it --restart=Never -- \
>   curl -sI http://<server-ip>:8888/rollouts-plugin-metric-instana_<VERSION>_linux_amd64
> ```
> Expect `HTTP/1.0 200 OK`. If unreachable, check firewall rules between the server
> and the cluster nodes.

Set `location` in `examples/argo-rollouts-config.yaml` to the HTTP URL:

```yaml
location: "http://<server-ip>:8888/rollouts-plugin-metric-instana_<VERSION>_linux_amd64"
sha256: "<value from SHA256SUMS>"
```

The controller downloads and verifies the binary automatically on startup.

Apply the ConfigMap once `location` and `sha256` are set:

```sh
kubectl apply -f examples/argo-rollouts-config.yaml
```

### Option B — Copy directly to the controller node

Copy the binary onto each node that may run the Argo Rollouts controller:

```sh
scp dist/rollouts-plugin-metric-instana_<VERSION>_linux_amd64 \
  <node>:/plugins/rollouts-plugin-metric-instana
ssh <node> chmod +x /plugins/rollouts-plugin-metric-instana
```

Set `location` in `examples/argo-rollouts-config.yaml` to the file path:

```yaml
location: "file:///plugins/rollouts-plugin-metric-instana"
sha256: "<value from SHA256SUMS>"
```

> **⚠️ The controller pod does not see host paths by default.** You must mount
> `/plugins` from the node into the pod via a `hostPath` volume, otherwise the
> controller cannot read the file and the plugin will fail to load.
>
> Patch the controller Deployment to add the volume and mount:
>
> ```sh
> kubectl patch deploy argo-rollouts -n argo-rollouts --type=json -p='[
>   {
>     "op": "add",
>     "path": "/spec/template/spec/volumes/-",
>     "value": {
>       "name": "host-plugins",
>       "hostPath": { "path": "/plugins", "type": "DirectoryOrCreate" }
>     }
>   },
>   {
>     "op": "add",
>     "path": "/spec/template/spec/containers/0/volumeMounts/-",
>     "value": {
>       "name": "host-plugins",
>       "mountPath": "/plugins",
>       "readOnly": true
>     }
>   }
> ]'
> ```
>
> This patch is idempotent — safe to re-apply if you update the binary on the node.

Apply the ConfigMap once `location` and `sha256` are set:

```sh
kubectl apply -f examples/argo-rollouts-config.yaml
```

---

## Step 3 — Store the API token

Create a token in **Settings → Team Settings → API Tokens** with only
**Service & endpoint metrics** (read) enabled, then store it:

```sh
kubectl create secret generic instana-api-token \
  -n argo-rollouts \
  --from-literal=token=<YOUR_TOKEN>
```

---

## Step 4 — Inject the token into the controller

> **⚠️ Do not use `kubectl set env --from=secret`** — it maps the secret key
> name (`token`) as the env var name (`TOKEN`), not `INSTANA_API_TOKEN`.
> Use the patch below instead, which sets the name explicitly via `secretKeyRef`.

```sh
kubectl patch deploy argo-rollouts -n argo-rollouts --type=json -p='[{
  "op": "add",
  "path": "/spec/template/spec/containers/0/env",
  "value": [{
    "name": "INSTANA_API_TOKEN",
    "valueFrom": {
      "secretKeyRef": {
        "name": "instana-api-token",
        "key": "token"
      }
    }
  }]
}]'
```

Verify the env var is wired correctly before restarting:

```sh
kubectl get deploy argo-rollouts -n argo-rollouts \
  -o jsonpath='{.spec.template.spec.containers[0].env}' | python3 -m json.tool
# Expected: [{ "name": "INSTANA_API_TOKEN", "valueFrom": { "secretKeyRef": { ... } } }]
```

---

## Step 5 — Restart the controller

```sh
kubectl -n argo-rollouts rollout restart deploy/argo-rollouts
kubectl -n argo-rollouts rollout status deploy/argo-rollouts
```

Confirm the plugin loaded:

```sh
kubectl -n argo-rollouts logs deploy/argo-rollouts | grep -i "instana\|plugin"
# Option A (HTTP) — expect two lines:
#   Downloading plugin instana/metrics from: http://...
#   Download complete, it took <N>ms
#
# Option B (file) — expect one line:
#   Copied plugin from /plugins/rollouts-plugin-metric-instana to /home/argo-rollouts/plugin-bin/instana/metrics
```

---

## Step 6 — Create an AnalysisTemplate

Gate your canary on error rate and latency. Adapt `host`, `clusterName`, and
`namespace` to your tenant:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: instana-canary
spec:
  args:
    - name: pod-template-hash
  metrics:
    - name: error-rate
      interval: 1m
      count: 5
      successCondition: result <= 0.05   # errors is a ratio 0..1
      failureLimit: 2
      provider:
        plugin:
          instana/metrics:
            host: https://<your-tenant>.instana.io
            metric: errors
            aggregation: MEAN
            clusterName: <your-cluster>
            namespace: <your-namespace>
            selectGroup: "{{args.pod-template-hash}}"
            tagFilters:
              - name: kubernetes.pod.label
                key: rollouts-pod-template-hash
                operator: EQUALS
                entity: DESTINATION
                value: "{{args.pod-template-hash}}"

    - name: latency-p90
      interval: 1m
      count: 5
      # latency is returned in milliseconds. Tune this threshold to your SLO;
      # typical P90 values vary widely by workload and infrastructure.
      successCondition: result <= 5000   # milliseconds — adjust to your SLO
      failureLimit: 2
      provider:
        plugin:
          instana/metrics:
            host: https://<your-tenant>.instana.io
            metric: latency
            aggregation: P90
            clusterName: <your-cluster>
            namespace: <your-namespace>
            selectGroup: "{{args.pod-template-hash}}"
            tagFilters:
              - name: kubernetes.pod.label
                key: rollouts-pod-template-hash
                operator: EQUALS
                entity: DESTINATION
                value: "{{args.pod-template-hash}}"
```

To gate on HTTP 500s specifically, count `calls` filtered by status. A healthy
canary returns no matching calls, so the condition must opt in with
`default(result, 0)`. Add a traffic guard beside it so "no 500s" cannot be
mistaken for "no traffic". The `call.http.status` filter has not yet been
confirmed against a live tenant; verify its `entity` and value type first:

```yaml
    - name: http-500s
      interval: 1m
      count: 5
      successCondition: default(result, 0) <= 5   # 500s in the last granularity bucket
      failureLimit: 2
      provider:
        plugin:
          instana/metrics:
            host: https://<your-tenant>.instana.io
            metric: calls
            aggregation: SUM
            clusterName: <your-cluster>
            namespace: <your-namespace>
            selectGroup: "{{args.pod-template-hash}}"
            tagFilters:
              - name: call.http.status
                operator: EQUALS
                entity: NOT_APPLICABLE
                value: "500"
              - name: kubernetes.pod.label
                key: rollouts-pod-template-hash
                operator: EQUALS
                entity: DESTINATION
                value: "{{args.pod-template-hash}}"

    - name: canary-traffic
      interval: 1m
      count: 5
      successCondition: default(result, 0) > 0   # the canary must be serving calls
      failureLimit: 2
      provider:
        plugin:
          instana/metrics:
            host: https://<your-tenant>.instana.io
            metric: calls
            aggregation: SUM
            clusterName: <your-cluster>
            namespace: <your-namespace>
            selectGroup: "{{args.pod-template-hash}}"
            tagFilters:
              - name: kubernetes.pod.label
                key: rollouts-pod-template-hash
                operator: EQUALS
                entity: DESTINATION
                value: "{{args.pod-template-hash}}"
```

---

## Step 7 — Wire the analysis into your Rollout

Gate analysis at **every weight step** — not just before full promotion. This
ensures the canary is scored at low traffic (20%) and again at higher traffic
(50%) before going to 100%. Rollback fires automatically if `failureLimit` is
exceeded at either gate.

```yaml
strategy:
  canary:
    canaryService: <your-app>-canary   # route canary traffic here
    stableService: <your-app>-stable   # route stable traffic here
    steps:
      - setWeight: 20
      - pause: { duration: 2m }        # let traffic accumulate before measuring
      - analysis:
          templates:
            - templateName: instana-canary
          args:
            - name: pod-template-hash
              valueFrom:
                podTemplateHashValue: Latest   # injects the live canary hash automatically
      - setWeight: 50
      - pause: { duration: 2m }
      - analysis:
          templates:
            - templateName: instana-canary
          args:
            - name: pod-template-hash
              valueFrom:
                podTemplateHashValue: Latest
```

> **Traffic must reach the canary during the pause window.**
> Instana only sees call groups for pods that received real requests. If no traffic
> flows to the canary during the `pause` step, the plugin query returns an empty
> result, recorded as value `[]` and evaluated as `nil`. A condition such as
> `result <= 0.05` then produces an `Error` measurement rather than `Failed`, and
> enough consecutive errors abort the rollout. Make sure your load balancer or
> ingress is routing a share of live traffic to `canaryService` before the
> analysis gate fires, and add the `canary-traffic` guard from Step 6 so the
> failure is reported as `Failed` with a clear value.

> **Why two analysis gates?**
> At 20% weight a degraded canary may not accumulate enough error calls within
> the measurement window to breach `failureLimit`. Gating again at 50% gives
> the plugin a second, higher-signal window before full promotion.

See [`examples/`](examples/) for complete, annotated YAML.

---

## How it works

```
Argo Rollouts controller ──RPC──▶ this plugin ──HTTPS──▶ Instana metrics API
                                       │
                              evaluate successCondition
                                       │
                            Successful / Failed / Error
                                       ▼
                         promote canary  or  roll back
```

The plugin queries `POST /api/application-monitoring/analyze/call-groups`,
scoping each request to the canary `rollouts-pod-template-hash` pod label so
only the canary variant's traffic is scored — not the stable baseline.

For all config options see the [README](README.md#config-fields).

---

## Troubleshooting

| Symptom | Likely cause |
|---------|-------------|
| `AnalysisRun` phase `Error`: "plugin handshake failed" | sha256 mismatch or controller cannot reach the download URL |
| `Instana returned 401: Unauthorized request` | `INSTANA_API_TOKEN` env var is missing or set to a literal value (e.g. `"-"`). Use the `kubectl patch` in Step 4 — not `kubectl set env --from=secret` |
| `Error: Instana API token not set` | Secret not applied or env var not injected into the controller |
| Value `[]`, phase `Error`: "mismatched types <nil> and float64" | The query matched no calls. Verify `clusterName`, `namespace`, and `tagFilters`. If empty is the healthy case for this metric (e.g. an HTTP 500 count), wrap the condition in `default(result, 0)` |
| Gate passes even when canary is degraded | Missing `selectGroup` — add it so only the canary group is scored |
| Service appears in Instana under wrong or literal name | Dockerfile uses JSON-array `ENTRYPOINT` — shell variable substitution does not work in that form. Switch to shell-form: `ENTRYPOINT ["sh", "-c", "exec java -Dinstana.service.name=${INSTANA_SERVICE_NAME:-my-service} -jar app.jar"]` |
| `error-rate` always `0` even after injecting errors | Test traffic was sent to the **stable** service, not the canary. Ensure traffic reaches the canary pods — use the canary service endpoint or target the canary pods directly |
