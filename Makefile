# Makefile for bucket-cli.
#
# Cross-compiles static, pure-Go binaries into ./bin for:
#   - linux/amd64   (x86_64)
#   - linux/arm64   (aarch64)
#   - darwin/arm64  (Apple Silicon)
#   - windows/amd64 (x86_64)
#
# No C toolchain is needed (CGO is disabled).

BINARY  := bucket-cli
BIN_DIR := bin
PKG     := .

# Version stamped into the binary. Defaults to the latest git tag (falling back
# to the short commit), and can be overridden: `make all VERSION=1.2.3`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Strip debug info (-s -w), drop local paths (-trimpath) and stamp the version.
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
BUILD   := CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)"

.PHONY: all linux-amd64 linux-arm64 darwin-arm64 windows-amd64 tidy clean help

all: linux-amd64 linux-arm64 darwin-arm64 windows-amd64 ## Build every target

linux-amd64: $(BIN_DIR) ## Build linux/amd64 (x86_64)
	GOOS=linux GOARCH=amd64 $(BUILD) -o $(BIN_DIR)/$(BINARY)-linux-amd64 $(PKG)

linux-arm64: $(BIN_DIR) ## Build linux/arm64 (aarch64)
	GOOS=linux GOARCH=arm64 $(BUILD) -o $(BIN_DIR)/$(BINARY)-linux-arm64 $(PKG)

darwin-arm64: $(BIN_DIR) ## Build darwin/arm64 (Apple Silicon)
	GOOS=darwin GOARCH=arm64 $(BUILD) -o $(BIN_DIR)/$(BINARY)-darwin-arm64 $(PKG)

windows-amd64: $(BIN_DIR) ## Build windows/amd64 (x86_64)
	GOOS=windows GOARCH=amd64 $(BUILD) -o $(BIN_DIR)/$(BINARY)-windows-amd64.exe $(PKG)

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

tidy: ## Sync go.mod/go.sum
	go mod tidy

clean: ## Remove the bin directory
	rm -rf $(BIN_DIR)

help: ## List available targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
