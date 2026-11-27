package apps

import "context"

// A tiny Docker volume is an ownership sentinel, not application data. Docker
// atomically creates it with immutable owner labels, preventing two resources
// with different directories/users from taking over the same Compose project.
// It is intentionally retained on Delete, just like application volumes.
func projectMarker(l locator) string { return "openctl-owner-" + l.Project }
func projectOwner(l locator) string {
	return "$(docker volume inspect --format " + quote(`{{index .Labels "io.openctl.owner"}}`) + " " + quote(projectMarker(l)) + ")"
}
func projectContainers(l locator) string {
	return "docker ps -aq --filter " + quote("label=com.docker.compose.project="+l.Project)
}
func projectGuard(l locator, identity string) string {
	return "docker info >/dev/null; ids=$(" + projectContainers(l) + "); if docker volume inspect " + quote(projectMarker(l)) + " >/dev/null 2>&1; then test \"" + projectOwner(l) + "\" = " + quote(identity) + "; else test -z \"$ids\"; fi; for id in $ids; do test \"$(docker inspect --format " + quote(`{{index .Config.Labels "com.docker.compose.project.working_dir"}}`) + " \"$id\")\" = " + quote(l.Directory) + "; done"
}
func (p *Provider) claimProject(ctx context.Context, l locator, identity string, allowMissingDocker bool) error {
	if err := p.requireIdentity(); err != nil {
		return err
	}
	command := projectGuard(l, identity) + "; docker volume create --label " + quote("io.openctl.owner="+identity) + " --name " + quote(projectMarker(l)) + " >/dev/null; test \"" + projectOwner(l) + "\" = " + quote(identity)
	if allowMissingDocker {
		command = "if command -v docker >/dev/null; then " + command + "; fi"
	}
	_, err := p.run(ctx, l, "claim Compose project (unmanaged containers or another application owner must not be replaced)", command, nil)
	return err
}
