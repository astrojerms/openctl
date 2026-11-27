package apps

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// Earlier Compose releases may resolve required service env files even when
// --no-env-resolution is requested, before the staged environment is remapped.
func (p *Provider) checkComposePrerequisites(ctx context.Context, l locator) error {
	version, err := p.run(ctx, l, "inspect Docker Compose prerequisite (5.8.0 or newer)", "docker compose version --short", nil)
	if err != nil {
		return err
	}
	if !supportedComposeVersion(version) {
		return errors.New("apps: Docker Compose 5.8.0 or newer is required on the guest; upgrade the Compose plugin before applying. Older bundled plugins may read installed service env files during staged validation; no proposed files were published")
	}
	return nil
}

func supportedComposeVersion(version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	version, _, _ = strings.Cut(version, "+")
	majorText, rest, ok := strings.Cut(version, ".")
	if !ok {
		return false
	}
	minorText, patchText, ok := strings.Cut(rest, ".")
	if !ok {
		return false
	}
	major, majorErr := strconv.ParseUint(majorText, 10, 32)
	minor, minorErr := strconv.ParseUint(minorText, 10, 32)
	_, patchErr := strconv.ParseUint(patchText, 10, 32)
	return majorErr == nil && minorErr == nil && patchErr == nil && (major > 5 || major == 5 && minor >= 8)
}
