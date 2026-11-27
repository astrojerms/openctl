package apps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

func composeBase(l locator) string {
	return "docker compose --project-name " + quote(l.Project) + " --project-directory " + quote(l.Directory)
}

func (p *Provider) applyCompose(ctx context.Context, l locator, s spec) (resultErr error) {
	if err := p.checkComposePrerequisites(ctx, l); err != nil {
		return err
	}
	// Sibling staging preserves the original relative include/build/bind base.
	// Compose normalizes paths before we redirect only the managed service .env
	// reference, so validation never reads the installed environment by mistake.
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fmt.Errorf("apps: cannot allocate private Compose staging paths")
	}
	prefix := l.Directory + "/.openctl-stage-" + hex.EncodeToString(token[:])
	candidate, environment, normalized := prefix+".yaml", prefix+".env", prefix+".json"
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := p.run(cleanupCtx, l, "remove private Compose staging files", "rm -f -- "+quote(candidate)+" "+quote(environment)+" "+quote(normalized), nil); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("%w; private staging files may remain in the application directory; remove only .openctl-stage files after inspecting the failed operation", err))
		}
	}()
	if err := p.upload(ctx, l, candidate, []byte(s.Compose)); err != nil {
		return err
	}
	if err := p.upload(ctx, l, environment, []byte(s.Environment)); err != nil {
		return err
	}
	proposed := composeBase(l) + " --env-file " + quote(environment) + " -f " + quote(candidate)
	model, err := p.run(ctx, l, "normalize proposed Compose configuration without reading service env files", proposed+" config --no-env-resolution --format json", nil)
	if err != nil {
		return err
	}
	validationModel, err := stageComposeEnvironment([]byte(model), l.Directory, environment)
	if err != nil {
		return err
	}
	if err := p.upload(ctx, l, normalized, validationModel); err != nil {
		return err
	}
	// --no-interpolate also skips service env-file resolution. Compose's
	// normalized JSON already escapes literal dollars; preserve that encoding
	// so the full validation pass can safely load required service env files.
	validation := composeBase(l) + " --env-file " + quote(environment) + " -f " + quote(normalized) + " config --quiet"
	if _, err := p.run(ctx, l, "validate proposed Compose configuration and service env files before installing files", validation, nil); err != nil {
		return err
	}
	// Publish the original declaration, never the normalized secret-expanded
	// validation model. This is not a multi-file transaction or container rollback.
	publish := "test ! -L " + quote(l.Directory+"/compose.yaml") + "; test ! -L " + quote(l.Directory+"/.env") + "; " +
		"mv -f -- " + quote(candidate) + " " + quote(l.Directory+"/compose.yaml") + "; " +
		"mv -f -- " + quote(environment) + " " + quote(l.Directory+"/.env")
	if _, err := p.run(ctx, l, "install validated Compose declaration and environment", publish, nil); err != nil {
		return fmt.Errorf("%w; file publication may be partial; inspect the application directory before retrying", err)
	}
	if _, err := p.run(ctx, l, "pull application images respecting build-only services", compose(l)+" pull --ignore-buildable --policy always", nil); err != nil {
		return fmt.Errorf("%w; validated configuration is installed; inspect the original guest before retrying (no container rollback was performed)", err)
	}
	if _, err := p.run(ctx, l, "roll out Compose application", compose(l)+" up -d --build --remove-orphans", nil); err != nil {
		return fmt.Errorf("%w; rollout may be partial; inspect containers on the original guest before retrying (no container rollback was performed)", err)
	}
	return nil
}

// stageComposeEnvironment edits the Compose-normalized JSON model, not raw YAML.
// All other normalized paths and extension fields remain untouched. Diagnostics
// intentionally exclude the model and decode errors, which may contain secrets.
func stageComposeEnvironment(model []byte, directory, stagedEnvironment string) ([]byte, error) {
	unsupported := func() ([]byte, error) {
		return nil, fmt.Errorf("apps: unsupported Compose normalization shape; require Compose JSON services with string or path-object env_file entries; installed files were not changed")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(model, &root) != nil || root == nil {
		return unsupported()
	}
	var services map[string]map[string]json.RawMessage
	if json.Unmarshal(root["services"], &services) != nil || len(services) == 0 {
		return unsupported()
	}
	replacement, _ := json.Marshal(strings.ReplaceAll(stagedEnvironment, "$", "$$"))
	for _, service := range services {
		if service == nil {
			return unsupported()
		}
		raw, exists := service["env_file"]
		if !exists {
			continue
		}
		var files []json.RawMessage
		if json.Unmarshal(raw, &files) != nil || files == nil {
			return unsupported()
		}
		for i, file := range files {
			var filename string
			var entry map[string]json.RawMessage
			isString := json.Unmarshal(file, &filename) == nil
			if !isString {
				if json.Unmarshal(file, &entry) != nil || entry == nil || json.Unmarshal(entry["path"], &filename) != nil {
					return unsupported()
				}
			}
			if filename == "" {
				return unsupported()
			}
			// config output escapes dollars for reloading as a Compose file.
			resolved := strings.ReplaceAll(filename, "$$", "$")
			if !path.IsAbs(resolved) {
				resolved = path.Join(directory, resolved)
			}
			if path.Clean(resolved) != path.Join(directory, ".env") {
				continue
			}
			if isString {
				files[i] = replacement
			} else {
				entry["path"] = replacement
				files[i], _ = json.Marshal(entry)
			}
		}
		service["env_file"], _ = json.Marshal(files)
	}
	root["services"], _ = json.Marshal(services)
	updated, err := json.Marshal(root)
	if err != nil {
		return unsupported()
	}
	return updated, nil
}
