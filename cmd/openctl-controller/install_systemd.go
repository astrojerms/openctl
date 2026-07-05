package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"text/template"
)

// systemdManager installs the controller as a per-user systemd service
// (systemctl --user). Restart=on-failure gives restart-on-death; the journal
// captures stdout/stderr (view with `journalctl --user -u openctl-controller`).
//
// User services survive logout only when lingering is enabled for the account
// (`loginctl enable-linger <user>`); the install prints that hint on Linux.
type systemdManager struct{}

func (systemdManager) name() string { return "systemd" }

// systemdUnitTemplate is the [Unit]/[Service]/[Install] definition. A user
// unit installs into the default.target (the user's "logged in" target).
var systemdUnitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description=openctl controller
Documentation=https://github.com/openctl/openctl
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.BinaryPath}} serve
WorkingDirectory={{.HomeDir}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`))

func (systemdManager) render(p *installPaths) ([]byte, error) {
	var buf bytes.Buffer
	if err := systemdUnitTemplate.Execute(&buf, struct {
		BinaryPath string
		HomeDir    string
	}{
		BinaryPath: p.BinaryPath,
		HomeDir:    p.HomeDir,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (systemdManager) reload(_ *installPaths) error {
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w (output: %s)", err, bytes.TrimSpace(out))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user enable --now %s: %w (output: %s)", systemdUnit, err, bytes.TrimSpace(out))
	}
	return nil
}

func (systemdManager) stop(_ *installPaths) error {
	// disable --now stops and unregisters in one step; best-effort.
	return exec.Command("systemctl", "--user", "disable", "--now", systemdUnit).Run()
}

// renderSystemdUnit is a thin wrapper for tests and direct callers.
func renderSystemdUnit(p *installPaths) ([]byte, error) { return systemdManager{}.render(p) }
