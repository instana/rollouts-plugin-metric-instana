# Onboarding: Instana metric provider for Argo Rollouts

Everything a new person needs to pick this up: why it exists, what was decided
and why, what is proven versus assumed, how to run it, and the traps.

Tracking: idea `INSTANA-I-1890`. Related epic: `INSTA-58207` (a *different*
feature — see below).

---

## 1. Why this exists

Argo Rollouts decides whether to promote or roll back a canary by querying a
metrics backend during the rollout. Datadog, New Relic, Prometheus and others
ship native providers for this. **Instana does not.** A customer asked for
parity.

**This is the opposite direction from the rollout visibility feature we already
shipped.** Do not confuse them:

| | Rollout visibility (`INSTA-58207`) | This project (`INSTANA-I-1890`) |
|---|---|---|
| Direction | Argo Rollouts → Instana | Instana → Argo Rollouts |
| Instana's role | Observer. Renders rollout health in a dashboard. | Metric provider. Argo queries it to decide. |
| Effect | A human *watches* a rollout | Argo *automatically* promotes or rolls back |

The visibility feature delivers exactly that: **visibility**. The customer asked
for **control**. Visibility is not control.

What makes this project tractable is separate and older: Instana attaches a
pod's labels to its APM spans, so `rollouts-pod-template-hash` is queryable —
and that is what separates canary traffic from stable traffic.

---

## 2. Decisions taken

| # | Decision | Why |
|---|----------|-----|
| D1 | Ship a **native Go metric-provider plugin**, and document a **Web-provider recipe** alongside it | The plugin gives Datadog-parity UX. The recipe needs no binary at all, so it covers air-gapped and plugin-averse customers at near-zero cost. |
| D2 | Query `POST /api/application-monitoring/analyze/call-groups` | **`/metrics/services` silently ignores `tagFilterExpression`.** It returns HTTP 200 and the full unfiltered result set. A canary gate built on it would score cluster-wide traffic and be meaningless. This was found by experiment, not documentation. |
| D3 | Scope the canary with the `kubernetes.pod.label` tag, keyed on `rollouts-pod-template-hash` | It is a `KEY_VALUE_PAIR` tag, so the filter needs a separate `key` field. Argo injects the canary hash automatically via `valueFrom.podTemplateHashValue: Latest`. |
| D4 | **An empty result is an error, never `0`** | An error-rate gate that returns `0` for "no data" would promote a canary that is emitting no telemetry at all — the exact failure it exists to prevent. |
| D5 | Delegate condition evaluation to Argo's own `evaluate.EvaluateResult` | `successCondition` / `failureCondition` semantics stay identical to every other provider. Don't reinvent them. |
| D6 | Distribute from **its own repo**, `instana/rollouts-metric-plugin`, via goreleaser | Precedent exists: `instana/terraform-provider-instana` publishes cross-compiled binaries + `SHA256SUMS` + GPG signature, publicly, Apache-2.0. Same category (a Go plugin binary consumed by a third-party OSS tool). **Not** `instana/content` — that is the docs repo, its releases carry documentation PDFs, and its tags track doc releases. |
| D7 | Publish a **raw executable**, not a zip | The Argo controller downloads the file at `location`, verifies its sha256, and execs it. `archives.formats: [binary]`. |
| D8 | API token via an **env var on the controller** for v1 | Simplest thing that works. A Secret + RBAC read is the follow-up if we need per-namespace tokens. |
| D9 | Blue-green shows the **active** variant only | Preview pods receive no production traffic, so a preview error rate is noise. |
| D10 | Always scope the query by **cluster and namespace** | A pod-template-hash is a short hash and is not globally unique. Without scoping, a same-hash workload elsewhere blends into the canary gate. Instana's own rollout UI scopes its equivalent query the same way. |

---

## 3. What is actually proven

Verified against a live Instana tenant and by running the compiled binary.
Nothing below is inferred from documentation.

