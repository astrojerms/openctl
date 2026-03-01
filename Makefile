.PHONY: all build build-cli build-plugins build-plugin-proxmox build-plugin-k3s install clean test test-e2e fmt lint modernize modernize-check

# Binary names
CLI_BINARY=openctl
PLUGIN_PROXMOX_BINARY=openctl-proxmox
PLUGIN_K3S_BINARY=openctl-k3s

# Build directories
BUILD_DIR=bin
PROXMOX_PLUGIN_DIR=plugins/proxmox
K3S_PLUGIN_DIR=plugins/k3s

# Go settings
GOFLAGS=-ldflags="-s -w"
export GOWORK=off

all: build

build: build-cli build-plugins

build-cli:
	@echo "Building openctl CLI..."
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -o $(BUILD_DIR)/$(CLI_BINARY) ./cmd/openctl

build-plugins: build-plugin-proxmox build-plugin-k3s

build-plugin-proxmox:
	@echo "Building openctl-proxmox plugin..."
	@mkdir -p $(BUILD_DIR)
	cd $(PROXMOX_PLUGIN_DIR) && go build $(GOFLAGS) -o ../../$(BUILD_DIR)/$(PLUGIN_PROXMOX_BINARY) ./cmd/openctl-proxmox

build-plugin-k3s:
	@echo "Building openctl-k3s plugin..."
	@mkdir -p $(BUILD_DIR)
	cd $(K3S_PLUGIN_DIR) && go build $(GOFLAGS) -o ../../$(BUILD_DIR)/$(PLUGIN_K3S_BINARY) ./cmd/openctl-k3s

install: build
	@echo "Installing binaries..."
	@mkdir -p $(HOME)/.openctl/plugins
	cp $(BUILD_DIR)/$(CLI_BINARY) $(GOBIN)/ 2>/dev/null || cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/
	cp $(BUILD_DIR)/$(PLUGIN_PROXMOX_BINARY) $(HOME)/.openctl/plugins/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_BINARY) $(HOME)/.openctl/plugins/

install-cli: build-cli
	cp $(BUILD_DIR)/$(CLI_BINARY) $(GOBIN)/ 2>/dev/null || cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/

install-plugins: build-plugins
	@mkdir -p $(HOME)/.openctl/plugins
	cp $(BUILD_DIR)/$(PLUGIN_PROXMOX_BINARY) $(HOME)/.openctl/plugins/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_BINARY) $(HOME)/.openctl/plugins/

install-plugin-proxmox: build-plugin-proxmox
	@mkdir -p $(HOME)/.openctl/plugins
	cp $(BUILD_DIR)/$(PLUGIN_PROXMOX_BINARY) $(HOME)/.openctl/plugins/

install-plugin-k3s: build-plugin-k3s
	@mkdir -p $(HOME)/.openctl/plugins
	cp $(BUILD_DIR)/$(PLUGIN_K3S_BINARY) $(HOME)/.openctl/plugins/

clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)

test:
	go test ./...
	cd $(PROXMOX_PLUGIN_DIR) && go test ./...
	cd $(K3S_PLUGIN_DIR) && go test ./...

test-e2e: build-cli
	go test -v ./test/e2e/...

fmt:
	go fmt ./...
	cd $(PROXMOX_PLUGIN_DIR) && go fmt ./...
	cd $(K3S_PLUGIN_DIR) && go fmt ./...

lint:
	golangci-lint run ./...
	cd $(PROXMOX_PLUGIN_DIR) && golangci-lint run --config=../../.golangci.yml ./...
	cd $(K3S_PLUGIN_DIR) && golangci-lint run --config=../../.golangci.yml ./...

# Download dependencies
deps:
	go mod download
	go mod tidy
	cd $(PROXMOX_PLUGIN_DIR) && go mod download && go mod tidy
	cd $(K3S_PLUGIN_DIR) && go mod download && go mod tidy

# Modernize code using latest Go idioms
modernize:
	@echo "Installing modernize tool..."
	@go install golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest
	@echo "Running modernize on root module..."
	modernize -fix ./...
	@echo "Running modernize on proxmox plugin..."
	cd $(PROXMOX_PLUGIN_DIR) && modernize -fix ./...
	@echo "Running modernize on k3s plugin..."
	cd $(K3S_PLUGIN_DIR) && modernize -fix ./...
	@echo "Done! Review changes with 'git diff'"

# Check for modernize suggestions without applying fixes
modernize-check:
	@go install golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest
	@echo "Checking root module..."
	@modernize ./... || true
	@echo "Checking proxmox plugin..."
	@cd $(PROXMOX_PLUGIN_DIR) && modernize ./... || true
	@echo "Checking k3s plugin..."
	@cd $(K3S_PLUGIN_DIR) && modernize ./... || true
