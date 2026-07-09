PLUGIN_NAME := rollouts-plugin-metric-instana
BIN_DIR := dist

.PHONY: all tidy verify hooks secrets-baseline test test-integration build build-all clean

all: tidy test build

tidy:
	go mod tidy

# The single entrypoint CI and the pre-commit hook both run.
verify:
	./ci/verify.sh

# Point git at the tracked hooks directory.
hooks:
	git config core.hooksPath .githooks
	@echo "pre-commit hook installed (bypass once with: git commit --no-verify)"

# Regenerate after auditing new findings: detect-secrets scan --baseline .secrets.baseline
secrets-baseline:
	detect-secrets scan --exclude-files '^dist/' --exclude-files '^go\.sum$$' > .secrets.baseline

test:
	go test ./...

# Drives the compiled binary over go-plugin RPC, exactly as the controller
# does. Instana is stubbed; no tenant required.
test-integration: build
	go test -tags integration ./test/integration/ -v

build:
	CGO_ENABLED=0 go build -o $(BIN_DIR)/$(PLUGIN_NAME) .

# Cross-compile the artifacts the controller downloads.
build-all:
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o $(BIN_DIR)/$(PLUGIN_NAME)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -o $(BIN_DIR)/$(PLUGIN_NAME)-linux-arm64 .

checksums: build-all
	cd $(BIN_DIR) && sha256sum $(PLUGIN_NAME)-* > checksums.txt

clean:
	rm -rf $(BIN_DIR)
