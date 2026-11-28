package templates

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

var starterResourceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ComposeApplication starts a private, resource-limited welcome service on a VM.
func ComposeApplication() *Template {
	params := []ParamDef{
		{Name: "name", Type: "string", Description: "Application resource and Compose project name.", Required: true},
		{Name: "vmName", Type: "string", Description: "Managed Docker-host VirtualMachine resource name.", Required: true, OptionsKind: "VirtualMachine"},
		{Name: "sshUser", Type: "string", Description: "Guest SSH user with noninteractive sudo access.", Default: "ubuntu"},
		{Name: "privateKeyFile", Type: "string", Description: "SSH private-key path on the controller host; never key contents.", Required: true},
		{Name: "knownHostsFile", Type: "string", Description: "Verified SSH known_hosts path on the controller host.", Required: true},
		{Name: "port", Type: "int", Description: "Guest loopback HTTP port, 1 through 65535.", Default: 18080},
		{Name: "welcomeText", Type: "string", Description: "Non-sensitive welcome text served by the starter.", Default: "OpenCtl is ready"},
	}
	return &Template{
		Name:        "compose-application",
		DisplayName: "Compose Application",
		Description: "Standalone welcome application on a managed Docker VM, pinned to official BusyBox and bound only to guest loopback. Requires verified SSH trust and controller-side private-key paths; no secret files or public listeners are created.",
		APIVersion:  "apps.openctl.io/v1",
		Kind:        "ComposeApplication",
		Parameters:  params,
		Render: func(p map[string]any) (*protocol.Resource, error) {
			if err := validateStarterParams(params, p); err != nil {
				return nil, err
			}
			name := getString(p, params, "name")
			vmName := getString(p, params, "vmName")
			if !starterResourceName.MatchString(name) || !starterResourceName.MatchString(vmName) {
				return nil, fmt.Errorf("name and vmName must be lowercase resource names containing letters, digits and interior hyphens")
			}
			port, err := starterInt(p, params, "port", 1, 65535)
			if err != nil {
				return nil, err
			}
			greeting := getString(p, params, "welcomeText")
			if strings.ContainsRune(greeting, '\x00') {
				return nil, fmt.Errorf("parameter %q cannot contain NUL bytes", "welcomeText")
			}
			// Marshal user data as YAML, never shell source. Compose's interpolation
			// must also be escaped so literal dollar expressions reach the guest.
			compose, err := yaml.Marshal(map[string]any{
				"services": map[string]any{
					"welcome": map[string]any{
						"image":       "busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec",
						"environment": map[string]string{"WELCOME_TEXT": strings.ReplaceAll(greeting, "$", "$$")},
						"command":     []string{"sh", "-ec", `printf '%s\n' "$$WELCOME_TEXT" > /www/index.html; exec httpd -f -p 8080 -h /www`},
						"ports":       []string{fmt.Sprintf("127.0.0.1:%d:8080", port)},
						"restart":     "unless-stopped",
						"cpus":        0.25,
						"mem_limit":   "32m",
						"read_only":   true,
						"tmpfs":       []string{"/www:rw,noexec,nosuid,size=1m,mode=0755"},
						"healthcheck": map[string]any{
							"test":     []string{"CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/"},
							"interval": "5s", "timeout": "3s", "retries": 12, "start_period": "5s",
						},
					},
				},
			})
			if err != nil {
				return nil, fmt.Errorf("encode welcome Compose document: %w", err)
			}
			return &protocol.Resource{
				APIVersion: "apps.openctl.io/v1",
				Kind:       "ComposeApplication",
				Metadata: protocol.ResourceMetadata{
					Name:        name,
					Annotations: map[string]string{"openctl.io/template": "compose-application"},
				},
				Spec: map[string]any{
					"target": map[string]any{
						"host": map[string]any{"$ref": map[string]any{
							"apiVersion": "proxmox.openctl.io/v1", "kind": "VirtualMachine", "name": vmName, "field": "status.ip",
						}},
						"user":           getString(p, params, "sshUser"),
						"privateKeyFile": getString(p, params, "privateKeyFile"),
						"knownHostsFile": getString(p, params, "knownHostsFile"),
						"sudo":           true,
					},
					"project":   name,
					"directory": "/opt/openctl/apps/" + name,
					"compose":   string(compose),
					"probe":     map[string]any{"url": fmt.Sprintf("http://127.0.0.1:%d/", port)},
				},
			}, nil
		},
	}
}
