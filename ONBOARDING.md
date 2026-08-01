# Contributing to rollouts-plugin-metric-instana

## What this is

An [Argo Rollouts](https://argo-rollouts.readthedocs.io/) metric-provider plugin
that queries Instana during a canary or blue-green rollout and feeds the result
into the promotion decision. Argo natively supports Datadog, New Relic and
Prometheus; this plugin adds Instana to that list.

The plugin is a standalone binary served over
[HashiCorp go-plugin](https://github.com/hashicorp/go-plugin) RPC. The Argo
controller downloads it at startup, verifies its sha256, and calls it over the
metric-provider interface (`Run`, `Resume`, `Terminate`, `GarbageCollect`).

---

## Repository layout

| Path | Purpose |
|------|---------|
| `main.go` | go-plugin handshake and serve |
| `pkg/plugin/plugin.go` | Metric-provider interface implementation |
| `pkg/plugin/config.go` | Parse `provider.plugin["instana/metrics"]` config |
| `pkg/plugin/instana.go` | Instana metrics API client and scalar reduction |
| `pkg/plugin/plugin_test.go` | Unit tests (Instana API stubbed in-process) |
| `test/integration/` | Integration tests (real binary over go-plugin RPC) |
| `examples/` | ConfigMap, AnalysisTemplate, Rollout |
| `manifests/secret.yaml` | API token Secret |
| `ci/verify.sh` | Single verify entrypoint — same locally, in hooks, and in CI |

---

## Development setup

**Prerequisites:** Go (see `go.mod` for the minimum version), `make`.

```sh
git clone https://github.com/instana/rollouts-plugin-metric-instana.git
cd rollouts-plugin-metric-instana

make hooks    # once: install the pre-commit hook
make verify   # gofmt, go vet, unit tests, build, integration tests, govulncheck
```

`make verify` calls `ci/verify.sh`, which is exactly what GitHub Actions runs.
A green local run means a green CI run.

Optional tools (`govulncheck`, `goreleaser`) are skipped with a notice when not
installed, so a fresh clone verifies with only Go.

---

## Testing

```sh
make test              # unit tests — Instana API stubbed in-process
make test-integration  # integration tests — real binary over go-plugin RPC
make build-all         # cross-compile linux amd64/arm64 into dist/
```

---

## Key design decisions

| # | Decision | Reason |
|---|----------|--------|
| D1 | Query `POST /api/application-monitoring/analyze/call-groups` | `/metrics/services` accepts `tagFilterExpression` but silently ignores it — a filter that looks correct would score unfiltered cluster-wide traffic. |
| D2 | Scope the canary by `kubernetes.pod.label` keyed on `rollouts-pod-template-hash` | Argo injects the canary hash automatically via `valueFrom.podTemplateHashValue: Latest`. The label is a `KEY_VALUE_PAIR` tag; the filter needs both `key` and `value`. |
| D3 | **An empty result is an error, never `0`** | Returning `0` for "no data" would promote a canary that is emitting no telemetry — the exact failure the gate exists to prevent. |
| D4 | Delegate condition evaluation to Argo's `evaluate.EvaluateResult` | `successCondition` / `failureCondition` semantics stay identical to every other provider. |
| D5 | Publish a **raw executable**, not a zip or tarball | The Argo controller downloads the file at `location`, verifies its sha256, and execs it directly. |
| D6 | API token via an **env var on the controller** | Simplest approach for v1. A Secret + RBAC read is the follow-up if per-namespace tokens are needed. |
| D7 | Always scope queries by **cluster and namespace** | A pod-template-hash is a short hash and not globally unique. Without scoping, a same-hash workload in another namespace or cluster blends into the canary gate. |

---

## Things that will bite you

- **`phase=Error` ≠ bad canary.** `Failed` means the metric breached the condition; `Error` means the plugin itself is broken (wrong binary, bad token, RPC failure). Always check `phase` first.
- **The plugin binary must match the node architecture.** A linux/amd64 binary on an arm64 node fails at exec time.
- **The controller pod enforces `runAsNonRoot`.** An initContainer copying the binary needs a non-root `securityContext` and a pod-level `fsGroup` so the shared `emptyDir` is writable.
- **`errors` is a ratio (0..1), not a percentage.** `successCondition: result <= 0.05` means 5%. Writing `result <= 5` would never fail.
- **The canary/stable hash split only exists mid-rollout.** Before promotion and after, both variants share the same hash — there is one group, not two.
- **A pod-template-hash is not globally unique.** Always set `clusterName` and `namespace` in the plugin config.
- **`kubernetes.deployment.name` will not work.** Argo Rollouts bypasses the Deployment entity entirely (pod → ReplicaSet → Rollout). Use the pod label.

---

## Security

- The API token needs **read-only** metrics scope. Nothing in this plugin writes to Instana.
- Keep the token out of `AnalysisTemplate` YAML — reference it via `apiTokenEnv` (defaults to `INSTANA_API_TOKEN`).
- The controller downloads and executes a binary. The sha256 in the ConfigMap and the GPG signature on releases are the integrity checks.
- If a token is ever exposed in a ticket, log, or transcript, **rotate it immediately**.

---

## Release

Push a `vX.Y.Z` tag — GitHub Actions runs goreleaser, which cross-compiles,
generates SHA256SUMS, GPG-signs, and publishes a GitHub Release.

```sh
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

See the [Release section in README.md](README.md#release) for the full details
and required repository secrets.
