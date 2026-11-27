package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The helper stands in for Compose's two config passes. The actual SSH command
// scripts, private uploads, file publication and cleanup execute on a temp FS.
func TestComposeDockerHelper(t *testing.T) {
	if os.Getenv("OPENCTL_COMPOSE_HELPER") != "1" {
		return
	}
	fail := func() { os.Exit(97) }
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fail()
	}
	args = args[1:]
	var directory, envFile, configFile string
	for len(args) >= 2 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--project-name":
		case "--project-directory":
			directory = args[1]
		case "--env-file":
			envFile = args[1]
		case "-f":
			configFile = args[1]
		default:
			fail()
		}
		args = args[2:]
	}
	if len(args) == 0 || directory == "" {
		fail()
	}
	trace, err := os.OpenFile(os.Getenv("OPENCTL_COMPOSE_TRACE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fail()
	}
	defer trace.Close()
	command := args[0]
	if command != "config" {
		_, _ = fmt.Fprintln(trace, command)
		if command == "up" && os.Getenv("OPENCTL_COMPOSE_FAIL_UP") == "1" {
			fail()
		}
		os.Exit(0)
	}
	for _, file := range []string{envFile, configFile} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0600 || filepath.Dir(file) != directory {
			fail()
		}
	}
	config, err := os.ReadFile(configFile)
	if err != nil {
		fail()
	}
	if strings.Contains(strings.Join(args, " "), "--no-env-resolution") {
		_, _ = fmt.Fprintln(trace, "normalize")
		if strings.Contains(string(config), "INVALID_COMPOSE") {
			fail()
		}
		model, err := os.ReadFile(os.Getenv("OPENCTL_COMPOSE_MODEL"))
		if err != nil {
			fail()
		}
		_, _ = os.Stdout.Write(model)
		os.Exit(0)
	}
	_, _ = fmt.Fprintln(trace, "validate")
	// Like real Compose, disabling interpolation also skips env-file loading.
	if strings.Contains(strings.Join(args, " "), "--no-interpolate") {
		os.Exit(0)
	}
	var model struct {
		Services map[string]struct {
			EnvFile []struct {
				Path string `json:"path"`
			} `json:"env_file"`
		} `json:"services"`
	}
	if json.Unmarshal(config, &model) != nil {
		fail()
	}
	for _, service := range model.Services {
		for _, entry := range service.EnvFile {
			// The common env_file: .env must resolve to the candidate, never
			// the installed .env (which may not even exist on first Apply).
			if entry.Path == filepath.Join(directory, ".env") {
				fail()
			}
			content, err := os.ReadFile(entry.Path)
			if err != nil || strings.Contains(string(content), "INVALID_ENV") {
				fail()
			}
		}
	}
	os.Exit(0)
}

func composeFixture(t *testing.T) (*Provider, locator, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, "application")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\n[ \"$1\" = compose ] || exit 97\nshift\nexec " + quote(executable) + " '-test.run=^TestComposeDockerHelper$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	model, err := json.Marshal(map[string]any{"services": map[string]any{"web": map[string]any{
		"image": "example.invalid/web:test", "env_file": []any{map[string]any{"path": filepath.Join(directory, ".env"), "required": true}},
		"volumes": []any{map[string]any{"type": "bind", "source": filepath.Join(directory, "media"), "target": "/media"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	modelFile := filepath.Join(dir, "normalized.json")
	if err := os.WriteFile(modelFile, model, 0600); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "trace")
	p, _ := testProvider()
	p.runner = localOwnershipRunner{env: append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
		"OPENCTL_COMPOSE_HELPER=1", "OPENCTL_COMPOSE_MODEL="+modelFile, "OPENCTL_COMPOSE_TRACE="+trace)}
	return p, locator{Directory: directory, Project: "openctl-media"}, trace
}

