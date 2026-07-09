# Argo Rollouts metric provider plugin for Instana

Lets [Argo Rollouts](https://argo-rollouts.readthedocs.io/) query Instana
during a canary or blue-green rollout and automatically promote or roll back
based on the result — the same role the built-in Datadog and New Relic
providers play.

This closes the gap left by the Instana ↔ Argo Rollouts *visibility* feature
(R321), which only surfaces rollout state in the Instana dashboard and does not
feed the promotion decision.

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

## Build

```sh
make tidy      # resolve module versions (needs network the first time)
make test
make build-all # cross-compile linux amd64/arm64 into dist/
make checksums # sha256 for the controller ConfigMap
```

## Release

Releases are cut by goreleaser (`.goreleaser.yml`) on a `v*` tag, mirroring the
pipeline used by `instana/terraform-provider-instana`: cross-compiled binaries,
a `SHA256SUMS` file, and a detached GPG signature over it.

One deliberate difference from that provider: it publishes **zips**, but the
Argo Rollouts controller downloads a **raw executable** and verifies its sha256,
so `archives.formats` is `[binary]` here.

Assets published per tag:

```
rollouts-metric-plugin-instana_<version>_linux_amd64
rollouts-metric-plugin-instana_<version>_linux_arm64
rollouts-metric-plugin-instana_<version>_darwin_{amd64,arm64}
rollouts-metric-plugin-instana_<version>_SHA256SUMS
rollouts-metric-plugin-instana_<version>_SHA256SUMS.sig
```

CI needs two secrets: `GPG_PRIVATE_KEY`, `GPG_PASSPHRASE`.

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

| Field | Required | Default | Notes |
|-------|----------|---------|-------|
| `host` | yes | — | Instana tenant base URL |
| `metric` | yes | — | Instana metric id (`errors`, `latency`, `calls`, …) |
| `aggregation` | no | `MEAN` | `MEAN`, `SUM`, `P90`, … |
| `timeWindowMs` | no | `60000` | look-back window |
| `applicationId` | no | — | scope to an Instana application |
| `apiTokenEnv` | no | `INSTANA_API_TOKEN` | controller env var holding the token |
| `tagFilters` | no | — | scope the query (e.g. pod-template-hash) |
| `insecure` | no | `false` | skip TLS verify (test only) |

## Open decisions before shipping

1. **Instana API contract** — confirm `POST /api/application-monitoring/metrics`
   (request/response shape in `instana.go`) is the right stable endpoint for the
   target release and that it supports pod-label tag filtering at the needed
   granularity. Adjust `metricsRequest` / `metricsResponse` accordingly.
2. **Secret handling** — env var (current) vs in-cluster Secret read with RBAC.
3. **Repo / ownership** — host under `argoproj-labs` (discoverability, parity
   feel) or the `instana` org; assign a maintenance owner.
4. **Auth scope** — least-privilege, read-only metrics API token.
