package agent

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type Info struct {
	Hostname     string            `json:"hostname"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	Kernel       string            `json:"kernel"`
	Distro       string            `json:"distro"`
	Init         string            `json:"init"`
	K3sVersion   string            `json:"k3sVersion"`
	K3sStatus    string            `json:"k3sStatus"`
	AgentVersion string            `json:"agentVersion"`
	Capabilities map[string]string `json:"capabilities"`
}

func gatherInfo() Info {
	hostname, _ := os.Hostname()
	init := detectInit()
	return Info{
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Kernel:       kernel(),
		Distro:       distro(),
		Init:         init,
		K3sVersion:   k3sVersion(),
		K3sStatus:    k3sStatus(init),
		AgentVersion: Version,
		Capabilities: capabilities(init),
	}
}

func detectInit() string {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	if _, err := os.Stat("/run/openrc"); err == nil {
		return "openrc"
	}
	if _, err := exec.LookPath("rc-service"); err == nil {
		return "openrc"
	}
	return "unknown"
}

func kernel() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func distro() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	var name, version string
	for line := range strings.SplitSeq(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "NAME="):
			name = strings.Trim(strings.TrimPrefix(line, "NAME="), `"`)
		case strings.HasPrefix(line, "VERSION_ID="):
			version = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"`)
		}
	}
	if name == "" {
		return ""
	}
	if version != "" {
		return name + " " + version
	}
	return name
}

func k3sVersion() string {
	out, err := exec.Command("k3s", "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	parts := strings.Fields(line)
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

func k3sStatus(init string) string {
	switch init {
	case "systemd":
		unit := k3sUnitSystemd()
		if unit == "" {
			return "not-installed"
		}
		out, err := exec.Command("systemctl", "is-active", unit).Output() // #nosec G204,G702 -- unit is from k3sUnitSystemd's fixed allowlist
		s := strings.TrimSpace(string(out))
		if s == "" {
			if err != nil {
				return "inactive"
			}
			return "unknown"
		}
		return s
	case "openrc":
		unit := k3sUnitOpenRC()
		if unit == "" {
			return "not-installed"
		}
		out, err := exec.Command("rc-service", unit, "status").CombinedOutput() // #nosec G204,G702 -- unit is from k3sUnitOpenRC's fixed allowlist
		s := string(out)
		switch {
		case strings.Contains(s, "started"):
			return "active"
		case strings.Contains(s, "stopped"):
			return "inactive"
		case err != nil:
			return "unknown"
		default:
			return "unknown"
		}
	default:
		return "unknown"
	}
}

func capabilities(init string) map[string]string {
	caps := map[string]string{}
	switch init {
	case "systemd":
		caps["logs"] = "journald"
		caps["service"] = "systemd"
		caps["upgrade"] = "binary-swap"
	case "openrc":
		caps["logs"] = "file"
		caps["service"] = "openrc"
		caps["upgrade"] = "binary-swap"
	default:
		caps["logs"] = "none"
		caps["service"] = "none"
	}
	return caps
}
