package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Install/uninstall constants shared across service managers.
const (
	serviceLabel   = "io.openctl.controller" // launchd label
	plistName      = "io.openctl.controller.plist"
	systemdUnit    = "openctl-controller.service" // systemd (user) unit name
	controllerBin  = "openctl-controller"
	defaultProbe   = "127.0.0.1:9444" // listener the install waits for
	defaultProbeTO = 10 * time.Second
)

// installPaths is the set of derived paths the install/uninstall flows touch.
// Computed once from $HOME per platform so install and uninstall agree on
// where things live. Field meanings are platform-generalized:
//   - UnitDir/UnitPath: the service definition (launchd plist / systemd unit)
//   - LogOut/LogErr: file log destinations (launchd only; empty on systemd,
//     which captures stdout/stderr into the journal)
type installPaths struct {
	HomeDir       string
	BinaryDir     string
	BinaryPath    string
	AgentDir      string
	UnitDir       string
	UnitPath      string
	LogDir        string
	LogOut        string
	LogErr        string
	StateDir      string
	CLIConfigDir  string
	CLIConfigFile string
}

// resolveInstallPaths resolves the install layout for the current OS.
func resolveInstallPaths() (*installPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve $HOME: %w", err)
	}
	return resolveInstallPathsFor(runtime.GOOS, home)
}

// resolveInstallPathsFor computes the install layout for a given OS + home.
// Split out from resolveInstallPaths so tests can exercise both platforms
// regardless of the host they run on.
func resolveInstallPathsFor(goos, home string) (*installPaths, error) {
	common := func(p *installPaths) *installPaths {
		p.HomeDir = home
		p.BinaryPath = filepath.Join(p.BinaryDir, controllerBin)
		p.AgentDir = filepath.Join(p.BinaryDir, "k3s-agents")
		p.StateDir = filepath.Join(home, ".openctl", "controller")
		p.CLIConfigDir = filepath.Join(home, ".openctl")
		p.CLIConfigFile = filepath.Join(home, ".openctl", "config.yaml")
		return p
	}

	switch goos {
	case "darwin":
		binaryDir := filepath.Join(home, "Library", "Application Support", "openctl", "bin")
		unitDir := filepath.Join(home, "Library", "LaunchAgents")
		logDir := filepath.Join(home, "Library", "Logs", "openctl")
		p := common(&installPaths{BinaryDir: binaryDir})
		p.UnitDir = unitDir
		p.UnitPath = filepath.Join(unitDir, plistName)
		p.LogDir = logDir
		p.LogOut = filepath.Join(logDir, "controller.out.log")
		p.LogErr = filepath.Join(logDir, "controller.err.log")
		return p, nil
	case "linux":
		// XDG-style user layout. systemd (user scope) captures stdout/stderr
		// into the journal, so no file log paths are needed.
		binaryDir := filepath.Join(home, ".local", "share", "openctl", "bin")
		unitDir := filepath.Join(home, ".config", "systemd", "user")
		p := common(&installPaths{BinaryDir: binaryDir})
		p.UnitDir = unitDir
		p.UnitPath = filepath.Join(unitDir, systemdUnit)
		return p, nil
	default:
		return nil, fmt.Errorf("unsupported platform %q (install supports darwin and linux)", goos)
	}
}

// serviceManager abstracts the per-OS service supervisor: launchd on macOS,
// systemd (user scope) on Linux. Selected by GOOS. The install flow writes the
// unit file to installPaths.UnitPath itself; the manager only (re)starts and
// stops the service.
type serviceManager interface {
	name() string
	// render produces the service definition bytes written to UnitPath.
	render(p *installPaths) ([]byte, error)
	// reload (re)installs and starts the service after the unit is written.
	reload(p *installPaths) error
	// stop stops and unregisters the service (best-effort, for uninstall).
	stop(p *installPaths) error
}

// serviceManagerFor returns the service manager for a GOOS.
func serviceManagerFor(goos string) (serviceManager, error) {
	switch goos {
	case "darwin":
		return launchdManager{}, nil
	case "linux":
		return systemdManager{}, nil
	default:
		return nil, fmt.Errorf("unsupported platform %q (install supports darwin and linux)", goos)
	}
}

