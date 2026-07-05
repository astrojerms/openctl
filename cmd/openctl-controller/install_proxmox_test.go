package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestParseProxmoxTarget(t *testing.T) {
	target, err := parseProxmoxTarget("proxmox://homelab?node=pve1&template=ubuntu-22.04&ssh-user=ubuntu&cores=4&memory=8192&disk-gb=64&bridge=vmbr1&ip=192.0.2.50/24&gateway=192.0.2.1")
	if err != nil {
		t.Fatalf("parseProxmoxTarget: %v", err)
	}
	if target.Context != "homelab" {
		t.Fatalf("context = %q, want homelab", target.Context)
	}
	opts := target.Options
	if opts.Node != "pve1" || opts.Template != "ubuntu-22.04" || opts.SSHUser != "ubuntu" {
		t.Fatalf("parsed identity options = %+v", opts)
	}
	if opts.Cores != 4 || opts.MemoryMB != 8192 || opts.DiskGB != 64 {
		t.Fatalf("parsed sizing options = %+v", opts)
	}
	if opts.Bridge != "vmbr1" || opts.IP != "192.0.2.50/24" || opts.Gateway != "192.0.2.1" {
		t.Fatalf("parsed network options = %+v", opts)
	}
}

func TestParseProxmoxTargetRejectsBadShape(t *testing.T) {
	cases := []string{
		"ssh://root@host",
		"proxmox://",
		"proxmox://user@homelab",
		"proxmox://homelab:8006",
		"proxmox://homelab/path",
		"proxmox://homelab?unknown=true",
		"proxmox://homelab?cores=zero",
		"proxmox://homelab?memory=0",
	}
	for _, raw := range cases {
		if _, err := parseProxmoxTarget(raw); err == nil {
			t.Errorf("%q: expected error", raw)
		}
	}
}

func TestApplyProxmoxBootstrapDefaults(t *testing.T) {
	opts, err := applyProxmoxBootstrapDefaults(proxmoxBootstrapOptions{
		Template:     "ubuntu-template",
		SSHPublicKey: "ssh-ed25519 AAAA test",
	}, &protocol.ProviderConfig{
		Node: "pve1",
		Defaults: map[string]string{
			"storage": "local-lvm",
			"network": "vmbr2",
			"cores":   "3",
			"memory":  "6144",
			"diskGB":  "48",
		},
	})
	if err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if opts.VMName != defaultBootstrapVMName || opts.Node != "pve1" || opts.SSHUser != defaultBootstrapUser {
		t.Fatalf("defaulted identity options = %+v", opts)
	}
	if opts.DiskStorage != "local-lvm" || opts.Bridge != "vmbr2" {
		t.Fatalf("defaulted storage/network options = %+v", opts)
	}
	if opts.Cores != 3 || opts.MemoryMB != 6144 || opts.DiskGB != 48 {
		t.Fatalf("defaulted sizing options = %+v", opts)
	}
}

func TestApplyProxmoxBootstrapDefaultsRequiresNodeAndImageSource(t *testing.T) {
	_, err := applyProxmoxBootstrapDefaults(proxmoxBootstrapOptions{
		SSHPublicKey: "ssh-ed25519 AAAA test",
	}, &protocol.ProviderConfig{})
	if err == nil || !strings.Contains(err.Error(), "node") {
		t.Fatalf("missing node error = %v", err)
	}

	_, err = applyProxmoxBootstrapDefaults(proxmoxBootstrapOptions{
		Node:         "pve1",
		SSHPublicKey: "ssh-ed25519 AAAA test",
	}, &protocol.ProviderConfig{})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing image source error = %v", err)
	}
}

