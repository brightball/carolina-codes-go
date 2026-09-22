# Pinned versions — keep in sync with mise.toml.
GOSEC_VERSION ?= v2.29.0
GOVULNCHECK_VERSION ?= v1.8.0
GITLEAKS_VERSION ?= v8.30.1
GOLANGCI_LINT_VERSION ?= v2.13.2

GOBIN ?= $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: test sast vuln gitleaks lint check hooks

test:
	go test -race ./...

sast:
	@command -v gosec >/dev/null || go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	gosec ./...

vuln:
	@command -v govulncheck >/dev/null || GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	govulncheck ./...

gitleaks:
	@command -v gitleaks >/dev/null || go install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)
	gitleaks detect --source .

lint:
	@command -v golangci-lint >/dev/null || GOTOOLCHAIN=auto go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	golangci-lint run

check: test sast vuln gitleaks lint

hooks:
	pre-commit install
	git config core.hooksPath .githooks
