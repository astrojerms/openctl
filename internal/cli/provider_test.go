package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/plugin"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestPluginSubcommandDispatchesArgs(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "request.json")
	pluginPath := filepath.Join(dir, "openctl-fake")
	body := `#!/bin/sh
if [ "$1" = "--capabilities" ]; then
  cat <<'JSON'
{
  "providerName": "fake",
  "protocolVersion": "1.0",
  "resources": [],
  "subcommands": [
    {
      "name": "logs",
      "short": "Fetch logs",
      "action": "agent.logs",
      "positionalArgs": [
        {"name": "cluster", "required": true}
      ],
      "flags": [
        {"name": "node", "short": "n", "type": "string", "required": true},
        {"name": "lines", "type": "int", "default": "100"},
        {"name": "follow", "type": "bool"}
      ]
    }
  ]
}
JSON
  exit 0
fi
cat > "$OPENCTL_CAPTURE"
printf '{"status":"success","message":"ok"}'
`
	if err := os.WriteFile(pluginPath, []byte(body), 0o755); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	oldConfig, oldTimeout, oldOutput := globalConfig, globalTimeout, outputFormat
	t.Cleanup(func() {
		globalConfig, globalTimeout, outputFormat = oldConfig, oldTimeout, oldOutput
	})
	globalConfig = &config.Config{
		Providers: map[string]*config.Provider{
			"fake": {
				Defaults: map[string]string{"region": "test"},
			},
		},
	}
	globalTimeout = time.Second
	outputFormat = "json"

	t.Setenv("OPENCTL_CAPTURE", capture)
	cmd := newProviderCommand(&plugin.Plugin{Name: "fake", Path: pluginPath})
	cmd.SetArgs([]string{"logs", "demo", "--node", "cp-1", "--lines", "25", "--follow"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured request: %v", err)
	}
	var req protocol.Request
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal request %s: %v", data, err)
	}
	if req.Action != "agent.logs" {
		t.Fatalf("action = %q, want agent.logs", req.Action)
	}
	if got := req.Args["cluster"]; got != "demo" {
		t.Fatalf("args.cluster = %v, want demo", got)
	}
	if got := req.Args["node"]; got != "cp-1" {
		t.Fatalf("args.node = %v, want cp-1", got)
	}
	if got := req.Args["lines"]; got != float64(25) {
		t.Fatalf("args.lines = %v (%T), want 25", got, got)
	}
	if got := req.Args["follow"]; got != true {
		t.Fatalf("args.follow = %v, want true", got)
	}
	if got := req.Config.Defaults["region"]; got != "test" {
		t.Fatalf("config.defaults.region = %q, want test", got)
	}
}
