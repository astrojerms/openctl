package main

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/openctl/openctl/pkg/k3s/ssh"
)

// Remote install layout on the target host. The controller runs as a system
// service (root), with state under /var/lib so it doesn't depend on a home dir.
const (
	remoteBinPath   = "/usr/local/bin/openctl-controller"
	remoteUnitPath  = "/etc/systemd/system/openctl-controller.service"
	remoteStateDir  = "/var/lib/openctl/controller"
	remoteUnitName  = "openctl-controller.service"
	remoteTokenPath = "/var/lib/openctl/controller/token"
)

// sshTarget is a parsed `ssh://user@host[:port]` install target.
type sshTarget struct {
	User string
	Host string
	Port int
}

// parseSSHTarget parses `ssh://user@host[:port]`. Port defaults to 22; user
// defaults to $USER when omitted.
func parseSSHTarget(raw string) (*sshTarget, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse target %q: %w", raw, err)
	}
	if u.Scheme != "ssh" {
		return nil, fmt.Errorf("target must be ssh://user@host[:port] (got scheme %q)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("target %q is missing a host", raw)
	}
	port := 22
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid port %q in target", p)
		}
		port = n
	}
	user := u.User.Username()
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		return nil, fmt.Errorf("target %q is missing a user (ssh://USER@host) and $USER is unset", raw)
	}
	return &sshTarget{User: user, Host: host, Port: port}, nil
}

// resolveSSHKey returns the SSH private key path: the flag value if set,
// otherwise the first of ~/.ssh/id_ed25519, ~/.ssh/id_rsa that exists.
func resolveSSHKey(flagKey string) (string, error) {
	if flagKey != "" {
		return flagKey, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"id_ed25519", "id_rsa"} {
		p := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no SSH key found (tried ~/.ssh/id_ed25519, ~/.ssh/id_rsa); pass --ssh-key PATH")
}

// sshRunner is the subset of *ssh.Client the remote install uses. Narrowing to
// an interface lets the orchestration be unit-tested against a fake without a
// real host.
type sshRunner interface {
	Run(command string) (string, error)
	RunSudo(command string) (string, error)
	UploadSudo(data []byte, remotePath string, mode os.FileMode) error
}

var _ sshRunner = (*ssh.Client)(nil)

// detectRemoteArch maps the remote `uname -m` to a Go arch we cross-build.
func detectRemoteArch(r sshRunner) (string, error) {
	out, err := r.Run("uname -m")
	if err != nil {
		return "", fmt.Errorf("detect remote arch: %w", err)
	}
	switch strings.TrimSpace(out) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported remote arch %q (build-controller-linux ships amd64 + arm64)", strings.TrimSpace(out))
	}
}

// findLinuxControllerBinary locates the cross-built controller for arch. Looks
// next to the running binary and in ./bin, matching how the k3s agents are
// found at local-install time.
func findLinuxControllerBinary(arch string) (string, error) {
	name := controllerBin + "-linux-" + arch
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "bin"), wd)
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found (looked in %v); run `make build-controller-linux`", name, dirs)
}

// systemdSystemUnitTemplate is the system-scope (root) unit written to
// /etc/systemd/system on the remote host. Unlike the user unit, it binds to the
// host network so the controller is reachable from the operator's machine
// (protected by the controller's token auth + TLS).
var systemdSystemUnitTemplate = template.Must(template.New("system-unit").Parse(`[Unit]
Description=openctl controller
Documentation=https://github.com/openctl/openctl
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.ExecStart}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`))

func renderSystemdSystemUnit() ([]byte, error) {
	execStart := fmt.Sprintf("%s serve --dir %s --listen 0.0.0.0:9444 --http-listen 0.0.0.0:9445",
		remoteBinPath, remoteStateDir)
	var buf bytes.Buffer
	if err := systemdSystemUnitTemplate.Execute(&buf, struct{ ExecStart string }{ExecStart: execStart}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// deployController performs the remote install steps over an sshRunner: create
// the state dir, upload the binary + unit, then daemon-reload and enable+start.
// Broken out from runInstallRemote so it's unit-testable against a fake runner.
func deployController(r sshRunner, binary, unit []byte) error {
	if _, err := r.RunSudo("mkdir -p " + remoteStateDir); err != nil {
		return fmt.Errorf("create remote state dir: %w", err)
	}
	if err := r.UploadSudo(binary, remoteBinPath, 0o755); err != nil {
		return fmt.Errorf("upload controller binary: %w", err)
	}
	if err := r.UploadSudo(unit, remoteUnitPath, 0o644); err != nil {
		return fmt.Errorf("upload systemd unit: %w", err)
	}
	if _, err := r.RunSudo("systemctl daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := r.RunSudo("systemctl enable --now " + remoteUnitName); err != nil {
		return fmt.Errorf("systemctl enable --now: %w", err)
	}
	return nil
}

// runInstallRemote deploys the cross-built Linux controller to a remote host.
func runInstallRemote(target, sshKeyFlag string) error {
	t, err := parseSSHTarget(target)
	if err != nil {
		return err
	}
	key, err := resolveSSHKey(sshKeyFlag)
	if err != nil {
		return err
	}

	fmt.Printf("Connecting to %s@%s:%d ...\n", t.User, t.Host, t.Port)
	fmt.Fprintln(os.Stderr, "note: SSH host-key verification is not performed for this install")

	client, err := ssh.NewClient(t.Host, t.Port, t.User, key)
	if err != nil {
		return fmt.Errorf("ssh client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("ssh connect to %s: %w", t.Host, err)
	}
	defer func() { _ = client.Close() }()

	arch, err := detectRemoteArch(client)
	if err != nil {
		return err
	}
	binPath, err := findLinuxControllerBinary(arch)
	if err != nil {
		return err
	}
	binary, err := os.ReadFile(binPath) // #nosec G304 -- binPath resolved from trusted build output dirs
	if err != nil {
		return fmt.Errorf("read controller binary: %w", err)
	}
	unit, err := renderSystemdSystemUnit()
	if err != nil {
		return fmt.Errorf("render systemd unit: %w", err)
	}

	fmt.Printf("Deploying %s (%s) to %s ...\n", filepath.Base(binPath), arch, t.Host)
	if err := deployController(client, binary, unit); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("openctl-controller installed and running on %s (systemd).\n", t.Host)
	fmt.Printf("  binary:  %s\n", remoteBinPath)
	fmt.Printf("  unit:    %s\n", remoteUnitPath)
	fmt.Printf("  state:   %s\n", remoteStateDir)
	fmt.Printf("  listen:  %s:9444 (gRPC), %s:9445 (UI/HTTP)\n", t.Host, t.Host)
	fmt.Println()
	fmt.Println("Logs:  ssh " + t.User + "@" + t.Host + " journalctl -u " + remoteUnitName)
	fmt.Println()
	fmt.Println("To point your CLI at it, copy the remote token + CA:")
	fmt.Printf("  ssh %s@%s sudo cat %s\n", t.User, t.Host, remoteTokenPath)
	fmt.Println("  ...then set controller.url/tokenFile/caFile in ~/.openctl/config.yaml.")
	return nil
}