| Claim | Evidence |
|---|---|
| Auth is `Authorization: apiToken <token>` and is enforced | bogus token → `401` |
| `/api/application-monitoring/metrics/services` **silently drops** `tagFilterExpression` | `service.name = ZZZ-bogus` still returned all 24 services; a nonsense field also returned `200` |
| `analyze/call-groups` **honours** the filter | baseline `totalHits=2`; `app=order-service` → `1`; `app=ZZZ-bogus` → `0` |
| Pod labels are groupable via `groupbyTagSecondLevelKey` | grouping by `kubernetes.pod.label[app]` returned groups `inventory-service`, `order-service` |
| Series key is `<metric>.<aggregation lowercased>.<granularity>` | response contained `errors.mean.60`, `latency.p90.60` — **not** `errors.MEAN` |
| `pagination.page` / `pageSize` must be `>= 1` | `{}` → `422 "metrics must not be null"`; `retrievalSize` → `422` |
| `timeFrame.to: 0` means epoch 0, not "now" | returned `totalHits: 0` until a real millisecond timestamp was supplied |
| The plugin returns real values from real Instana | `errors` → `0.208955223880597`, `latency P90` → `36` |
| A filter matching nothing errors instead of returning zero | live test: `phase=Error`, `"returned no groups"` |
| The compiled binary speaks the controller's RPC protocol | integration tests dispense the real binary over go-plugin RPC; `Type()` → `"Instana"` |
| A degraded canary produces the rollback phase | `phase=Failed` at `value=37.5` against `result <= 5.0` |
| The real controller loads the plugin from `argo-rollouts-config` | `"Copied plugin from /plugins/... to /home/argo-rollouts/plugin-bin/instana/metrics"` |
| Argo injects the canary hash and the plugin scopes to it | stub logged `group=7f9b9574b8`, matching the canary ReplicaSet's `rollouts-pod-template-hash` |
| A degraded canary is **automatically rolled back** on a real cluster | `AnalysisRun` → `Failed` (`value=0.9`), Rollout → `Degraded`, canary scaled to 0 |
| A healthy canary is **promoted** (so it is not just always failing) | `AnalysisRun` → `Successful` (`value=0.01`), Rollout → `Healthy`, image advanced |
| `goreleaser` config builds | `goreleaser build --snapshot` succeeds and runs the `go test` hook |
| **Hash-shaped pod labels reach spans** | grouping by `kubernetes.pod.label[pod-template-hash]` on a live cluster returned real ReplicaSet hashes `['56bffbdc86','8584d6bbc9']` |
| Every pod label reaches spans; there is no allowlist | `KubernetesPodApplicationMonitoringContract` maps `KUBERNETES_POD_LABEL` via `extractFrom(LABELS)` — the pod's whole label map. `rollouts-pod-template-hash` has **zero** references in `instana/backend` and `instana/sensors` |
| `kubernetes.label` and `kubernetes.pod.label` are equivalent for pod labels | both returned the identical groups for `app` and for `pod-template-hash` |
| Cluster and namespace tags filter correctly | `namespace=shop-demo` → 2 groups, `ZZZ-bogus` → 0; `cluster=k-llama` → 2 groups, `ZZZ-bogus` → 0 |
| Instana's own rollout UI uses the same endpoint we do | `useRolloutServiceId.ts` calls `getCallGroups` with a `rollouts-pod-template-hash` tag filter |

### The former blocker, resolved

"Does `rollouts-pod-template-hash` reach Instana spans?" — **yes, by construction.**

It is an ordinary pod label, and Instana attaches a pod's entire label map to its
spans: `KubernetesPodApplicationMonitoringContract` registers
`KUBERNETES_POD_LABEL` with `extractFrom(LABELS)`. There is no allowlist, and
nothing anywhere special-cases the Argo label. Empirically, the stock
`pod-template-hash` that Kubernetes puts on every Deployment-managed pod is
groupable and filterable on a live cluster.

Nothing here depends on a particular Instana release. `totalHits: 0` for
`rollouts-pod-template-hash` on the test tenant means only that the cluster runs
no Argo Rollouts workloads.

### What is NOT yet proven

1. A real Argo Rollouts canary observed end to end on a cluster reporting to
   Instana. The mechanism is proven by the identical stock `pod-template-hash`;
   what is left is a confirmation run, not a risk.
2. `goreleaser check` needs a git remote to validate the `release` section; only
   `goreleaser build` has run. `formats: [binary]` needs goreleaser ≥ v2.5.
3. The GPG signing step has never run (needs `GPG_FINGERPRINT` in CI).
4. Blue-green (D9) is neither implemented nor tested.

---

## 4. How to run it

### Test tiers

```sh
make test              # unit: logic + parsing, Instana stubbed in-process
make test-integration  # spawns the REAL binary over go-plugin RPC, Instana stubbed
make build-all         # cross-compile linux amd64/arm64
```

Live tier — read-only, needs a tenant:

```sh
export INSTANA_HOST=https://<tenant>.instana.io
export INSTANA_API_TOKEN=<read-only token>
export LIVE_GROUP_KEY=app LIVE_GROUP_VALUE=order-service
go test -tags live ./test/live/ -v
```

Probe the tenant directly (read-only, no Go needed):

```sh
./hack/probe-instana.sh app order-service
# once a Rollout exists:
./hack/probe-instana.sh rollouts-pod-template-hash <canary-hash>
```

### Full demo (kind cluster, auto-rollback)

```sh
./hack/demo.sh up      # kind + Argo Rollouts + plugin + stub Instana + Rollout
./hack/demo.sh good    # healthy canary -> analysis passes -> promoted
./hack/demo.sh bad     # degraded canary -> analysis fails -> AUTO ROLLBACK
./hack/demo.sh down
```

See `demo/README.md`. `MODE=live` points the plugin at a real tenant instead of
the stub.

### Install into a real cluster

1. Register the plugin in `argo-rollouts-config` (`examples/argo-rollouts-config.yaml`),
   with the release URL and the sha256 from `SHA256SUMS`.
2. Give the controller the token:
   `kubectl -n argo-rollouts set env deploy/argo-rollouts INSTANA_API_TOKEN=...`
3. Restart the controller — plugins load at startup.
4. Apply `examples/analysis-template.yaml` and wire it into a Rollout step
   (`examples/rollout.yaml`).

