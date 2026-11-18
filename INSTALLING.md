# Installing the Instana metric plugin for Argo Rollouts

Get canary promotion and automatic rollback driven by live Instana metrics.

This guide takes you from an empty setup to a Rollout that is gated by Instana.
It assumes the controller runs in the `argo-rollouts` namespace, which is the
default of the upstream install manifests. If yours differs (for example a Helm
install), replace the namespace in every command.

## Prerequisites

- Argo Rollouts controller **v1.5 or later** (metric plugins were introduced in
  1.5.0) running in your cluster
- An Instana tenant with the APM agent reporting calls from your workload
- An Instana API token with read access to application data (see Step 2)
- `kubectl` access to the `argo-rollouts` namespace
- `curl` and `sha256sum` (macOS: `shasum -a 256`) on your workstation

---

## Step 1 — Register the plugin with the controller

The plugin is a single executable that the controller downloads (or copies) at
startup, verifies against a sha256, and runs. It is published as a raw binary,
not an archive, so do not unpack or rename anything.

Pick the release and the architecture of the **nodes that run the controller**:

```sh
VERSION=0.1.1        # see https://github.com/instana/rollouts-plugin-metric-instana/releases
ARCH=amd64           # amd64 or arm64
BASE=https://github.com/instana/rollouts-plugin-metric-instana/releases/download/v${VERSION}
```

Download the checksum file and look up the sha256 of your binary:

```sh
curl -sSLO ${BASE}/rollouts-plugin-metric-instana_${VERSION}_SHA256SUMS
SHA256=$(grep "_linux_${ARCH}$" rollouts-plugin-metric-instana_${VERSION}_SHA256SUMS | awk '{print $1}')
echo "${SHA256}"       # must print one 64-character hex string
```

Choose **one** of the two methods below.

### Option A — Let the controller download it (recommended)

The controller downloads the binary from GitHub when it starts.

```sh
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-rollouts-config
  namespace: argo-rollouts
data:
  metricProviderPlugins: |-
    - name: "instana/metrics"
      location: "${BASE}/rollouts-plugin-metric-instana_${VERSION}_linux_${ARCH}"
      sha256: "${SHA256}"
EOF
```

> **This replaces the whole `metricProviderPlugins` list.** If your cluster
> already registers other metric plugins, edit the ConfigMap instead
> (`kubectl -n argo-rollouts edit configmap argo-rollouts-config`) and add the
> `instana/metrics` entry next to them.

> **The controller must be able to reach github.com at startup.** If it cannot
> download the plugin, the controller does not start. Use Option B for
> clusters without outbound access, or when you want to remove the dependency
> on GitHub being reachable whenever the pod restarts.

### Option B — Provide the binary as a file (air-gapped clusters)

Download the binary and verify it on your workstation:

```sh
curl -sSLO ${BASE}/rollouts-plugin-metric-instana_${VERSION}_linux_${ARCH}
echo "${SHA256}  rollouts-plugin-metric-instana_${VERSION}_linux_${ARCH}" | sha256sum --check
```

Make it available inside the controller container at
`/plugins/rollouts-plugin-metric-instana`. The controller pod does **not** see
files on the node by default, and it runs as non-root with a read-only root
filesystem, so the file has to arrive through a volume. Any of these work:

- Copy the file to `/plugins` on every node that can run the controller, then
  mount it with a `hostPath` volume (shown below).
- Use an initContainer that copies it from an image or internal artifact store
  into a shared `emptyDir`. The initContainer needs a non-root `securityContext`
  and the pod needs an `fsGroup` so the `emptyDir` is writable.
- A shared volume such as NFS.

For the `hostPath` approach, after copying the file to each node:

```sh
ssh <node> 'chmod +x /plugins/rollouts-plugin-metric-instana'

kubectl -n argo-rollouts patch deploy argo-rollouts -p '{
  "spec": {"template": {"spec": {
    "volumes": [
      {"name": "host-plugins", "hostPath": {"path": "/plugins", "type": "Directory"}}
    ],
    "containers": [
      {"name": "argo-rollouts",
       "volumeMounts": [{"name": "host-plugins", "mountPath": "/plugins", "readOnly": true}]}
    ]
  }}}
}'
```

