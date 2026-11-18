GOFUMPT_VERSION := v0.12.0
GOLANGCI_LINT_VERSION := v2.15.0
BIN := $(CURDIR)/bin

.PHONY: tools fmt lint vet test check

tools:
	GOBIN=$(BIN) go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	GOBIN=$(BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

fmt:
	$(BIN)/golangci-lint fmt ./...

lint:
	$(BIN)/golangci-lint run ./...

vet:
	go vet ./...

test:
	go test -race ./...

check: lint vet test
