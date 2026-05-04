package agent

import (
	"fmt"
	"os/exec"
	"strings"
)

// ServiceAction is one of the verbs we support for k3s service control.
type ServiceAction string

const (
	ServiceStart   ServiceAction = "start"
	ServiceStop    ServiceAction = "stop"
	ServiceRestart ServiceAction = "restart"
)

var serviceActions = []ServiceAction{ServiceStart, ServiceStop, ServiceRestart}

// controlK3s runs the requested action against the installed k3s unit ("k3s"
// on CPs, "k3s-agent" on workers) via the detected init system. The agent
// runs as root, so no sudo needed.
func controlK3s(action ServiceAction) error {
	switch detectInit() {
	case "systemd":
		unit := k3sUnitSystemd()
		if unit == "" {
			return fmt.Errorf("no k3s systemd unit installed")
		}
		return runService("systemctl", string(action), unit)
	case "openrc":
		unit := k3sUnitOpenRC()
		if unit == "" {
			return fmt.Errorf("no k3s OpenRC service installed")
		}
		return runService("rc-service", unit, string(action))
	default:
		return fmt.Errorf("service control not supported on this init system")
	}
}

func runService(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput() // #nosec G204,G702 -- name and args are package-controlled
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
