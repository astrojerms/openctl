package client

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseCloudConfig strips the "#cloud-config" header and unmarshals the rest,
// so tests assert on structured content rather than brittle substring matches.
func parseCloudConfig(t *testing.T, s string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(s, "#cloud-config\n") {
		t.Fatalf("vendor data must start with #cloud-config header, got:\n%s", s)
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("rendered vendor data is not valid YAML: %v\n%s", err, s)
	}
	return out
}

func asStrings(t *testing.T, v any, field string) []string {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("%s: want a list, got %T", field, v)
	}
	out := make([]string, len(list))
	for i, e := range list {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%s[%d]: want string, got %T", field, i, e)
		}
		out[i] = s
	}
	return out
}

func TestRenderVendorData_PackagesAndRunCmd(t *testing.T) {
	got, err := RenderVendorData([]string{"open-iscsi", "nfs-common"}, []string{"echo hi", "mkdir -p /data"})
	if err != nil {
		t.Fatalf("RenderVendorData: %v", err)
	}
	cfg := parseCloudConfig(t, got)

	// package_update must be true whenever packages are requested, so the
	// index is refreshed before install.
	if cfg["package_update"] != true {
		t.Errorf("package_update = %v, want true", cfg["package_update"])
	}
	if pkgs := asStrings(t, cfg["packages"], "packages"); !equal(pkgs, []string{"open-iscsi", "nfs-common"}) {
		t.Errorf("packages = %v", pkgs)
	}
	// runcmd must lead with the qemu-guest-agent enablement (IP discovery),
	// then the user's commands in order.
	want := []string{
		"systemctl enable qemu-guest-agent",
		"systemctl start qemu-guest-agent",
		"echo hi",
		"mkdir -p /data",
	}
	if cmds := asStrings(t, cfg["runcmd"], "runcmd"); !equal(cmds, want) {
		t.Errorf("runcmd = %v, want %v", cmds, want)
	}
}

func TestRenderVendorData_NoPackages(t *testing.T) {
	got, err := RenderVendorData(nil, []string{"touch /ready"})
	if err != nil {
		t.Fatalf("RenderVendorData: %v", err)
	}
	cfg := parseCloudConfig(t, got)
	// No packages → no package_update, no packages key (omitempty).
	if _, ok := cfg["package_update"]; ok {
		t.Errorf("package_update should be omitted when no packages")
	}
	if _, ok := cfg["packages"]; ok {
		t.Errorf("packages should be omitted when empty")
	}
	want := []string{
		"systemctl enable qemu-guest-agent",
		"systemctl start qemu-guest-agent",
		"touch /ready",
	}
	if cmds := asStrings(t, cfg["runcmd"], "runcmd"); !equal(cmds, want) {
		t.Errorf("runcmd = %v, want %v", cmds, want)
	}
}

func TestRenderVendorData_OnlyAgentWhenEmpty(t *testing.T) {
	got, err := RenderVendorData(nil, nil)
	if err != nil {
		t.Fatalf("RenderVendorData: %v", err)
	}
	cfg := parseCloudConfig(t, got)
	want := []string{
		"systemctl enable qemu-guest-agent",
		"systemctl start qemu-guest-agent",
	}
	if cmds := asStrings(t, cfg["runcmd"], "runcmd"); !equal(cmds, want) {
		t.Errorf("runcmd = %v, want %v", cmds, want)
	}
}

// TestRenderVendorData_EscapesSpecialChars proves arbitrary runcmd strings with
// YAML-significant characters round-trip intact — the reason rendering goes
// through yaml.Marshal instead of hand-concatenation.
func TestRenderVendorData_EscapesSpecialChars(t *testing.T) {
	tricky := []string{
		`sh -c "echo key: value > /etc/x.conf"`, // colon-space + quotes
		`printf '%s\n' "a: b"`,                  // embedded colon
		`echo '#not a comment'`,                 // leading hash inside quotes
	}
	got, err := RenderVendorData([]string{"pkg-with-dash"}, tricky)
	if err != nil {
		t.Fatalf("RenderVendorData: %v", err)
	}
	cfg := parseCloudConfig(t, got)
	cmds := asStrings(t, cfg["runcmd"], "runcmd")
	// The two agent commands prefix the three tricky ones.
	if len(cmds) != 5 {
		t.Fatalf("runcmd length = %d, want 5", len(cmds))
	}
	if !equal(cmds[2:], tricky) {
		t.Errorf("tricky commands did not round-trip:\n got  %q\n want %q", cmds[2:], tricky)
	}
}

func TestVendorSnippetName(t *testing.T) {
	if got := VendorSnippetName(1234); got != "openctl-vendor-1234.yaml" {
		t.Errorf("VendorSnippetName(1234) = %q", got)
	}
	// Distinct per VM so contents never collide.
	if VendorSnippetName(1) == VendorSnippetName(2) {
		t.Errorf("VendorSnippetName must be unique per vmid")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