// runInstall dispatches `openctl-controller install`:
//   - `--local` installs on this machine as a per-user background service.
//   - `--target ssh://user@host` deploys the cross-built Linux controller to
//     a remote host as a system systemd service (see install_ssh.go).
func runInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we print our own usage on -h
	local := fs.Bool("local", false, "install on this machine")
	target := fs.String("target", "", "remote target, e.g. ssh://user@host[:port]")
	sshKey := fs.String("ssh-key", "", "SSH private key for --target ssh:// (default: ~/.ssh/id_ed25519 or id_rsa)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(installUsage)
			return nil
		}
		return err
	}

	switch {
	case *target != "":
		if *local {
			return fmt.Errorf("--local and --target are mutually exclusive")
		}
		return runInstallRemote(*target, *sshKey)
	case *local:
		return runInstallLocal()
	default:
		return fmt.Errorf("one of --local or --target ssh://user@host is required")
	}
}

// runInstallLocal copies the current binary into a per-user tree, writes a
// service definition (launchd plist on macOS, systemd user unit on Linux),
// starts it, waits for the controller to come up, and ensures the CLI config
// has a controller section.
func runInstallLocal() error {
	mgr, err := serviceManagerFor(runtime.GOOS)
	if err != nil {
		return err
	}
	paths, err := resolveInstallPaths()
	if err != nil {
		return err
	}

	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current binary: %w", err)
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return fmt.Errorf("resolve symlinks for current binary: %w", err)
	}

	for _, dir := range []string{paths.BinaryDir, paths.AgentDir, paths.UnitDir, paths.LogDir, paths.StateDir, paths.CLIConfigDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := copyFile(src, paths.BinaryPath, 0o755); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := copyK3sAgentBinaries(filepath.Dir(src), paths.AgentDir); err != nil {
		return fmt.Errorf("copy k3s agent binaries: %w", err)
	}

	unit, err := mgr.render(paths)
	if err != nil {
		return fmt.Errorf("render %s unit: %w", mgr.name(), err)
	}
	if err := os.WriteFile(paths.UnitPath, unit, 0o600); err != nil {
		return fmt.Errorf("write service unit: %w", err)
	}
	if err := mgr.reload(paths); err != nil {
		return err
	}

	if err := waitForListener(defaultProbe, defaultProbeTO); err != nil {
		logHint := paths.LogErr
		if logHint == "" {
			logHint = "journalctl --user -u " + systemdUnit
		}
		return fmt.Errorf("controller did not come up within %s: %w (check %s)", defaultProbeTO, err, logHint)
	}

	if err := ensureCLIConfig(paths.CLIConfigFile); err != nil {
		return fmt.Errorf("write CLI config: %w", err)
	}

	fmt.Printf("openctl-controller installed and running (%s).\n", mgr.name())
	fmt.Printf("  binary:   %s\n", paths.BinaryPath)
	fmt.Printf("  agents:   %s\n", paths.AgentDir)
	fmt.Printf("  unit:     %s\n", paths.UnitPath)
	if paths.LogOut != "" {
		fmt.Printf("  logs:     %s\n", paths.LogOut)
	} else {
		fmt.Printf("  logs:     journalctl --user -u %s\n", systemdUnit)
	}
	fmt.Printf("  state:    %s\n", paths.StateDir)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  openctl ping")
	fmt.Println("    → ok: echo=\"ping\" server-version=...")
	fmt.Println()
	fmt.Println("To uninstall:")
	fmt.Println("  openctl-controller uninstall [--purge]")
	return nil
}