The patch can be re-applied safely. Then register the file:

```sh
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-rollouts-config
  namespace: argo-rollouts
data:
  metricProviderPlugins: |-
    - name: "instana/metrics"
      location: "file:///plugins/rollouts-plugin-metric-instana"
      sha256: "${SHA256}"
EOF
```

The same note applies as in Option A: this replaces the whole
`metricProviderPlugins` list.

---

## Step 2 — Store the Instana API token

Create an API token in the Instana UI under **Settings → Security & Access →
API tokens**. Give it **read-only** permissions. The plugin calls
`POST /api/application-monitoring/analyze/call-groups`, which Instana documents
as requiring the *Applications* permission. Do not grant configuration or
write permissions.

The Secret must live in the **controller's namespace** (`argo-rollouts`),
because the controller reads it, not your application:

```sh
kubectl create secret generic instana-api-token \
  -n argo-rollouts \
  --from-literal=token=<YOUR_TOKEN>
```

You can also fill in and apply [`manifests/secret.yaml`](manifests/secret.yaml).

---

## Step 3 — Give the token to the controller

The plugin reads the token from an environment variable on the controller
(`INSTANA_API_TOKEN` by default; change it per metric with `apiTokenEnv`).

> **Do not use `kubectl set env ... --from=secret/...`.** Users have ended up
> with `INSTANA_API_TOKEN` set to the literal value `-` this way, so the plugin
> sends `-` to Instana and every query fails with `401 Unauthorized`. Use the
> patch below, which names the variable and the secret key explicitly.

```sh
kubectl -n argo-rollouts patch deploy argo-rollouts -p '{
  "spec": {"template": {"spec": {"containers": [{
    "name": "argo-rollouts",
    "env": [{
      "name": "INSTANA_API_TOKEN",
      "valueFrom": {"secretKeyRef": {"name": "instana-api-token", "key": "token"}}
    }]
  }]}}}
}'
```

The patch keeps any environment variables the Deployment already has, and can
be re-applied safely. Check the result:

```sh
kubectl -n argo-rollouts get deploy argo-rollouts \
  -o jsonpath='{.spec.template.spec.containers[0].env}'
# expect an entry named INSTANA_API_TOKEN with a valueFrom.secretKeyRef
```

---

## Step 4 — Restart the controller and confirm the plugin loaded

Patching the Deployment in Step 3 already triggers a rollout. If you only
changed the ConfigMap (Step 1), restart it yourself, because the controller
reads plugin registrations at startup:

```sh
kubectl -n argo-rollouts rollout restart deploy/argo-rollouts
kubectl -n argo-rollouts rollout status deploy/argo-rollouts
```

Confirm the plugin was picked up:

```sh
kubectl -n argo-rollouts logs deploy/argo-rollouts | grep -i "instana"
```

- **Option A** logs `Downloading plugin instana/metrics from: https://...`
  followed by `Download complete`.
- **Option B** logs `Copied plugin from /plugins/rollouts-plugin-metric-instana
  to .../plugin-bin/instana/metrics`.

