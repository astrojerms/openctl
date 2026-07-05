package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"text/template"
)

// launchdManager installs the controller as a per-user macOS LaunchAgent.
// RunAtLoad + KeepAlive give "start now and restart if it dies" semantics.
type launchdManager struct{}

func (launchdManager) name() string { return "launchd" }

// plistTemplate is the LaunchAgent plist for the controller. Logs go to
// ~/Library/Logs/openctl/ so they show up in Console.app under "User Reports".
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

func (launchdManager) render(p *installPaths) ([]byte, error) {
	var buf bytes.Buffer
	if err := plistTemplate.Execute(&buf, struct {
		Label      string
		BinaryPath string
		LogOut     string
		LogErr     string
		HomeDir    string
	}{
		Label:      serviceLabel,
		BinaryPath: p.BinaryPath,
		LogOut:     p.LogOut,
		LogErr:     p.LogErr,
		HomeDir:    p.HomeDir,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (launchdManager) reload(p *installPaths) error {
	// Best-effort unload first — install over an existing install should
	// replace cleanly. Errors are ignored because the plist may not be loaded.
	_ = exec.Command("launchctl", "unload", p.UnitPath).Run()                                         // #nosec G204 -- UnitPath is derived from $HOME
	if out, err := exec.Command("launchctl", "load", "-w", p.UnitPath).CombinedOutput(); err != nil { // #nosec G204 -- UnitPath is derived from $HOME
		return fmt.Errorf("launchctl load: %w (output: %s)", err, bytes.TrimSpace(out))
	}
	return nil
}

func (launchdManager) stop(p *installPaths) error {
	return exec.Command("launchctl", "unload", p.UnitPath).Run() // #nosec G204 -- UnitPath is derived from $HOME
}

// renderPlist is retained as a thin wrapper for tests and any direct callers.
func renderPlist(p *installPaths) ([]byte, error) { return launchdManager{}.render(p) }
