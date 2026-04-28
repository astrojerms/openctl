package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

const (
	defaultLogLines = 100
	maxLogLines     = 5000
)

// fetchK3sLogs returns recent k3s log lines. Picks journald or file-based
// tailing based on init system, and routes to "k3s" or "k3s-agent" depending
// on which unit is installed (CPs vs workers).
func fetchK3sLogs(lines int) (string, error) {
	if lines <= 0 {
		lines = defaultLogLines
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	switch detectInit() {
	case "systemd":
		unit := k3sUnitSystemd()
		if unit == "" {
			return "", fmt.Errorf("no k3s unit installed (looked for k3s.service and k3s-agent.service)")
		}
		return fetchJournald(unit, lines)
	case "openrc":
		return fetchK3sFileLog(lines)
	default:
		return "", fmt.Errorf("logs not supported on this init system")
	}
}

func fetchJournald(unit string, lines int) (string, error) {
	out, err := exec.Command("journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager").CombinedOutput() // #nosec G204,G702 -- unit/lines are package-controlled
	if err != nil {
		return "", fmt.Errorf("journalctl: %w: %s", err, string(out))
	}
	return string(out), nil
}

func fetchK3sFileLog(lines int) (string, error) {
	candidates := []string{
		"/var/log/k3s.log",
		"/var/log/k3s/k3s.log",
		"/var/log/k3s-agent.log",
		"/var/log/k3s-agent/k3s-agent.log",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			out, err := exec.Command("tail", "-n", strconv.Itoa(lines), p).CombinedOutput() // #nosec G204,G702 -- p is from package allowlist
			if err != nil {
				return "", fmt.Errorf("tail %s: %w: %s", p, err, string(out))
			}
			return string(out), nil
		}
	}
	return "", fmt.Errorf("no k3s log file found in %v", candidates)
}