If the controller is in `CrashLoopBackOff`, see [Troubleshooting](#troubleshooting).

---

## Step 5 — Create an AnalysisTemplate

Gate your canary on error rate and latency. Replace `host`, `clusterName` and
`namespace` with your own values. `namespace` is the namespace of the
**application** being rolled out. Always set both `clusterName` and
`namespace`: a pod-template-hash is not globally unique, and without them the
gate can mix in traffic from an unrelated workload.

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
      successCondition: result <= 0.05   # errors is a ratio 0..1, not a percentage
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
      successCondition: result <= 500
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

Apply it in the namespace where your Rollout lives:

```sh
kubectl apply -n <your-namespace> -f analysis-template.yaml
```

### Gating on a count where zero is good (HTTP 500s)

When nothing matches the query, the plugin reports the value as `[]` and the
condition sees `nil`, the same way the Datadog provider does. A plain
`result <= 0.05` then ends in `Error`, so a canary that emits no telemetry can
never pass by accident.

That is the wrong outcome when the filter selects the failure itself. A healthy
canary has no HTTP 500s, so the query is empty. Opt in with `default(result, 0)`
and add a traffic guard beside it, so that "no 500s" cannot be mistaken for "no
traffic". The `call.http.status` filter has not yet been confirmed against a
live tenant; verify its `entity` and value type before relying on it. Add both
metrics to the `metrics:` list above:

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

Argo requires every metric in the template to succeed, so a canary that
receives no traffic fails the run instead of being promoted.

---

## Step 6 — Wire the analysis into your Rollout

Gate analysis at **every weight step**, not only before full promotion. The
canary is then scored at low traffic (20%) and again at higher traffic (50%)
before it goes to 100%. Rollback fires automatically if `failureLimit` is
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
> Instana only returns a call group for pods that handled requests. If no
> traffic reaches the canary during the `pause` step, the query is empty, which
> is recorded as value `[]` and evaluated as `nil`. A condition such as
> `result <= 0.05` then produces an `Error` measurement rather than `Failed`,
> and enough consecutive errors abort the rollout. Make sure your load balancer
> or ingress sends a share of live traffic to `canaryService` before the
> analysis gate fires, and add the `canary-traffic` guard from Step 5 so the
> failure is reported as `Failed` with a clear value.

> **Why two analysis gates?**
> At 20% weight a degraded canary may not accumulate enough error calls within
> the measurement window to breach `failureLimit`. Gating again at 50% gives
> the plugin a second, higher-signal window before full promotion.

Trigger a rollout and watch the analysis:

```sh
kubectl argo rollouts get rollout <your-app> -n <your-namespace> --watch
kubectl get analysisrun -n <your-namespace>
kubectl describe analysisrun <name> -n <your-namespace>   # per-measurement value and message
```

See [`examples/`](examples/) for complete, annotated manifests.

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
only the canary variant's traffic is scored, not the stable baseline.

For all config options see the
[configuration reference](README.md#configuration-reference).

---

## Troubleshooting

| Symptom | Likely cause |
|---------|-------------|
| Controller in `CrashLoopBackOff`, log shows a download or checksum error for the plugin | The controller cannot reach the `location` URL, or the `sha256` does not match the binary (wrong architecture or version). Re-run the checksum lookup in Step 1, or use Option B |
| `AnalysisRun` phase `Error`: "plugin handshake failed" | Binary built for another architecture than the node, or the file is not executable (Option B: `chmod +x`) |
| `Instana returned 401: Unauthorized request` | `INSTANA_API_TOKEN` is missing or set to a literal value such as `-`. Redo Step 3 with the patch, not `kubectl set env --from=secret` |
| `Instana returned 403` | The token lacks the permission to read application data. Create a token with the *Applications* permission (Step 2) |
| `Instana API token not set` | The Secret is not in the `argo-rollouts` namespace, or the env var is not on the controller. Check Step 3 |
| Value `[]`, phase `Error`: `mismatched types <nil> and float64` | The query matched no calls. Verify `clusterName`, `namespace` and `tagFilters`, and that traffic reaches the canary. If empty is the healthy case for this metric (for example an HTTP 500 count), wrap the condition in `default(result, 0)` |
| `Instana returned N groups ... narrow with tagFilters or set selectGroup` | Several groups came back and no `selectGroup` is set. Add `selectGroup: "{{args.pod-template-hash}}"` |
| Gate passes even when the canary is degraded | Missing `selectGroup`, so the stable group is being scored. Add it |
| `error-rate` is always `0` even after injecting errors | Test traffic went to the **stable** service. Send it to the canary service or the canary pods |
| Service appears in Instana under a wrong or literal name | Dockerfile uses a JSON-array `ENTRYPOINT`, where shell variable substitution does not work. Use the shell form: `ENTRYPOINT ["sh", "-c", "exec java -Dinstana.service.name=${INSTANA_SERVICE_NAME:-my-service} -jar app.jar"]` |
