.PHONY: all build build-cli build-controller build-plugins build-plugin-proxmox build-plugin-k3s build-plugin-k3s-agent build-plugin-k3s-agent-linux install clean test test-e2e fmt lint modernize modernize-check generate ui ui-install ui-clean

# Binary names
CLI_BINARY=openctl
CONTROLLER_BINARY=openctl-controller
PLUGIN_PROXMOX_BINARY=openctl-proxmox
PLUGIN_K3S_BINARY=openctl-k3s
PLUGIN_K3S_AGENT_BINARY=openctl-k3s-agent

# Build directories
BUILD_DIR=bin
PROXMOX_PLUGIN_DIR=plugins/proxmox
K3S_PLUGIN_DIR=plugins/k3s
UI_DIR=ui
UI_OUT=internal/controller/server/uiassets/dist

# Go settings
GOFLAGS=-ldflags="-s -w"
export GOWORK=off

all: build

build: build-cli build-controller build-plugins

build-cli:
	@echo "Building openctl CLI..."
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -o $(BUILD_DIR)/$(CLI_BINARY) ./cmd/openctl

build-controller:
	@echo "Building openctl-controller..."
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -o $(BUILD_DIR)/$(CONTROLLER_BINARY) ./cmd/openctl-controller

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
	cp $(BUILD_DIR)/$(CONTROLLER_BINARY) $(GOBIN)/ 2>/dev/null || cp $(BUILD_DIR)/$(CONTROLLER_BINARY) /usr/local/bin/
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

clean: ui-clean
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)

# Build the browser UI (Vite + Svelte). Output goes directly into the
# controller's embed.FS root ($(UI_OUT)) so a subsequent `make build` bakes
# it into the binary. ui-install runs `npm ci` against committed
# package-lock.json — keep it separate from ui so iterative builds don't
# re-resolve deps on every invocation.
ui-install:
	@echo "Installing UI dependencies..."
	cd $(UI_DIR) && npm install

ui: ui-install
	@echo "Building UI ($(UI_DIR) -> $(UI_OUT))..."
	cd $(UI_DIR) && npm run build
	@# Vite's emptyOutDir wipes the .gitkeep marker we use to keep the
	@# dist/ directory present in git for fresh checkouts; restore it so
	@# `go:embed all:uiassets/dist` keeps working after `make clean` etc.
	@touch $(UI_OUT)/.gitkeep

ui-clean:
	@echo "Cleaning UI build output..."
	@# Use find rather than rm -rf $(UI_OUT) so we don't blow away the
	@# directory itself (embed.FS needs it to exist) or the .gitkeep.
	@find $(UI_OUT) -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
	@rm -rf $(UI_DIR)/node_modules

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

# Regenerate gRPC bindings from .proto files. Requires protoc + the Go
# plugins (see DEVELOPMENT.md for install instructions). The generated
# files are committed so building the project doesn't require protoc.
generate:
	@echo "Regenerating gRPC + gateway bindings..."
	@GW1=$$(go env GOMODCACHE)/github.com/grpc-ecosystem/grpc-gateway@v1.16.0/third_party/googleapis; \
	test -d "$$GW1" || (echo "missing $$GW1 — run: go mod download github.com/grpc-ecosystem/grpc-gateway" && exit 1); \
	protoc \
		--proto_path=pkg/api/v1 \
		--proto_path=$$GW1 \
		--go_out=pkg/api/v1 --go_opt=paths=source_relative \
		--go-grpc_out=pkg/api/v1 --go-grpc_opt=paths=source_relative \
		--grpc-gateway_out=pkg/api/v1 --grpc-gateway_opt=paths=source_relative \
		pkg/api/v1/api.proto

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
