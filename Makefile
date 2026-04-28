.PHONY: all build build-cli build-plugins build-plugin-proxmox build-plugin-k3s build-plugin-k3s-agent build-plugin-k3s-agent-linux install clean test test-e2e fmt lint modernize modernize-check

# Binary names
CLI_BINARY=openctl
PLUGIN_PROXMOX_BINARY=openctl-proxmox
PLUGIN_K3S_BINARY=openctl-k3s
PLUGIN_K3S_AGENT_BINARY=openctl-k3s-agent

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

# Build the k3s agent for the host platform (for local dev/testing).
build-plugin-k3s-agent:
	@echo "Building openctl-k3s-agent (native)..."
	@mkdir -p $(BUILD_DIR)
	cd $(K3S_PLUGIN_DIR) && go build $(GOFLAGS) -o ../../$(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY) ./cmd/openctl-k3s-agent

# Cross-compile the k3s agent for all Linux architectures we deploy to.
# These artifacts get uploaded to k3s nodes during cluster create.
build-plugin-k3s-agent-linux:
	@echo "Building openctl-k3s-agent for linux/amd64, linux/arm64, linux/arm..."
	@mkdir -p $(BUILD_DIR)
	cd $(K3S_PLUGIN_DIR) && GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -o ../../$(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-amd64 ./cmd/openctl-k3s-agent
	cd $(K3S_PLUGIN_DIR) && GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -o ../../$(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-arm64 ./cmd/openctl-k3s-agent
	cd $(K3S_PLUGIN_DIR) && GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -o ../../$(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-armv7 ./cmd/openctl-k3s-agent

install: build build-plugin-k3s-agent-linux
	@echo "Installing binaries..."
	@mkdir -p $(HOME)/.openctl/plugins/k3s-agents
	cp $(BUILD_DIR)/$(CLI_BINARY) $(GOBIN)/ 2>/dev/null || cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/
	cp $(BUILD_DIR)/$(PLUGIN_PROXMOX_BINARY) $(HOME)/.openctl/plugins/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_BINARY) $(HOME)/.openctl/plugins/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-amd64 $(HOME)/.openctl/plugins/k3s-agents/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-arm64 $(HOME)/.openctl/plugins/k3s-agents/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-armv7 $(HOME)/.openctl/plugins/k3s-agents/

install-cli: build-cli
	cp $(BUILD_DIR)/$(CLI_BINARY) $(GOBIN)/ 2>/dev/null || cp $(BUILD_DIR)/$(CLI_BINARY) /usr/local/bin/

install-plugins: build-plugins build-plugin-k3s-agent-linux
	@mkdir -p $(HOME)/.openctl/plugins/k3s-agents
	cp $(BUILD_DIR)/$(PLUGIN_PROXMOX_BINARY) $(HOME)/.openctl/plugins/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_BINARY) $(HOME)/.openctl/plugins/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-amd64 $(HOME)/.openctl/plugins/k3s-agents/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-arm64 $(HOME)/.openctl/plugins/k3s-agents/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-armv7 $(HOME)/.openctl/plugins/k3s-agents/

install-plugin-proxmox: build-plugin-proxmox
	@mkdir -p $(HOME)/.openctl/plugins
	cp $(BUILD_DIR)/$(PLUGIN_PROXMOX_BINARY) $(HOME)/.openctl/plugins/

install-plugin-k3s: build-plugin-k3s build-plugin-k3s-agent-linux
	@mkdir -p $(HOME)/.openctl/plugins/k3s-agents
	cp $(BUILD_DIR)/$(PLUGIN_K3S_BINARY) $(HOME)/.openctl/plugins/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-amd64 $(HOME)/.openctl/plugins/k3s-agents/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-arm64 $(HOME)/.openctl/plugins/k3s-agents/
	cp $(BUILD_DIR)/$(PLUGIN_K3S_AGENT_BINARY)-linux-armv7 $(HOME)/.openctl/plugins/k3s-agents/

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