func TestComposeInvalidUpdatePreservesInstalledFiles(t *testing.T) {
	for _, invalid := range []string{"compose", "environment"} {
		t.Run(invalid, func(t *testing.T) {
			p, l, trace := composeFixture(t)
			before := map[string]string{"compose.yaml": "original valid compose\n", ".env": "ORIGINAL_SECRET=retained\n"}
			for file, content := range before {
				if err := os.WriteFile(filepath.Join(l.Directory, file), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s := spec{Compose: "services:\n  web:\n    image: proposed\n    env_file: .env\n", Environment: "PROPOSED_SECRET=not-for-logs\n"}
			if invalid == "compose" {
				s.Compose += "INVALID_COMPOSE\n"
			} else {
				s.Environment += "INVALID_ENV\n"
			}
			err := p.applyCompose(context.Background(), l, s)
			if err == nil || strings.Contains(err.Error(), "not-for-logs") || strings.Contains(err.Error(), "retained") {
				t.Fatalf("invalid candidate must fail without secret diagnostic: %v", err)
			}
			for file, content := range before {
				actual, err := os.ReadFile(filepath.Join(l.Directory, file))
				if err != nil || string(actual) != content {
					t.Fatalf("invalid update replaced installed %s: %v", file, err)
				}
			}
			steps, err := os.ReadFile(trace)
			if err != nil || strings.Contains(string(steps), "pull") || strings.Contains(string(steps), "up") {
				t.Fatalf("invalid candidate reached rollout: %s (%v)", steps, err)
			}
			if invalid == "environment" && string(steps) != "normalize\nvalidate\n" {
				t.Fatalf("candidate service .env was not fully validated: %s", steps)
			}
			assertNoComposeStages(t, l)
		})
	}
}

func TestComposeFirstApplyValidatesManagedEnvAndPublishesOriginalIntent(t *testing.T) {
	p, l, trace := composeFixture(t)
	s := spec{Compose: "services:\n  web:\n    image: proposed\n    env_file: .env\n    volumes: [./media:/media]\n", Environment: "SECRET=candidate-only\n"}
	if err := p.applyCompose(context.Background(), l, s); err != nil {
		t.Fatal(err)
	}
	for file, content := range map[string]string{"compose.yaml": s.Compose, ".env": s.Environment} {
		actual, err := os.ReadFile(filepath.Join(l.Directory, file))
		if err != nil || string(actual) != content {
			t.Fatalf("published normalized output instead of original %s: %v", file, err)
		}
	}
	steps, err := os.ReadFile(trace)
	if err != nil || string(steps) != "normalize\nvalidate\npull\nup\n" {
		t.Fatalf("unexpected Compose lifecycle: %s (%v)", steps, err)
	}
	assertNoComposeStages(t, l)
}

func TestComposePartialRolloutDoesNotClaimRollback(t *testing.T) {
	p, l, _ := composeFixture(t)
	runner := p.runner.(localOwnershipRunner)
	runner.env = append(runner.env, "OPENCTL_COMPOSE_FAIL_UP=1")
	p.runner = runner
	s := spec{Compose: "services:\n  web:\n    image: proposed\n    env_file: .env\n", Environment: "SECRET=candidate-only\n"}
	err := p.applyCompose(context.Background(), l, s)
	if err == nil || !strings.Contains(err.Error(), "rollout may be partial") || !strings.Contains(err.Error(), "no container rollback") {
		t.Fatalf("partial rollout diagnostic is misleading: %v", err)
	}
	actual, readErr := os.ReadFile(filepath.Join(l.Directory, "compose.yaml"))
	if readErr != nil || string(actual) != s.Compose {
		t.Fatal("partial rollout unexpectedly restored obsolete installed intent")
	}
	assertNoComposeStages(t, l)
}

func assertNoComposeStages(t *testing.T, l locator) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(l.Directory, ".openctl-stage-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("private staged files were not cleaned: %v (%v)", files, err)
	}
}

func TestComposeEnvironmentRemappingPreservesPlacementAndExternalFiles(t *testing.T) {
	model := map[string]any{
		"name": "openctl-media", "x-extension": map[string]any{"unchanged": true},
		"services": map[string]any{"web": map[string]any{
			"build":       map[string]any{"context": "/apps/media/build"},
			"volumes":     []any{map[string]any{"type": "bind", "source": "/apps/media/data", "target": "/data"}},
			"environment": map[string]any{"SECRET": "must-not-log"},
			"env_file": []any{
				map[string]any{"path": "/apps/media/.env", "required": true, "format": "raw"},
				map[string]any{"path": "/outside/extra.env", "required": false},
				"./.env", "./other.env",
			},
		}},
	}
	input, _ := json.Marshal(model)
	updated, err := stageComposeEnvironment(input, "/apps/media", "/apps/media/.openctl-stage.env")
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(updated, &actual); err != nil {
		t.Fatal(err)
	}
	files := model["services"].(map[string]any)["web"].(map[string]any)["env_file"].([]any)
	files[0].(map[string]any)["path"] = "/apps/media/.openctl-stage.env"
	files[2] = "/apps/media/.openctl-stage.env"
	if !reflect.DeepEqual(actual, model) {
		t.Fatal("normalization changed placement, external env files, or service options")
	}
}

func TestComposeUnknownNormalizationFailsClosedWithoutSecretDiagnostics(t *testing.T) {
	for _, invalid := range []string{
		`null`, `{}`, `{"services":[]}`, `{"services":{"web":null}}`,
		`{"services":{"web":{"env_file":null}}}`, `{"services":{"web":{"env_file":"secret-path"}}}`,
		`{"services":{"web":{"env_file":[{"path":123,"password":"must-not-log"}]}}}`,
	} {
		_, err := stageComposeEnvironment([]byte(invalid), "/apps/media", "/apps/media/.stage.env")
		if err == nil || !strings.Contains(err.Error(), "unsupported Compose normalization") || strings.Contains(err.Error(), "must-not-log") || strings.Contains(err.Error(), "secret-path") {
			t.Fatalf("unsupported model did not fail safely: %v", err)
		}
	}
}

func TestComposeMissingRequiredEnvFilePreservesInstalledFiles(t *testing.T) {
	p, l, _ := composeFixture(t)
	before := map[string]string{"compose.yaml": "original valid compose\n", ".env": "ORIGINAL=retained\n"}
	for file, content := range before {
		if err := os.WriteFile(filepath.Join(l.Directory, file), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	model, _ := json.Marshal(map[string]any{"services": map[string]any{"web": map[string]any{
		"image": "proposed", "env_file": []any{map[string]any{"path": filepath.Join(l.Directory, "missing.env"), "required": true}},
	}}})
	if err := os.WriteFile(filepath.Join(filepath.Dir(l.Directory), "normalized.json"), model, 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.applyCompose(context.Background(), l, spec{Compose: "services:\n  web:\n    image: proposed\n    env_file: missing.env\n", Environment: "PROPOSED=value\n"}); err == nil {
		t.Fatal("missing required env file was accepted")
	}
	for file, content := range before {
		actual, err := os.ReadFile(filepath.Join(l.Directory, file))
		if err != nil || string(actual) != content {
			t.Fatalf("missing required env file replaced installed %s: %v", file, err)
		}
	}
	assertNoComposeStages(t, l)
}

func TestComposeStagedEnvPreservesLiteralDollars(t *testing.T) {
	// Real `config --no-env-resolution --format json` escapes these values
	// for another Compose load. Only the newly substituted path needs escaping.
	model := []byte(`{"services":{"web":{"environment":{"VALUE":"literal$${MISSING:?must-not-expand}"},"env_file":[{"path":"/apps/literal$$directory/.env"}]}}}`)
	updated, err := stageComposeEnvironment(model, "/apps/literal$directory", "/apps/literal$directory/.stage.env")
	if err != nil {
		t.Fatal(err)
	}
	var actual struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
			EnvFile     []struct {
				Path string `json:"path"`
			} `json:"env_file"`
		} `json:"services"`
	}
	if err := json.Unmarshal(updated, &actual); err != nil {
		t.Fatal(err)
	}
	web := actual.Services["web"]
	if web.EnvFile[0].Path != "/apps/literal$$directory/.stage.env" || web.Environment["VALUE"] != "literal$${MISSING:?must-not-expand}" {
		t.Fatal("second Compose load would expand a literal value or use the installed env file")
	}
}