// runUninstall handles `openctl-controller uninstall`. Stops the service and
// removes the unit + binary; optionally with --purge also removes
// ~/.openctl/controller/. The user's CLI config is always left alone.
func runUninstall(args []string) error {
	var purge bool
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "-h", "--help":
			fmt.Println(uninstallUsage)
			return nil
		default:
			return fmt.Errorf("unknown uninstall flag %q", a)
		}
	}

	mgr, err := serviceManagerFor(runtime.GOOS)
	if err != nil {
		return err
	}
	paths, err := resolveInstallPaths()
	if err != nil {
		return err
	}

	// Best-effort stop — ignore errors if not running.
	_ = mgr.stop(paths)

	if err := removeIfExists(paths.UnitPath); err != nil {
		return fmt.Errorf("remove service unit: %w", err)
	}
	if err := removeIfExists(paths.BinaryPath); err != nil {
		return fmt.Errorf("remove binary: %w", err)
	}

	fmt.Printf("openctl-controller uninstalled (%s).\n", mgr.name())
	fmt.Printf("  removed: %s\n", paths.UnitPath)
	fmt.Printf("  removed: %s\n", paths.BinaryPath)

	if purge {
		if err := os.RemoveAll(paths.StateDir); err != nil {
			return fmt.Errorf("purge state: %w", err)
		}
		fmt.Printf("  purged:  %s\n", paths.StateDir)
		fmt.Println()
		fmt.Println("Note: ~/.openctl/config.yaml was left in place — it usually")
		fmt.Println("carries provider credentials. Edit by hand if you want to")
		fmt.Println("drop the controller section too.")
	} else {
		fmt.Println()
		fmt.Printf("State preserved at %s. Pass --purge to remove it.\n", paths.StateDir)
	}
	return nil
}

// --- shared helpers (platform-independent) ---

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) // #nosec G304 -- src is os.Executable() of this process
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// Write to a temp file and rename so the install is atomic — important
	// when reinstalling over a binary that's currently running.
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) // #nosec G304 -- dst is derived from $HOME
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func copyK3sAgentBinaries(srcDir, dstDir string) error {
	names := []string{
		"openctl-k3s-agent-linux-amd64",
		"openctl-k3s-agent-linux-arm64",
		"openctl-k3s-agent-linux-armv7",
	}
	for _, name := range names {
		src := filepath.Join(srcDir, name)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%s not found next to controller binary; run `make build-plugin-k3s-agent-linux` before install", name)
			}
			return err
		}
		if err := copyFile(src, filepath.Join(dstDir, name), 0o755); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// waitForListener dials addr until either the dial succeeds or budget expires.
// Verifies the supervisor actually started the controller.
func waitForListener(addr string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return lastErr
}

// ensureCLIConfig makes sure ~/.openctl/config.yaml exists, creating a minimal
// stub only when absent (an existing file is left untouched — it usually
// carries provider credentials).
func ensureCLIConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	stub := `# openctl CLI configuration. The controller-side install added this
# file so the CLI knows it can talk to the local controller. Add a
# providers: section below to register proxmox/k3s when you're ready.
defaults:
  output: table
  timeout: 300

controller:
  # url: 127.0.0.1:9444
  # tokenFile: ~/.openctl/controller/token
  # caFile: ~/.openctl/controller/tls/ca.crt
`
	return os.WriteFile(path, []byte(stub), 0o600)
}

const installUsage = `usage: openctl-controller install (--local | --target ssh://user@host[:port])

--local: installs the controller as a per-user background service on this
machine:
  macOS  — a LaunchAgent (~/Library/LaunchAgents/io.openctl.controller.plist)
  Linux  — a systemd user unit (~/.config/systemd/user/openctl-controller.service)
It copies this binary + the k3s agent binaries into a per-user tree, starts
the service (start-now + restart-on-death), verifies the controller responds
on 127.0.0.1:9444, and writes a stub ~/.openctl/config.yaml if none exists.
Logs: macOS → ~/Library/Logs/openctl/controller.{out,err}.log;
      Linux → journalctl --user -u openctl-controller.service.

--target ssh://user@host[:port]: deploys the cross-built Linux controller to a
remote host as a system systemd service. Requires 'make build-controller-linux'
first (produces bin/openctl-controller-linux-<arch>). Flags:
  --ssh-key PATH   SSH private key (default: ~/.ssh/id_ed25519, then id_rsa)
The remote user needs passwordless sudo. The service listens on the host's
network (protected by the controller's token auth + TLS); copy the remote
token (/var/lib/openctl/controller/token) and CA to point your CLI at it.

Run 'openctl-controller uninstall' to remove a local install.`

const uninstallUsage = `usage: openctl-controller uninstall [--purge]

Stops the service, removes the unit definition and the installed binary.

  --purge   also remove ~/.openctl/controller (TLS material, token,
            state DB, applied manifests). Default leaves state alone so
            re-installing keeps your operation history and credentials.

Your ~/.openctl/config.yaml is always left in place — it usually carries
provider credentials that aren't ours to remove.`