func TestBuildProxmoxBootstrapVMFromTemplate(t *testing.T) {
	vm := buildProxmoxBootstrapVM(proxmoxBootstrapOptions{
		VMName:       "openctl-controller",
		Node:         "pve1",
		Template:     "ubuntu-22.04",
		DiskStorage:  "local-lvm",
		SSHUser:      "ubuntu",
		SSHPublicKey: "ssh-ed25519 AAAA test",
		Cores:        2,
		MemoryMB:     4096,
		DiskGB:       32,
		Bridge:       "vmbr0",
		IP:           "192.0.2.50/24",
		Gateway:      "192.0.2.1",
	})
	if vm.APIVersion != "proxmox.openctl.io/v1" || vm.Kind != "VirtualMachine" || vm.Metadata.Name != "openctl-controller" {
		t.Fatalf("identity = %s %s %s", vm.APIVersion, vm.Kind, vm.Metadata.Name)
	}
	if got := vm.Spec["node"]; got != "pve1" {
		t.Fatalf("node = %v", got)
	}
	if tmpl := vm.Spec["template"].(map[string]any); tmpl["name"] != "ubuntu-22.04" {
		t.Fatalf("template = %v", tmpl)
	}
	if cpu := vm.Spec["cpu"].(map[string]any); cpu["cores"] != 2 || cpu["sockets"] != 1 {
		t.Fatalf("cpu = %v", cpu)
	}
	if mem := vm.Spec["memory"].(map[string]any); mem["size"] != 4096 {
		t.Fatalf("memory = %v", mem)
	}
	disk := vm.Spec["disks"].([]any)[0].(map[string]any)
	if disk["name"] != "scsi0" || disk["storage"] != "local-lvm" || disk["size"] != "32G" {
		t.Fatalf("disk = %v", disk)
	}
	net := vm.Spec["networks"].([]any)[0].(map[string]any)
	if net["name"] != "net0" || net["bridge"] != "vmbr0" || net["model"] != "virtio" {
		t.Fatalf("network = %v", net)
	}
	ci := vm.Spec["cloudInit"].(map[string]any)
	if ci["user"] != "ubuntu" {
		t.Fatalf("cloudInit user = %v", ci)
	}
	ipConfig := ci["ipConfig"].(map[string]any)["net0"].(map[string]any)
	if ipConfig["ip"] != "192.0.2.50/24" || ipConfig["gateway"] != "192.0.2.1" {
		t.Fatalf("ipConfig = %v", ipConfig)
	}
}

func TestBuildProxmoxBootstrapVMFromCloudImage(t *testing.T) {
	vm := buildProxmoxBootstrapVM(proxmoxBootstrapOptions{
		VMName:       "openctl-controller",
		Node:         "pve1",
		CloudImage:   "https://example.com/jammy.img",
		Storage:      "local",
		DiskStorage:  "local-lvm",
		SSHUser:      "ubuntu",
		SSHPublicKey: "ssh-ed25519 AAAA test",
		Cores:        2,
		MemoryMB:     4096,
		DiskGB:       32,
		Bridge:       "vmbr0",
	})
	cloudImage := vm.Spec["cloudImage"].(map[string]any)
	if cloudImage["url"] != "https://example.com/jammy.img" || cloudImage["storage"] != "local" || cloudImage["diskStorage"] != "local-lvm" {
		t.Fatalf("cloudImage = %v", cloudImage)
	}
	if _, ok := vm.Spec["template"]; ok {
		t.Fatalf("template should be absent for cloud-image VM: %v", vm.Spec["template"])
	}
}

func TestSSHHostFromIP(t *testing.T) {
	cases := map[string]string{
		"192.0.2.50/24": "192.0.2.50",
		"192.0.2.50":    "192.0.2.50",
		" dhcp ":        "",
		"":              "",
	}
	for in, want := range cases {
		if got := sshHostFromIP(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

type fakeProxmoxGetter struct {
	resource *protocol.Resource
}

func (f fakeProxmoxGetter) Get(context.Context, string, string) (*protocol.Resource, error) {
	return f.resource, nil
}

func TestWaitForProxmoxBootstrapIPUsesConfiguredStaticIP(t *testing.T) {
	ip, err := waitForProxmoxBootstrapIP(context.Background(), fakeProxmoxGetter{}, "vm", "192.0.2.50/24", time.Minute)
	if err != nil {
		t.Fatalf("waitForProxmoxBootstrapIP: %v", err)
	}
	if ip != "192.0.2.50/24" {
		t.Fatalf("ip = %q", ip)
	}
}

func TestWaitForProxmoxBootstrapIPUsesObservedIP(t *testing.T) {
	ip, err := waitForProxmoxBootstrapIP(context.Background(), fakeProxmoxGetter{resource: &protocol.Resource{
		Status: map[string]any{"ip": "192.0.2.60"},
	}}, "vm", "", time.Minute)
	if err != nil {
		t.Fatalf("waitForProxmoxBootstrapIP: %v", err)
	}
	if ip != "192.0.2.60" {
		t.Fatalf("ip = %q", ip)
	}
}

func TestWaitForTCPListenerSucceeds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if err := waitForTCPListener(context.Background(), ln.Addr().String(), 200*time.Millisecond, 10*time.Millisecond); err != nil {
		t.Fatalf("waitForTCPListener: %v", err)
	}
}

func TestWaitForTCPListenerTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	start := time.Now()
	err = waitForTCPListener(context.Background(), addr, 50*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took %s, want bounded wait", elapsed)
	}
}

func TestSSHInstallTarget(t *testing.T) {
	cases := map[string]string{
		"192.0.2.50":  "ssh://ubuntu@192.0.2.50",
		"2001:db8::1": "ssh://ubuntu@[2001:db8::1]",
	}
	for host, want := range cases {
		if got := sshInstallTarget("ubuntu", host); got != want {
			t.Errorf("sshInstallTarget(%q) = %q, want %q", host, got, want)
		}
	}
}
