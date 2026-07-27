#!/usr/bin/env bash
# Pipeline runs are at: https://console-openshift-console.apps.instana-cicd-1.cp.fyre.ibm.com/k8s/ns/sonarqube/tekton.dev~v1~PipelineRun

set -eo pipefail

# Install common utilities
apt-get update -y
apt-get install -y git make curl unzip gcc

# Install Go
curl -sSL https://go.dev/dl/go1.21.linux-amd64.tar.gz | tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin
export GOPATH=/workdir/go
mkdir -p "${GOPATH}"

echo "Go version: $(go version)"

# Run tests with coverage
echo "Running Go tests with coverage"
go test ./... -coverprofile=coverage.out -covermode=atomic

echo "sonar.go.coverage.reportPaths=coverage.out" >> sonar-project.properties
