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

## Release

Releases are published via GitHub Actions ([`.github/workflows/release.yml`](.github/workflows/release.yml))
using [goreleaser](https://goreleaser.com/). Pushing a `vX.Y.Z` tag triggers
the workflow automatically.

### To cut a release

```sh
git tag -a v1.2.3 -m "Release v1.2.3"
git push origin v1.2.3
```

goreleaser cross-compiles, generates SHA256SUMS, GPG-signs the checksum file,
and publishes a GitHub Release. The Argo Rollouts controller downloads a
**raw executable** (not a zip), so `archives.formats` is `[binary]`.

### Assets published per tag

```
rollouts-plugin-metric-instana_<version>_linux_amd64
rollouts-plugin-metric-instana_<version>_linux_arm64
rollouts-plugin-metric-instana_<version>_darwin_amd64
rollouts-plugin-metric-instana_<version>_darwin_arm64
rollouts-plugin-metric-instana_<version>_SHA256SUMS
rollouts-plugin-metric-instana_<version>_SHA256SUMS.sig
```

### One-time secrets setup (Settings → Secrets and variables → Actions)

| Secret | How to obtain |
|--------|---------------|
| `GPG_PRIVATE_KEY` | `gpg --armor --export-secret-keys <fingerprint>` |
| `GPG_PASSPHRASE` | Passphrase for the key above |

`GITHUB_TOKEN` is provided automatically by Actions — no manual secret needed.

The controller verifies **sha256 only** — take the value from `SHA256SUMS`. The
GPG signature is defence in depth and is not verified by Argo.

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

## Open decisions before shipping

1. **Support model** — community OSS artifact vs supported product artifact.
2. **Secret handling** — controller env var (current) vs in-cluster Secret read
   with RBAC, if per-namespace tokens are ever needed.
3. **Auth scope** — least-privilege, read-only metrics API token.
4. **Metric coverage** — `errors` and `latency` today; HTTP status codes are the
   next ask.

The Instana API contract is settled: `POST /api/application-monitoring/analyze/call-groups`,
verified against a live tenant. Do **not** use `/metrics/services` — it accepts
`tagFilterExpression`, returns 200, and silently ignores it.
