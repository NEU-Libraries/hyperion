# Cross-compiled binaries land in the repo root as hyperion-<os>-<arch>,
# which .gitignore already covers (/hyperion-*).

GOFLAGS := -trimpath
export CGO_ENABLED := 0

.PHONY: build mac mac-arm64 mac-amd64 clean test lint fmt check tools

# golangci-lint is pinned so every machine and CI lint alike.
# `make tools` installs it into $(go env GOPATH)/bin.
GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null || echo $(shell go env GOPATH)/bin/golangci-lint)

build:
	go build $(GOFLAGS) -o hyperion .

mac: mac-arm64 mac-amd64

mac-arm64:
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o hyperion-darwin-arm64 .

mac-amd64:
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o hyperion-darwin-amd64 .

clean:
	rm -f hyperion hyperion-darwin-arm64 hyperion-darwin-amd64

test:
	go test ./...

# lint runs the linters in .golangci.yml; fmt rewrites files to match.
lint:
	@test -x $(GOLANGCI_LINT) || { echo "golangci-lint not found: run 'make tools'"; exit 1; }
	$(GOLANGCI_LINT) run ./...

fmt:
	@test -x $(GOLANGCI_LINT) || { echo "golangci-lint not found: run 'make tools'"; exit 1; }
	$(GOLANGCI_LINT) fmt ./...

# check is what CI runs: tests, then lint.
check: test lint

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
