#!/usr/bin/env bash
#
# Single verification entrypoint. Developers, the pre-commit hook, and CI all
# run this, so "it passed locally" and "it passed in CI" mean the same thing.
#
#   ./ci/verify.sh
#
# Optional tools are skipped with a notice rather than failing the run, so a
# fresh clone verifies without installing anything beyond Go.

set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail=0
step()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
skip()  { printf '\033[1;33m[skip] %s\033[0m\n' "$*"; }
bad()   { printf '\033[1;31m[fail] %s\033[0m\n' "$*"; fail=1; }

step "gofmt"
unformatted="$(gofmt -l . | grep -v '^dist/' || true)"
if [[ -n "$unformatted" ]]; then
  bad "these files are not gofmt'd:"; echo "$unformatted"
else
  echo "  clean"
fi

step "go vet"
go vet ./... || bad "go vet reported problems"

step "unit tests"
go test ./... || bad "unit tests failed"

step "build"
CGO_ENABLED=0 go build -o dist/rollouts-metric-plugin-instana . || bad "build failed"

step "integration tests (real binary over go-plugin RPC)"
# Scrub INSTANA_API_TOKEN: go-plugin appends os.Environ() after cmd.Env, so a
# developer's real token would otherwise reach the stub.
env -u INSTANA_API_TOKEN go test -tags integration ./test/integration/ || bad "integration tests failed"

step "govulncheck"
if command -v govulncheck >/dev/null 2>&1; then
  govulncheck ./... || bad "govulncheck found vulnerabilities"
else
  skip "govulncheck not installed (go install golang.org/x/vuln/cmd/govulncheck@latest)"
fi

step "goreleaser check"
if ! command -v goreleaser >/dev/null 2>&1; then
  skip "goreleaser not installed"
elif ! git remote get-url origin >/dev/null 2>&1; then
  skip "no git remote configured; goreleaser cannot validate the release section"
else
  goreleaser check || bad "goreleaser config is invalid"
fi

echo
if [[ "$fail" -ne 0 ]]; then
  printf '\033[1;31mverify FAILED\033[0m\n'
  exit 1
fi
printf '\033[1;32mverify passed\033[0m\n'
