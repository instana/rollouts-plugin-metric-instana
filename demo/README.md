# Demo: Argo Rollouts rolls back a canary using Instana metrics

Shows the thing the customer asked for: Argo Rollouts queries Instana mid-canary
and **automatically rolls back** when the new version is unhealthy.

```sh
./hack/demo.sh up      # kind + Argo Rollouts + plugin + stub Instana + Rollout
./hack/demo.sh good    # healthy canary  -> analysis passes -> promoted
./hack/demo.sh bad     # degraded canary -> analysis fails  -> AUTO ROLLBACK
./hack/demo.sh status  # watch it
./hack/demo.sh logs    # controller + stub logs, every query the plugin made
./hack/demo.sh down    # delete the cluster
```

Requires `docker`, `kind`, `kubectl`, `go`. The `kubectl-argo-rollouts` plugin
makes `status` much nicer but is optional.

## The two modes

**`MODE=stub` (default).** A stub Instana runs in-cluster. It implements the one
endpoint the plugin calls, with the response shape verified against a real
tenant, and `POST /admin/error-rate` flips it between healthy and degraded
mid-demo. No agent, no instrumentation, no tenant — so the demo is deterministic
and always works.

**`MODE=live`.** Points the plugin at a real tenant:

```sh
export INSTANA_HOST=https://<tenant>.instana.io
export INSTANA_API_TOKEN=<read-only token>
MODE=live ./hack/demo.sh up
```

This needs an Instana agent on the cluster and an instrumented app emitting
spans, and it only produces a canary/stable split once the
`rollouts-pod-template-hash` pod label reaches Instana. That last point is the
one thing not yet confirmed on a live tenant — see `ONBOARDING.md` §3.

## What `bad` proves

1. The canary ReplicaSet scales up and takes 25% of traffic.
2. An `AnalysisRun` starts. Argo passes the canary's `pod-template-hash` into
   the `AnalysisTemplate` via `valueFrom.podTemplateHashValue: Latest`.
3. The plugin queries Instana, **scoped to that hash** — so it measures the
   canary, not the whole service.
4. The stub reports a 90% error rate. `successCondition: result <= 0.05` fails.
5. `AnalysisRun` → `Failed`, Rollout → `Degraded`, canary scaled back to zero.

`./hack/demo.sh logs` shows each query the plugin made, including the group it
scoped to.

## Notes

- The stub echoes back whatever group the plugin filtered on, because it cannot
  know the canary hash in advance. Real Instana returns zero groups for an
  unknown value — which the plugin treats as an error, never as a healthy zero.
- After `bad` the Rollout is `Degraded`. Run `good` to roll forward again.
- The plugin is delivered via an initContainer that copies the binary into the
  controller pod, and registered with `location: file:///plugins/...`. That is
  deliberately the **air-gapped** install path; clusters with egress can point
  `location` at a release URL plus its sha256 instead.
- `hack/demo.sh` refuses to run against any context other than its own kind
  cluster, so it cannot touch a shared cluster. Override with `FORCE=1` only if
  you mean it.
