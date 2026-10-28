# Argo Rollouts metric provider plugin for Instana

Automatically promote or roll back [Argo Rollouts](https://argo-rollouts.readthedocs.io/)
canary deployments based on live Instana metrics — the same role the built-in
Datadog and New Relic providers play, now for Instana.

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

At each analysis interval the plugin queries the Instana
[call-groups API](https://instana.github.io/openapi/) for the canary
ReplicaSet, reduces the result to a scalar, and returns it to Argo for
evaluation against your `successCondition`. A failed condition rolls the
canary back automatically.

---

## Prerequisites

- Argo Rollouts **v1.2+** with plugin support enabled
- An Instana tenant with APM data for your workload
- A **read-only** Instana API token (metrics scope only)

---

## Installation

### 1. Register the plugin with the Argo Rollouts controller

Download the binary for your node architecture from the
[latest GitHub release](https://github.com/instana/rollouts-plugin-metric-instana/releases)
and update the sha256 from the `SHA256SUMS` file on that release page.

```yaml
# argo-rollouts-config ConfigMap (namespace: argo-rollouts)
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-rollouts-config
  namespace: argo-rollouts
data:
  metricProviderPlugins: |-
    - name: "instana/metrics"
      location: "https://github.com/instana/rollouts-plugin-metric-instana/releases/download/<version>/rollouts-plugin-metric-instana_<version>_linux_amd64"
      sha256: "<sha256-from-SHA256SUMS>"
```

Apply and restart the controller:

```sh
kubectl apply -f argo-rollouts-config.yaml
kubectl rollout restart deployment/argo-rollouts -n argo-rollouts
```

> **Air-gapped clusters** — copy the binary to every node and use a
> `file://` location instead:
> `location: "file:///plugins/rollouts-plugin-metric-instana"`

### 2. Create the API token Secret

```sh
kubectl create secret generic instana-api-token \
  --from-literal=token=<your-instana-api-token> \
  -n <your-app-namespace>
```

Or apply [`manifests/secret.yaml`](manifests/secret.yaml) after filling in the
token value.

### 3. Expose the token to the controller

```sh
kubectl -n argo-rollouts set env deploy/argo-rollouts \
  INSTANA_API_TOKEN=- --from=secret/instana-api-token --keys=token
```

Alternatively add a `valueFrom.secretKeyRef` to the controller Deployment.

---

## Usage

### AnalysisTemplate

Create an `AnalysisTemplate` that references `provider.plugin: instana/metrics`.
The example below gates on error rate and P90 latency. Argo injects the canary
`pod-template-hash` automatically via `valueFrom.podTemplateHashValue: Latest`.

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
      successCondition: result <= 0.05   # errors is a ratio (0..1), not a percentage
      failureLimit: 2
      provider:
        plugin:
          instana/metrics:
            host: https://<tenant>.instana.io
            apiTokenEnv: INSTANA_API_TOKEN
            metric: errors
            aggregation: MEAN
            granularity: 60
            timeWindowMs: 300000
            clusterName: <my-cluster>
            namespace: <my-namespace>
            groupByTag: kubernetes.pod.label
            groupByTagKey: rollouts-pod-template-hash
            groupByTagEntity: DESTINATION
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
      successCondition: result <= 500    # milliseconds
      failureLimit: 2
      provider:
        plugin:
          instana/metrics:
            host: https://<tenant>.instana.io
            apiTokenEnv: INSTANA_API_TOKEN
            metric: latency
            aggregation: P90
            granularity: 60
            timeWindowMs: 300000
            clusterName: <my-cluster>
            namespace: <my-namespace>
            groupByTagKey: rollouts-pod-template-hash
            selectGroup: "{{args.pod-template-hash}}"
            tagFilters:
              - name: kubernetes.pod.label
                key: rollouts-pod-template-hash
                operator: EQUALS
                value: "{{args.pod-template-hash}}"
```

### Wire the analysis into a Rollout

```yaml
strategy:
  canary:
    steps:
      - setWeight: 20
      - pause: { duration: 2m }
      - analysis:
          templates:
            - templateName: instana-canary
          args:
            - name: pod-template-hash
              valueFrom:
                podTemplateHashValue: Latest
      - setWeight: 60
      - pause: { duration: 2m }
      - setWeight: 100
```

See [`examples/`](examples/) for complete, ready-to-apply manifests.

---

## Configuration reference

All fields go under `metric.provider.plugin["instana/metrics"]`.

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `host` | yes | — | Instana tenant base URL (e.g. `https://acme.instana.io`) |
| `metric` | yes | — | Instana metric ID: `errors`, `latency`, `calls`, … |
| `aggregation` | no | `MEAN` | `MEAN`, `SUM`, `P90`, `P95`, `P99`, … |
| `granularity` | no | `60` | Resolution in seconds |
| `timeWindowMs` | no | `60000` | Look-back window in milliseconds |
| `clusterName` | no | — | Scope to a specific cluster. **Strongly recommended** — a pod-template-hash is not globally unique. |
| `namespace` | no | — | Scope to a specific namespace. **Strongly recommended** for the same reason. |
| `groupByTag` | no | `kubernetes.pod.label` | Instana tag to group call groups by |
| `groupByTagKey` | no | `rollouts-pod-template-hash` | Label key when using `KEY_VALUE_PAIR` tags |
| `groupByTagEntity` | no | `DESTINATION` | Tag entity direction |
| `selectGroup` | no | — | When canary and stable both report, pick this group by name. Set to `{{args.pod-template-hash}}`. |
| `tagFilters` | no | — | Additional Instana tag filters for scoping |
| `apiTokenEnv` | no | `INSTANA_API_TOKEN` | Name of the env var on the controller that holds the API token |
| `pageSize` | no | `20` | Instana API page size (must be ≥ 1) |
| `insecure` | no | `false` | Skip TLS verification — for testing only, never in production |


### Empty results

Instana only returns a call group for pods that handled matching calls in the
window. When nothing matches, the plugin reports the measurement value as `[]`
and hands `nil` to the condition, the same way the Datadog provider does. What
that means is up to the template:

| Condition | Empty result |
|-----------|--------------|
| `result <= 0.05` | `Error` — evaluation fails on `nil`. A canary with no telemetry cannot pass by accident. |
| `default(result, 0) <= 5` | evaluates with `0`. Use this when empty is the healthy case. |
| `isNil(result)` | `true`; combine as needed. |

Several groups and no `selectGroup` is still an error. That is ambiguity, not
emptiness.

To gate on a count where zero is good, filter for the failure and opt in. The
`call.http.status` filter below has not yet been confirmed against a live
tenant; verify the `entity` and value type before relying on it:

```yaml
- name: http-500s
  successCondition: default(result, 0) <= 5
  provider:
    plugin:
      instana/metrics:
        metric: calls
        aggregation: SUM
        tagFilters:
          - name: call.http.status
            operator: EQUALS
            entity: NOT_APPLICABLE
            value: "500"
          # plus the usual pod-template-hash filter
```

Pair it with a traffic guard so "no 500s" is never mistaken for "no traffic":

```yaml
- name: canary-traffic
  successCondition: default(result, 0) > 0
  provider:
    plugin:
      instana/metrics:
        metric: calls
        aggregation: SUM
```

Argo requires every metric in the template to succeed, so a canary that
receives no traffic fails the run instead of promoting.

#### Coming from Datadog

| Datadog | Instana plugin |
|---------|----------------|
| `sum:trace.http.request.hits{http.status_code:500}` | `metric: calls`, `aggregation: SUM`, `tagFilters` on `call.http.status` |
| `default(result, 0)` in the condition | unchanged |
| error rate from `trace.http.request.errors` | `metric: errors`, `aggregation: MEAN` — a ratio `0..1` of Instana-classified erroneous calls, which includes 4xx, not an HTTP 500 count |

---

## Release

Releases are published automatically via GitHub Actions when a `vX.Y.Z` tag is
pushed. Each release includes binaries for linux/amd64, linux/arm64,
darwin/amd64, and darwin/arm64, plus a `SHA256SUMS` file and its GPG signature.

See [CONTRIBUTING.md](ONBOARDING.md) for details on building and contributing.