Air-gapped clusters cannot reach github.com. Mount the binary and use
`location: file:///plugins/rollouts-metric-plugin-instana`.

---

## 5. Things that matter (the traps)

- **`/metrics/services` lies.** It accepts `tagFilterExpression`, returns `200`,
  and ignores it. Unknown JSON fields are dropped silently. If someone "fixes"
  the endpoint back, every canary gate becomes a cluster-wide average and will
  pass regardless of what the canary does. This is the single most dangerous
  thing in the codebase; the constant is commented accordingly.
- **The handshake cookie is `metricprovider`, not `metrics`.** Get it wrong and
  the controller refuses to exec the binary. It surfaces as `AnalysisRun`
  `phase=Error` — and because `consecutiveErrorLimit` is exceeded, **the rollout
  still aborts**. So a broken plugin looks exactly like a correctly-detected bad
  canary. Always check `phase`: `Failed` means the metric breached the
  condition; `Error` means the plugin is broken. `handshake.go` is the single
  definition, pinned by `TestHandshakeMatchesController`.
- **A test that owns both sides of a contract proves nothing.** The original
  integration test defined its own handshake constant, so it agreed with the
  bug and stayed green while the real controller rejected the binary. The
  end-to-end demo is what caught it.
- **The plugin must match the node architecture.** kind nodes run the host arch;
  a linux/amd64 binary on an arm64 node fails the same way (Argo helpfully
  prints `ELF architecture` vs `current architecture`).
- **The controller pod enforces `runAsNonRoot`.** An initContainer copying the
  binary needs a non-root `securityContext` and a pod-level `fsGroup` so the
  shared `emptyDir` is writable.
- **Aggregation case flips.** You send `MEAN`, Instana returns `errors.mean.60`.
  Build the lookup key with `strings.ToLower`.
- **Granularity is part of the series key.** `errors.mean.60`, not `errors.mean`.
- **`timeFrame.to: 0` is epoch 0.** Always send a real millisecond timestamp.
- **`pagination` must be `>= 1`** on both fields, or `422`.
- **`kubernetes.pod.label` is `KEY_VALUE_PAIR`.** The filter needs `key` *and*
  `value`. `kubernetes.pod.label.rollouts-pod-template-hash` as a single dotted
  name — which the original design docs assumed — does not exist.
- **`kubernetes.deployment.name` will not work.** Rollouts bypass the Deployment
  entity entirely (pod → ReplicaSet → Rollout). Use the pod label.
- **A pod-template-hash is not globally unique.** It is a short hash. Always set
  `clusterName` and `namespace`, or the gate can silently include traffic from a
  same-hash workload in another namespace or cluster. See D10.
- **go-plugin appends `os.Environ()` after `cmd.Env`.** You cannot override an
  inherited variable through `cmd.Env` when launching a plugin — the inherited
  value wins. A developer with a real `INSTANA_API_TOKEN` exported would
  silently have had it used by the integration test. The test now calls
  `t.Setenv` on the parent process instead.
- **`kubernetes.label` and `kubernetes.pod.label` both work** for pod labels;
  Instana's own rollout UI uses one in each of two hooks. We use the
  pod-specific name.
- **Empty ≠ zero.** See D4. Guarded by
  `TestReduceToScalar_EmptyResultIsError` and a live test.
- **Ambiguity ≠ pick the first.** If several groups come back and no
  `selectGroup` is set, we error rather than silently score the stable variant.
- **`errors` is a ratio (0..1), not a percentage.** `successCondition: result <= 0.05`
  is 5%. Writing `result <= 5` would accept a 500%-impossible value and never fail.
- **The canary/stable split only exists mid-rollout.** Before and after, canary
  hash == stable hash and there is one group.
- **Publish a raw binary, not an archive.** See D7.

---

## 6. Security notes

- The API token needs **read-only** metrics scope. Nothing here writes.
- The token is never written to disk by this repo. It is read from an env var at
  request time. Keep it out of `AnalysisTemplate` YAML — reference `apiTokenEnv`.
- The controller downloads and executes a binary. That is why the sha256 in the
  ConfigMap is mandatory and why releases are GPG-signed.
- If a token is ever pasted into a ticket, chat, or transcript, **rotate it**.

---

## 7. Open questions / next steps

1. **Confirmation run against a real canary.** Deploy Argo Rollouts + an
   instrumented app on a cluster reporting to a tenant, start a canary, then
   `./hack/probe-instana.sh rollouts-pod-template-hash <hash>`. The mechanism is
   already established (see §3); this is a confirmation, not a gate.
2. Decide the support model: community OSS artifact vs supported product
   artifact. Apache-2.0 precedent points to community.
3. Converge with Emil's independent prototype — two plugins exist.
4. Add metrics beyond `errors` / `latency` (Irfad's ask; the original idea cites
   HTTP status codes).
5. Consider a `baselineComparison` mode: one call already returns both canary
   and stable groups, which is exactly the `(canary - stable) / stable > X%`
   check the epic describes.
6. Run `goreleaser check` in CI before the first tag.
