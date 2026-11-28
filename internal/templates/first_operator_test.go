package templates

import (
	"maps"
	"math"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/internal/schema"
)

func dockerHostParams() map[string]any {
	return map[string]any{
		"name": "docker-test", "context": "lab", "node": "pve1",
		"templateVMID": 9000, "sshKey": "ssh-ed25519 AAAA test",
	}
}

func composeStarterParams() map[string]any {
	return map[string]any{
		"name": "welcome", "vmName": "docker-test",
		"privateKeyFile": "/controller/ssh/id_ed25519", "knownHostsFile": "/controller/ssh/known_hosts",
	}
}

func TestDockerHostVMSizingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]any
		cores  int
		memory int
		disk   string
	}{
		{"defaults", nil, 2, 2048, "16G"},
		{"acceptance override", map[string]any{"cpuCores": 1.0, "memoryMiB": 1024.0, "diskGiB": 8.0}, 1, 1024, "8G"},
		{"minimum memory", map[string]any{"memoryMiB": 512}, 2, 512, "16G"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := dockerHostParams()
			maps.Copy(params, tc.values)
			resource, err := Default().Render("docker-host-vm", params)
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(resource); err != nil {
				t.Fatalf("native VM schema rejected starter: %v", err)
			}
			if resource.Spec["cpu"].(map[string]any)["cores"] != tc.cores || resource.Spec["memory"].(map[string]any)["size"] != tc.memory || resource.Spec["disks"].([]any)[0].(map[string]any)["size"] != tc.disk {
				t.Fatal("sizing was not preserved")
			}
		})
	}
}

func TestDockerHostVMRejectsInvalidNumbers(t *testing.T) {
	for _, key := range []string{"templateVMID", "cpuCores", "memoryMiB", "diskGiB"} {
		for _, value := range []any{0, -1, 1.5, math.Inf(1), math.NaN(), float64(1 << 63), int64(math.MaxInt64)} {
			t.Run(key, func(t *testing.T) {
				params := dockerHostParams()
				params[key] = value
				if _, err := Default().Render("docker-host-vm", params); err == nil {
					t.Fatalf("accepted %s = %v", key, value)
				}
			})
		}
	}
	for key, value := range map[string]any{"memoryMiB": 511, "diskGiB": 7, "templateVMID": 1000000000} {
		params := dockerHostParams()
		params[key] = value
		if _, err := Default().Render("docker-host-vm", params); err == nil {
			t.Errorf("accepted %s = %v", key, value)
		}
	}
}

func TestComposeStarterGreetingRemainsData(t *testing.T) {
	greeting := "hello: 'world'\nservices:\n  hostile: {}\n${UNSET:-fallback} $(touch /not-executed) `id` \\ \" %s"
	params := composeStarterParams()
	params["welcomeText"] = greeting
	resource, err := Default().Render("compose-application", params)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
			Command     []string          `yaml:"command"`
			Ports       []string          `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(resource.Spec["compose"].(string)), &document); err != nil {
		t.Fatal(err)
	}
	service, exists := document.Services["welcome"]
	if !exists || len(document.Services) != 1 {
		t.Fatal("greeting changed the Compose service structure")
	}
	if service.Environment["WELCOME_TEXT"] != strings.ReplaceAll(greeting, "$", "$$") {
		t.Fatal("greeting lost literal content or Compose interpolation escaping")
	}
	wantCommand := []string{"sh", "-ec", `printf '%s\n' "$$WELCOME_TEXT" > /www/index.html; exec httpd -f -p 8080 -h /www`}
	if !reflect.DeepEqual(service.Command, wantCommand) {
		t.Fatal("greeting changed the shell program")
	}
	if !reflect.DeepEqual(service.Ports, []string{"127.0.0.1:18080:8080"}) {
		t.Fatalf("unexpected public binding: %v", service.Ports)
	}
	if _, exists := resource.Spec["environment"]; exists {
		t.Fatal("starter must not require a dotenv secret")
	}
	if _, exists := resource.Spec["publicEndpoint"]; exists {
		t.Fatal("starter must not declare public exposure")
	}
}

func TestComposeStarterPortBoundaries(t *testing.T) {
	for _, port := range []any{1, 65535.0} {
		params := composeStarterParams()
		params["port"] = port
		if _, err := Default().Render("compose-application", params); err != nil {
			t.Fatalf("valid boundary %v: %v", port, err)
		}
	}
	for _, port := range []any{0, 65536, -1, 80.5, math.NaN(), math.Inf(1), int64(math.MaxInt64)} {
		params := composeStarterParams()
		params["port"] = port
		if _, err := Default().Render("compose-application", params); err == nil {
			t.Errorf("accepted invalid port %v", port)
		}
	}
}

func TestFirstOperatorStartersRequireNonemptyInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		keys   []string
	}{
		{"docker-host-vm", dockerHostParams(), []string{"name", "context", "node", "sshKey"}},
		{"compose-application", composeStarterParams(), []string{"name", "vmName", "privateKeyFile", "knownHostsFile"}},
	} {
		for _, key := range tc.keys {
			params := maps.Clone(tc.params)
			params[key] = " \n"
			if _, err := Default().Render(tc.name, params); err == nil {
				t.Errorf("%s accepted empty %s", tc.name, key)
			}
		}
	}
}

func TestComposeStarterRejectsInvalidEnvironmentAndPlacement(t *testing.T) {
	for key, value := range map[string]any{
		"welcomeText": "cannot\x00be-an-environment-value",
		"name":        "../outside",
		"vmName":      "../other-vm",
	} {
		params := composeStarterParams()
		params[key] = value
		if _, err := Default().Render("compose-application", params); err == nil {
			t.Errorf("accepted invalid %s", key)
		}
	}
}
