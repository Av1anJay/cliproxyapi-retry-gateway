.PHONY: all clean linux darwin windows test fmt

PLUGIN_NAME := codex-retry-gateway
CLI_PROXY_API_ROOT ?= $(abspath $(CURDIR)/../CLIProxyAPI)

# Default: Linux .so (CGO required for the plugin ABI)
all: linux

linux: bin/$(PLUGIN_NAME)-go.so

darwin: bin/$(PLUGIN_NAME)-go.dylib

windows: bin/$(PLUGIN_NAME)-go.dll

bin/$(PLUGIN_NAME)-go.so: $(wildcard *.go) go.mod
	@mkdir -p bin
	CGO_ENABLED=1 go build -buildmode=c-shared -o bin/$(PLUGIN_NAME)-go.so .

bin/$(PLUGIN_NAME)-go.dylib: $(wildcard *.go) go.mod
	@mkdir -p bin
	CGO_ENABLED=1 go build -buildmode=c-shared -o bin/$(PLUGIN_NAME)-go.dylib .

bin/$(PLUGIN_NAME)-go.dll: $(wildcard *.go) go.mod
	@mkdir -p bin
	CGO_ENABLED=1 go build -buildmode=c-shared -o bin/$(PLUGIN_NAME)-go.dll .

# Run CLIProxyAPI tests (requires Go toolchain + the repo checked out at $(CLI_PROXY_API_ROOT))
test:
	go test ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin/ $(PLUGIN_NAME)-go.h $(PLUGIN_NAME)-go.dylib $(PLUGIN_NAME)-go.so $(PLUGIN_NAME)-go.dll
