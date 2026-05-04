package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"
	"time"
)

// Install/uninstall constants. Kept together so the plist label, paths, and
// log destinations stay in sync between the two flows.
const (
	launchdLabel = "io.openctl.controller"
	plistName    = "io.openctl.controller.plist"
)

// installPaths is the set of derived paths the install/uninstall flows
// touch. Computed once from $HOME so the install and uninstall agree on
// where things live.
type installPaths struct {
	HomeDir       string
	BinaryDir     string // ~/Library/Application Support/openctl/bin
	BinaryPath    string // .../openctl-controller
	PlistDir      string // ~/Library/LaunchAgents
	PlistPath     string // .../io.openctl.controller.plist
	LogDir        string // ~/Library/Logs/openctl
	LogOut        string // .../controller.out.log
	LogErr        string // .../controller.err.log
	StateDir      string // ~/.openctl/controller
	CLIConfigDir  string // ~/.openctl
	CLIConfigFile string // ~/.openctl/config.yaml
}

func resolveInstallPaths() (*installPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve $HOME: %w", err)
	}
	binaryDir := filepath.Join(home, "Library", "Application Support", "openctl", "bin")
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	logDir := filepath.Join(home, "Library", "Logs", "openctl")
	return &installPaths{
		HomeDir:       home,
		BinaryDir:     binaryDir,
		BinaryPath:    filepath.Join(binaryDir, "openctl-controller"),
		PlistDir:      plistDir,
		PlistPath:     filepath.Join(plistDir, plistName),
		LogDir:        logDir,
		LogOut:        filepath.Join(logDir, "controller.out.log"),
		LogErr:        filepath.Join(logDir, "controller.err.log"),
		StateDir:      filepath.Join(home, ".openctl", "controller"),
		CLIConfigDir:  filepath.Join(home, ".openctl"),
		CLIConfigFile: filepath.Join(home, ".openctl", "config.yaml"),
	}, nil
}

// runInstall handles `openctl-controller install --local`. It copies the
// current binary into the user-local Application Support tree, writes a
// LaunchAgent plist that runs `... serve`, loads it, waits for the
// controller to come up, and ensures the CLI config has a controller
// section.
//
// Local-Mac install only — Linux/remote variants are tracked as followups
// in CONTROLLER.md.
func runInstall(args []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("install --local is macOS-only (LaunchAgents); platform=%s", runtime.GOOS)
	}
	// Single supported flag for now: `--local`. Parse it manually to keep
	// the surface honest (we don't yet support --target ssh:// etc.).
	var local bool
	for _, a := range args {
		switch a {
		case "--local":
			local = true
		case "-h", "--help":
			fmt.Println(installUsage)
			return nil
		default:
			return fmt.Errorf("unknown install flag %q", a)
		}
	}
	if !local {
		return fmt.Errorf("--local is required (other targets are followup work)")
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

	for _, dir := range []string{paths.BinaryDir, paths.PlistDir, paths.LogDir, paths.StateDir, paths.CLIConfigDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	// LaunchAgents requires 0755 on the dir per Apple's recent enforcement,
	// but ~/Library is the user's. The MkdirAll above creates it 0700; that
	// works for LaunchAgents because the dir is owned by the user.

	if err := copyFile(src, paths.BinaryPath, 0o755); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}

	plist, err := renderPlist(paths)
	if err != nil {
		return fmt.Errorf("render plist: %w", err)
	}
	// Plist mode 0o600 is fine — LaunchAgents reads it as the user who
	// owns it (us). 0o644 would also work but tighter is fine here.
	if err := os.WriteFile(paths.PlistPath, plist, 0o600); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Best-effort unload first — install over an existing install should
	// replace cleanly. Errors are ignored because the plist may not be
	// loaded.
	_ = exec.Command("launchctl", "unload", paths.PlistPath).Run() // #nosec G204 -- paths.PlistPath is derived from $HOME

	if out, err := exec.Command("launchctl", "load", "-w", paths.PlistPath).CombinedOutput(); err != nil { // #nosec G204 -- paths.PlistPath is derived from $HOME
		return fmt.Errorf("launchctl load: %w (output: %s)", err, bytes.TrimSpace(out))
	}

	// Verify the controller is up by dialing the default listen address.
	// If the user customized --listen via flags in the plist, this misses,
	// but the default install never does.
	if err := waitForListener("127.0.0.1:9444", 10*time.Second); err != nil {
		return fmt.Errorf("controller did not come up within 10s: %w (check %s)", err, paths.LogErr)
	}

	if err := ensureCLIConfig(paths.CLIConfigFile); err != nil {
		return fmt.Errorf("write CLI config: %w", err)
	}

	fmt.Println("openctl-controller installed and running.")
	fmt.Printf("  binary:   %s\n", paths.BinaryPath)
	fmt.Printf("  plist:    %s\n", paths.PlistPath)
	fmt.Printf("  logs:     %s\n", paths.LogOut)
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

// runUninstall handles `openctl-controller uninstall`. Removes the plist
// and binary; optionally with --purge also removes ~/.openctl/controller/.
// The user's CLI config is always left alone — it usually carries provider
// credentials that aren't ours to remove.
func runUninstall(args []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("uninstall is macOS-only; platform=%s", runtime.GOOS)
	}
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

	paths, err := resolveInstallPaths()
	if err != nil {
		return err
	}

	// Best-effort unload — ignore errors if not loaded.
	_ = exec.Command("launchctl", "unload", paths.PlistPath).Run() // #nosec G204 -- paths.PlistPath is derived from $HOME

	if err := removeIfExists(paths.PlistPath); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}
	if err := removeIfExists(paths.BinaryPath); err != nil {
		return fmt.Errorf("remove binary: %w", err)
	}

	fmt.Println("openctl-controller uninstalled.")
	fmt.Printf("  removed: %s\n", paths.PlistPath)
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

