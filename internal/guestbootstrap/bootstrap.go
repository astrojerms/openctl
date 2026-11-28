// Package guestbootstrap ships first-boot setup for dedicated application guests.
package guestbootstrap

import _ "embed"

//go:embed assets/docker-host.sh
var dockerHostScript string

// Cloud-init executes string runcmd entries through sh, not bash. The quoted
// heredoc keeps the embedded Bash program literal until Bash reads it.
var dockerHostCommand = "bash <<'OPENCTL_DOCKER_HOST_BOOTSTRAP'\n" + dockerHostScript + "\nOPENCTL_DOCKER_HOST_BOOTSTRAP"

// DockerHostCommand returns a cloud-init runcmd entry for Ubuntu 24.04 amd64 VMs.
// It must run as guest root; it is not an operator-host installation command.
func DockerHostCommand() string {
	return dockerHostCommand
}
