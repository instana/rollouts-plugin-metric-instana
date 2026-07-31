#!/usr/bin/env bash
# scripts/release-manual.sh — build release artifacts locally without GitHub Actions.
#
# The normal release path is: push a vX.Y.Z tag → GitHub Actions runs goreleaser.
# Use this script only when you need artifacts on a machine that cannot push a tag
# (e.g. air-gapped environment or a quick local smoke-test).
#
# Usage:
#   VERSION=1.2.3 ./scripts/release-manual.sh
#
# Output (in dist/):
#   rollouts-plugin-metric-instana_<VERSION>_linux_amd64
#   rollouts-plugin-metric-instana_<VERSION>_linux_arm64
#   rollouts-plugin-metric-instana_<VERSION>_SHA256SUMS
#
# After running:
#   1. Upload the three files in dist/ as GitHub release assets for tag v<VERSION>.
#   2. Copy the sha256 values from SHA256SUMS into examples/argo-rollouts-config.yaml.

set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

VERSION="${VERSION:?set VERSION before running, e.g. VERSION=0.1.0}"
COMMIT=$(git rev-parse --short HEAD)
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}"
BIN_PREFIX="dist/rollouts-plugin-metric-instana_${VERSION}"

mkdir -p dist

step() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

step "tests"
go test ./...

step "linux/amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "$LDFLAGS" -o "${BIN_PREFIX}_linux_amd64" .

step "linux/arm64"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "$LDFLAGS" -o "${BIN_PREFIX}_linux_arm64" .

step "SHA256SUMS"
SUMS_FILE="rollouts-plugin-metric-instana_${VERSION}_SHA256SUMS"
(cd dist && sha256sum \
  "rollouts-plugin-metric-instana_${VERSION}_linux_amd64" \
  "rollouts-plugin-metric-instana_${VERSION}_linux_arm64" \
  > "${SUMS_FILE}")

cat "dist/${SUMS_FILE}"