// plistTemplate is the LaunchAgent plist for the controller. RunAtLoad +
// KeepAlive together give us "start now and restart if it dies" semantics.
// Logs go to ~/Library/Logs/openctl/ so they show up in Console.app under
// the standard "User Reports" tree.
var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.BinaryPath}}</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>{{.LogOut}}</string>
  <key>StandardErrorPath</key>
  <string>{{.LogErr}}</string>
  <key>WorkingDirectory</key>
  <string>{{.HomeDir}}</string>
</dict>
</plist>
`))

func renderPlist(p *installPaths) ([]byte, error) {
	var buf bytes.Buffer
	if err := plistTemplate.Execute(&buf, struct {
		Label      string
		BinaryPath string
		LogOut     string
		LogErr     string
		HomeDir    string
	}{
		Label:      launchdLabel,
		BinaryPath: p.BinaryPath,
		LogOut:     p.LogOut,
		LogErr:     p.LogErr,
		HomeDir:    p.HomeDir,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

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

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// waitForListener dials addr until either the dial succeeds or budget
// expires. Used to verify launchd actually started the controller — if the
// binary panics or the listen flag is wrong, the wait times out and the
// install reports the issue while pointing at the log file.
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

// ensureCLIConfig makes sure ~/.openctl/config.yaml exists. We don't
// rewrite an existing file — if the user has provider configs there
// already, they're hands-off. We only create a minimal stub when the file
// doesn't exist, so a fresh install has something to point at.
//
// The CLI auto-fills controller defaults (URL, token path, CA path) when
// the controller section is absent or empty, so we don't need to write
// those keys explicitly.
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

const installUsage = `usage: openctl-controller install --local

Installs the controller as a per-user LaunchAgent on macOS:
  - copies this binary to ~/Library/Application Support/openctl/bin/
  - writes ~/Library/LaunchAgents/io.openctl.controller.plist
  - launchctl-loads the plist (RunAtLoad + KeepAlive)
  - verifies the controller responds on 127.0.0.1:9444
  - writes a stub ~/.openctl/config.yaml if none exists

Logs land at ~/Library/Logs/openctl/controller.{out,err}.log.
Run 'openctl-controller uninstall' to remove.`

const uninstallUsage = `usage: openctl-controller uninstall [--purge]

Stops the LaunchAgent, removes the plist and the installed binary.

  --purge   also remove ~/.openctl/controller (TLS material, token,
            state DB, applied manifests). Default leaves state alone so
            re-installing keeps your operation history and credentials.

Your ~/.openctl/config.yaml is always left in place — it usually carries
provider credentials that aren't ours to remove.`
