GOFUMPT_VERSION := v0.12.0
GOLANGCI_LINT_VERSION := v2.15.0
BIN := $(CURDIR)/bin

.PHONY: tools fmt lint vet test test-integration hooks check

tools:
	GOBIN=$(BIN) go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	GOBIN=$(BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(BIN)/gofumpt $(BIN)/golangci-lint:
	$(MAKE) tools

fmt: $(BIN)/gofumpt $(BIN)/golangci-lint
	$(BIN)/golangci-lint fmt ./...

lint: $(BIN)/gofumpt $(BIN)/golangci-lint
	$(BIN)/golangci-lint run ./...

vet:
	go vet ./...

test:
	go test -race ./...

test-integration:
	@test -n "$(TEST_VALKEY_ADDR)" || { echo "set TEST_VALKEY_ADDR to a running valkey, for example TEST_VALKEY_ADDR=127.0.0.1:16379 make test-integration (the compose valkey)" >&2; exit 1; }
	@test -n "$(TEST_DATABASE_URL)" || { echo "set TEST_DATABASE_URL to a running postgres, for example TEST_DATABASE_URL=postgres://se_app:dev-only-se-app-password@127.0.0.1:15432/secret_externalizer_test make test-integration (the compose postgres)" >&2; exit 1; }
	TEST_VALKEY_ADDR="$(TEST_VALKEY_ADDR)" TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

hooks: $(BIN)/gofumpt $(BIN)/golangci-lint
	bash .claude/hooks/hooks_test.sh

check: lint vet test hooks
