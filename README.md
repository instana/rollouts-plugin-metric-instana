# Argo Rollouts metric provider plugin for Instana

Lets [Argo Rollouts](https://argo-rollouts.readthedocs.io/) query Instana
during a canary or blue-green rollout and automatically promote or roll back
based on the result — the same role the built-in Datadog and New Relic
providers play.

This closes the gap left by the Instana ↔ Argo Rollouts *visibility* feature
(`INSTA-58207`), which only surfaces rollout state in the Instana dashboard and
does not feed the promotion decision.

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

The plugin implements the Argo Rollouts metric-provider plugin interface
(`InitPlugin`, `Run`, `Resume`, `Terminate`, `GarbageCollect`, `Type`,
`GetMetadata`) and is served as a standalone binary over HashiCorp go-plugin
RPC.

## Layout

| Path | Purpose |
|------|---------|
| `main.go` | go-plugin handshake + serve |
| `pkg/plugin/plugin.go` | metric-provider interface implementation |
| `pkg/plugin/config.go` | parse `provider.plugin["instana/metrics"]` |
| `pkg/plugin/instana.go` | Instana metrics API client + scalar reduction |
| `pkg/plugin/plugin_test.go` | unit tests against a mocked Instana API |
| `examples/` | ConfigMap, AnalysisTemplate, Rollout |
| `manifests/secret.yaml` | API token secret |

## Build and verify

```sh
make hooks     # once: run the same checks on every commit
make verify    # == ./ci/verify.sh == exactly what CI runs
make build-all # cross-compile linux amd64/arm64 into dist/
make checksums # sha256 for the controller ConfigMap
```

`ci/verify.sh` runs gofmt, `go vet`, unit tests, a build, the integration tests,
`govulncheck`, and `goreleaser check`. CI (`.github/workflows/build.yml`) calls
the same script, so a green local run means a green build. CodeQL and Go
dependency submission run as separate workflows.

> The repo is staged on github.ibm.com while access to the public `instana` org
> is arranged. GitHub Actions is disabled on Enterprise, so the workflows are
> dormant for now — use `make verify` locally. The Go module path already points
> at the public home (`github.com/instana/…`), so it will not `go get` from
> Enterprise until the move.

## Release

Releases mirror `instana/terraform-provider-instana`, which publishes the same
category of artifact (a Go plugin binary consumed by a third-party OSS tool).

`.pipeline-config.yaml` (IBM Continuous Delivery) owns the release: it runs
goreleaser (`.goreleaser.yml`) on a `v*` tag to cross-compile, checksum, and
GPG-sign. `.github/workflows/release.yml` is deliberately **disabled**
(`workflow_dispatch` only) so the two paths cannot both publish — exactly how
the Terraform provider does it.

One deliberate difference from that provider: it publishes **zips**, because the
Terraform Registry expects them. The Argo Rollouts controller instead downloads a
**raw executable** and execs it, so `archives.formats` is `[binary]` here.

Assets published per tag:

```
rollouts-plugin-metric-instana_<version>_linux_amd64
rollouts-plugin-metric-instana_<version>_linux_arm64
rollouts-plugin-metric-instana_<version>_darwin_{amd64,arm64}
rollouts-plugin-metric-instana_<version>_SHA256SUMS
rollouts-plugin-metric-instana_<version>_SHA256SUMS.sig
```

The pipeline needs `GHP_PAT`, `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE`,
`GPG_FINGERPRINT`, `GIT_USER_NAME`, `GIT_USER_EMAIL` and `TAG` — the same
environment properties the Terraform provider already uses.

The controller verifies **sha256 only** — take the value from `SHA256SUMS`. The
GPG signature is defence in depth, not something Argo checks.

## Install

1. Publish the binary as a GitHub release (or host it anywhere the controller
   can reach), then put its URL + sha256 in
   `examples/argo-rollouts-config.yaml` and apply it. Air-gapped clusters should
   mount the binary and use a `file://` location instead.
2. Create the API token secret: `kubectl apply -f manifests/secret.yaml`.
3. Provide the API token to the controller as an env var:

   ```sh
   kubectl -n argo-rollouts set env deploy/argo-rollouts \
     INSTANA_API_TOKEN=- --from=secret/instana-api-token --keys=token
   ```

   (or add a `valueFrom.secretKeyRef` to the controller Deployment).
4. Restart the controller so it loads the plugin.

## Use

Apply an `AnalysisTemplate` (`examples/analysis-template.yaml`) referencing
`provider.plugin: instana/metrics`, and wire it into a Rollout step
(`examples/rollout.yaml`). Use `valueFrom.podTemplateHashValue: Latest` to scope
the query to the canary variant.

### Config fields

Supplied under `metric.provider.plugin["instana/metrics"]`.

| Field | Required | Default | Notes |
|-------|----------|---------|-------|
| `host` | yes | — | Instana tenant base URL |
| `metric` | yes | — | Instana metric id (`errors`, `latency`, `calls`, …) |
| `aggregation` | no | `MEAN` | `MEAN`, `SUM`, `P90`, … Instana lower-cases it in the series key |
| `granularity` | no | `60` | seconds; also part of the series key (`errors.mean.60`) |
| `timeWindowMs` | no | `60000` | look-back window |
| `clusterName` | no | — | scope the query; a pod-template-hash is **not** globally unique |
| `namespace` | no | — | scope the query, as above |
| `groupByTag` | no | `kubernetes.pod.label` | tag to group call groups by |
| `groupByTagKey` | no | `rollouts-pod-template-hash` | label key for `KEY_VALUE_PAIR` tags |
| `groupByTagEntity` | no | `DESTINATION` | |
| `selectGroup` | no | — | pick one group by name when canary and stable both report |
| `tagFilters` | no | — | extra scoping; `kubernetes.pod.label` needs `key` **and** `value` |
| `apiTokenEnv` | no | `INSTANA_API_TOKEN` | controller env var holding the token |
| `pageSize` | no | `20` | must be ≥ 1 |
| `insecure` | no | `false` | skip TLS verify (test only) |

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

## Open decisions before shipping

1. **Support model** — community OSS artifact vs supported product artifact.
2. **Secret handling** — controller env var (current) vs in-cluster Secret read
   with RBAC, if per-namespace tokens are ever needed.
3. **Auth scope** — least-privilege, read-only metrics API token.
4. **Metric coverage** — `errors`, `latency`, and any `calls`-based count
   (HTTP status codes via `tagFilters`, see *Empty results*). A
   `baselineComparison` mode against the stable group is the next ask.

The Instana API contract is settled: `POST /api/application-monitoring/analyze/call-groups`,
verified against a live tenant. Do **not** use `/metrics/services` — it accepts
`tagFilterExpression`, returns 200, and silently ignores it.
