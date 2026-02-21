.PHONY: all build build-cli build-plugin install clean test fmt lint

# Binary names
CLI_BINARY=openctl
PLUGIN_BINARY=openctl-proxmox

# Build directories
BUILD_DIR=bin
PLUGIN_DIR=plugins/proxmox

# Go settings
GOFLAGS=-ldflags="-s -w"
export GOWORK=off

all: build

build: build-cli build-plugin

build-cli:
	@echo "Building openctl CLI..."
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -o $(BUILD_DIR)/$(CLI_BINARY) ./cmd/openctl

build-plugin:
	@echo "Building openctl-proxmox plugin..."
	@mkdir -p $(BUILD_DIR)
	cd $(PLUGIN_DIR) && go build $(GOFLAGS) -o ../../$(BUILD_DIR)/$(PLUGIN_BINARY) ./cmd/openctl-proxmox

install: build
	@echo "Installing binaries..."
	@mkdir -p $(HOME)/.openctl/plugins
	cp $(BUILD_DIR)/$(CLI_BINARY) $(GOBIN)/ 2>/dev/null || cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/
	cp $(BUILD_DIR)/$(PLUGIN_BINARY) $(HOME)/.openctl/plugins/

install-cli: build-cli
	cp $(BUILD_DIR)/$(CLI_BINARY) $(GOBIN)/ 2>/dev/null || cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/

install-plugin: build-plugin
	@mkdir -p $(HOME)/.openctl/plugins
	cp $(BUILD_DIR)/$(PLUGIN_BINARY) $(HOME)/.openctl/plugins/

clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)

test:
	go test ./...
	cd $(PLUGIN_DIR) && go test ./...

fmt:
	go fmt ./...
	cd $(PLUGIN_DIR) && go fmt ./...

lint:
	golangci-lint run ./...
	cd $(PLUGIN_DIR) && golangci-lint run ./...

# Download dependencies
deps:
	go mod download
	go mod tidy
	cd $(PLUGIN_DIR) && go mod download && go mod tidy
