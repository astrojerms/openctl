// Package bootstrap installs the openctl-k3s-agent on a node over SSH. It
// detects the node's architecture and init system, picks the matching agent
// binary, uploads it along with the cert bundle and the appropriate unit file,
// then enables and starts the service.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	"github.com/openctl/openctl/pkg/k3s/ssh"
)

const (
	binaryRemotePath  = "/usr/local/bin/openctl-k3s-agent"
	configDir         = "/etc/openctl-k3s-agent"
	caRemotePath      = configDir + "/ca.pem"
	serverCertRemote  = configDir + "/server.pem"
	serverKeyRemote   = configDir + "/server.key"
	systemdUnitPath   = "/etc/systemd/system/openctl-k3s-agent.service"
	openrcInitPath    = "/etc/init.d/openctl-k3s-agent"
	envAgentBinaryDir = "OPENCTL_K3S_AGENT_DIR"
	defaultPort       = 9443
)

// Init system kind reported by Detect.
type Init string

const (
	InitSystemd Init = "systemd"
	InitOpenRC  Init = "openrc"
)

// HostInfo is what Detect returns about a target node.
type HostInfo struct {
	Arch string // "amd64" | "arm64" | "armv7"
	Init Init
}

// Installer orchestrates per-node agent install. The zero value is valid and
// resolves agent binaries from BinaryDir / OPENCTL_K3S_AGENT_DIR / next to the
// running plugin executable, in that order.
type Installer struct {
	BinaryDir string // override; if empty, uses env var or plugin dir
}

// Install does the full per-node install on an already-connected SSH client.
// Returns the detected host info so the caller can record it.
func (i *Installer) Install(client *ssh.Client, server certs.ServerCert, caPEM []byte) (*HostInfo, error) {
	host, err := Detect(client)
	if err != nil {
		return nil, fmt.Errorf("detect host: %w", err)
	}

	binPath, err := i.resolveBinary(host.Arch)
	if err != nil {
		return nil, err
	}
	binaryData, err := os.ReadFile(binPath) // #nosec G304 -- binPath is resolved via Installer's allowlisted dirs
	if err != nil {
		return nil, fmt.Errorf("read agent binary %s: %w", binPath, err)
	}

	if _, err := client.RunSudo("mkdir -p " + configDir); err != nil {
		return nil, fmt.Errorf("create %s: %w", configDir, err)
	}

	if err := client.UploadSudo(binaryData, binaryRemotePath, 0o755); err != nil {
		return nil, fmt.Errorf("upload binary: %w", err)
	}
	if err := client.UploadSudo(caPEM, caRemotePath, 0o644); err != nil {
		return nil, fmt.Errorf("upload CA: %w", err)
	}
	if err := client.UploadSudo(server.CertPEM, serverCertRemote, 0o644); err != nil {
		return nil, fmt.Errorf("upload server cert: %w", err)
	}
	if err := client.UploadSudo(server.KeyPEM, serverKeyRemote, 0o600); err != nil {
		return nil, fmt.Errorf("upload server key: %w", err)
	}

	switch host.Init {
	case InitSystemd:
		if err := installSystemd(client); err != nil {
			return nil, err
		}
	case InitOpenRC:
		if err := installOpenRC(client); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported init system %q", host.Init)
	}

	return host, nil
}

// Detect probes the remote host for its CPU arch and init system.
func Detect(client *ssh.Client) (*HostInfo, error) {
	rawArch, err := client.Run("uname -m")
	if err != nil {
		return nil, fmt.Errorf("uname -m: %w", err)
	}
	arch, err := normalizeArch(strings.TrimSpace(rawArch))
	if err != nil {
		return nil, err
	}

	init, err := detectInit(client)
	if err != nil {
		return nil, err
	}

	return &HostInfo{Arch: arch, Init: init}, nil
}

func detectInit(client *ssh.Client) (Init, error) {
	// systemd marker. Returning "yes" only when the directory exists keeps
	// this robust against shells that print nothing on test failure.
	if out, _ := client.Run("test -d /run/systemd/system && echo yes || true"); strings.TrimSpace(out) == "yes" {
		return InitSystemd, nil
	}
	if out, _ := client.Run("command -v rc-service > /dev/null && echo yes || true"); strings.TrimSpace(out) == "yes" {
		return InitOpenRC, nil
	}
	return "", fmt.Errorf("no supported init system detected (need systemd or OpenRC)")
}

func normalizeArch(uname string) (string, error) {
	switch uname {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	case "armv7l", "armv7":
		return "armv7", nil
	default:
		return "", fmt.Errorf("unsupported architecture %q (want x86_64, aarch64, or armv7l)", uname)
	}
}

func (i *Installer) resolveBinary(arch string) (string, error) {
	name := "openctl-k3s-agent-linux-" + arch
	dirs := i.candidateDirs()
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("agent binary %s not found in any of: %s (set %s to override)",
		name, strings.Join(dirs, ", "), envAgentBinaryDir)
}

func (i *Installer) candidateDirs() []string {
	var dirs []string
	if i.BinaryDir != "" {
		dirs = append(dirs, i.BinaryDir)
	}
	if env := os.Getenv(envAgentBinaryDir); env != "" {
		dirs = append(dirs, env)
	}
	// In production installs, agent binaries live in a sibling subdirectory
	// of the plugin: <plugin-dir>/k3s-agents/. The subdir is required so the
	// `openctl-*-...` filenames don't get auto-registered as openctl plugins.
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "k3s-agents"))
		dirs = append(dirs, filepath.Dir(exe))
	}
	return dirs
}

func installSystemd(client *ssh.Client) error {
	if err := client.UploadSudo(SystemdUnit, systemdUnitPath, 0o644); err != nil {
		return fmt.Errorf("upload systemd unit: %w", err)
	}
	if _, err := client.RunSudo("systemctl daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if _, err := client.RunSudo("systemctl enable --now openctl-k3s-agent.service"); err != nil {
		return fmt.Errorf("enable+start: %w", err)
	}
	return nil
}

func installOpenRC(client *ssh.Client) error {
	if err := client.UploadSudo(OpenRCInit, openrcInitPath, 0o755); err != nil {
		return fmt.Errorf("upload openrc init: %w", err)
	}
	if _, err := client.RunSudo("rc-update add openctl-k3s-agent default"); err != nil {
		return fmt.Errorf("rc-update: %w", err)
	}
	if _, err := client.RunSudo("rc-service openctl-k3s-agent start"); err != nil {
		return fmt.Errorf("rc-service start: %w", err)
	}
	return nil
}

// Port is the well-known port the agent listens on. Lives here so callers
// (cluster create, agent client) all agree.
const Port = defaultPort
