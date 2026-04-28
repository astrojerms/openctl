package agent

import (
	"os"
	"os/exec"
	"strings"
)

// k3sUnitSystemd returns the installed k3s systemd unit name for this node:
// "k3s" on control-plane nodes, "k3s-agent" on workers. Returns "" if
// neither is installed.
func k3sUnitSystemd() string {
	for _, unit := range []string{"k3s", "k3s-agent"} {
		out, _ := exec.Command("systemctl", "show", "-p", "LoadState", unit+".service").Output() // #nosec G204,G702 -- unit is from a fixed allowlist
		if strings.Contains(string(out), "LoadState=loaded") {
			return unit
		}
	}
	return ""
}

// k3sUnitOpenRC returns the installed k3s OpenRC service name. "" if neither.
func k3sUnitOpenRC() string {
	for _, unit := range []string{"k3s", "k3s-agent"} {
		if _, err := os.Stat("/etc/init.d/" + unit); err == nil {
			return unit
		}
	}
	return ""
}
