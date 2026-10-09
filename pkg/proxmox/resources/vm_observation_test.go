package resources

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openctl/openctl/pkg/proxmox/client"
)

func observedConfig(t *testing.T, raw string) map[string]any {
	t.Helper()
	var config client.VMConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	return VMToResourceWithIP(&client.VM{Name: "dev-system", Node: "pve", CPUs: 99, MaxMem: 1}, &config, "192.0.2.1").Spec
}

func TestNativeVMConfigurationComparesWithoutCreationDrift(t *testing.T) {
	observed := observedConfig(t, `{"cores":8,"sockets":1,"memory":"10240","scsi0":"local-lvm:vm-101-disk-0,size=300G","ide2":"local-lvm:vm-101-cloudinit,media=cdrom","net0":"virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0","agent":"1","ciuser":"ubuntu","sshkeys":"ssh-ed25519%20AAA+BBB%20user%40host%0A","ipconfig0":"ip=dhcp","cipassword":"SECRET"}`)
	desired := map[string]any{
		"context": "lab", "template": map[string]any{"name": "ubuntu"}, "image": map[string]any{"file": "ubuntu.img"}, "cloudImage": map[string]any{"url": "https://example.test/image"}, "startOnCreate": true,
		"node": "pve", "cpu": map[string]any{"cores": float64(8), "sockets": float64(1)}, "memory": map[string]any{"size": float64(10240)},
		"disks":     []any{map[string]any{"name": "scsi0", "storage": "local-lvm", "size": "300GiB"}},
		"networks":  []any{map[string]any{"name": "net0", "bridge": "vmbr0", "model": "virtio", "macAddress": "aa:bb:cc:dd:ee:ff", "firewall": false}},
		"agent":     map[string]any{"enabled": true},
		"cloudInit": map[string]any{"user": "ubuntu", "sshKeys": []any{"ssh-ed25519 AAA+BBB user@host"}, "ipConfig": map[string]any{"net0": map[string]any{"ip": "dhcp"}}, "password": "SECRET", "packages": []any{"curl"}, "runcmd": []any{"echo hello"}},
	}
	d, o := ComparableVMSpecs(desired, observed)
	if !reflect.DeepEqual(d, o) {
		t.Fatalf("unexpected drift: desired=%#v observed=%#v", d, o)
	}
	ci := observed["cloudInit"].(map[string]any)
	if _, ok := ci["password"]; ok {
		t.Fatal("write-only password exposed")
	}
	if len(observed["disks"].([]any)) != 1 {
		t.Fatal("cloud-init CD-ROM exposed as data disk")
	}
	if _, ok := observed["template"]; ok {
		t.Fatal("creation state exposed")
	}
	if desired["cloudInit"].(map[string]any)["password"] != "SECRET" {
		t.Fatal("comparison mutated desired state")
	}
	if observed["cpu"].(map[string]any)["cores"] != 8 {
		t.Fatal("comparison mutated observation")
	}
}

func TestNativeVMDriftRemainsVisible(t *testing.T) {
	observed := observedConfig(t, `{"cores":"8","sockets":"1","memory":"10240","scsi0":"local-lvm:vm-101-disk-0,size=300G,backup=0,ssd=1,discard=on,iothread=1","net0":"virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0,tag=20,firewall=1","agent":"enabled=1,fstrim_cloned_disks=1","ciuser":"ubuntu","ciupgrade":1,"ipconfig0":"ip=dhcp","sshkeys":"ssh-ed25519%20AAA+BBB"}`)
	cases := []map[string]any{
		{"cpu": map[string]any{"cores": 4}}, {"cpu": map[string]any{"sockets": 2}}, {"cpu": map[string]any{"type": "host"}},
		{"memory": map[string]any{"size": 2048}}, {"agent": map[string]any{"enabled": false}},
		{"disks": []any{map[string]any{"name": "scsi0", "size": "301G"}}},
		{"disks": []any{map[string]any{"name": "scsi1", "size": "300G"}}},
		{"disks": []any{map[string]any{"name": "scsi0", "backup": true}}},
		{"disks": []any{map[string]any{"name": "scsi0", "ssd": false, "discard": false, "iothread": false}}},
		{"networks": []any{map[string]any{"name": "net0", "firewall": false}}},
		{"networks": []any{map[string]any{"name": "net0", "bridge": "vmbr1"}}},
		{"networks": []any{map[string]any{"name": "net0", "vlan": 21}}},
		{"networks": []any{map[string]any{"name": "net1", "bridge": "vmbr0"}}},
		{"cloudInit": map[string]any{"user": "root"}}, {"cloudInit": map[string]any{"packageUpgrade": false}},
		{"cloudInit": map[string]any{"sshKeys": []any{"ssh-ed25519 CHANGED"}}},
		{"cloudInit": map[string]any{"ipConfig": map[string]any{"net0": map[string]any{"ip": "192.0.2.5/24"}}}},
		{"cloudInit": map[string]any{"ipConfig": map[string]any{"net1": map[string]any{"ip": "dhcp"}}}},
		{"futureManagedOption": true}, {"cpu": map[string]any{"futureManagedOption": true}},
	}
	for i, desired := range cases {
		d, o := ComparableVMSpecs(desired, observed)
		if reflect.DeepEqual(d, o) {
			t.Errorf("case %d lost drift for %#v", i, desired)
		}
	}
}

func TestVMNamedSlotsAndFractionalDiskSizes(t *testing.T) {
	observed := observedConfig(t, `{"scsi12":"fast:vm-101-disk-12,size=1.5T","scsi2":"slow:vm-101-disk-2,size=1G","net12":"virtio=AA:BB:CC:DD:EE:12,bridge=vmbr12","net2":"virtio=AA:BB:CC:DD:EE:02,bridge=vmbr2","ipconfig12":"ip=192.0.2.12/24,gw=192.0.2.1","hostpci0":"mapping=gpu,pcie=1,x-vga=1,rombar=0,mdev=nvidia-123,romfile=gpu.rom","efidisk0":"fast:vm-101-disk-0,efitype=4m,pre-enrolled-keys=1","bios":"ovmf","machine":"q35","ostype":"l26","cpu":"host,flags=+aes","nameserver":"1.1.1.1 8.8.8.8","searchdomain":"example.test"}`)
	desired := map[string]any{
		"disks":    []any{map[string]any{"name": "scsi12", "size": "1536GiB"}, map[string]any{"name": "scsi2", "size": "1024M"}},
		"networks": []any{map[string]any{"name": "net12", "bridge": "vmbr12"}},
		"hostPCI":  []any{map[string]any{"mapping": "gpu", "pcie": true, "primaryGPU": true, "rombar": false, "mdev": "nvidia-123", "romfile": "gpu.rom"}},
		"efiDisk":  map[string]any{"storage": "fast", "type": "4m", "preEnrolledKeys": true},
		"bios":     "ovmf", "machine": "q35", "osType": "l26", "cpu": map[string]any{"type": "host"},
		"cloudInit": map[string]any{"ipConfig": map[string]any{"net12": map[string]any{"ip": "192.0.2.12/24", "gateway": "192.0.2.1"}}, "searchDomain": "example.test", "nameservers": []any{"1.1.1.1", "8.8.8.8"}},
	}
	d, o := ComparableVMSpecs(desired, observed)
	if !reflect.DeepEqual(d, o) {
		t.Fatalf("slot ordering or native observation caused drift: %#v != %#v", d, o)
	}
	for _, field := range []string{"bios", "machine", "osType", "hostPCI", "efiDisk"} {
		missing := make(map[string]any, len(observed))
		for key, value := range observed {
			if key != field {
				missing[key] = value
			}
		}
		d, o := ComparableVMSpecs(desired, missing)
		if reflect.DeepEqual(d, o) {
			t.Errorf("missing %s lost drift", field)
		}
	}
}

func TestVMSSHKeyDecodingDoesNotEraseMalformedOrLiteralPlus(t *testing.T) {
	for _, key := range []string{"ssh-ed25519 AAA+BBB", "ssh-ed25519 AAA%invalid"} {
		raw, err := json.Marshal(map[string]any{"sshkeys": key})
		if err != nil {
			t.Fatal(err)
		}
		observed := observedConfig(t, string(raw))
		keys := observed["cloudInit"].(map[string]any)["sshKeys"].([]any)
		if len(keys) != 1 || keys[0] != key {
			t.Fatalf("public key changed: %v", keys)
		}
	}
}

func TestVMUnmanagedDefaultsAndExplicitFalseOptions(t *testing.T) {
	observed := observedConfig(t, `{"scsi0":"local-lvm:vm-101-disk-0,size=8G","net0":"virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0","hostpci2":"host=0000:01:00.0"}`)
	desired := map[string]any{
		"disks":    []any{map[string]any{"name": "scsi0", "ssd": false, "discard": false, "iothread": false, "backup": true}},
		"networks": []any{map[string]any{"name": "net0", "firewall": false, "vlan": 0}},
		"agent":    map[string]any{"enabled": false},
	}
	d, o := ComparableVMSpecs(desired, observed)
	if !reflect.DeepEqual(d, o) {
		t.Fatalf("native default flags caused drift: %#v != %#v", d, o)
	}
	d, o = ComparableVMSpecs(map[string]any{"disks": []any{}, "networks": []any{}}, observed)
	if !reflect.DeepEqual(d, o) {
		t.Fatal("undeclared live members became managed")
	}
	d, o = ComparableVMSpecs(map[string]any{"hostPCI": []any{map[string]any{"device": "0000:01:00.0"}}}, observed)
	if reflect.DeepEqual(d, o) {
		t.Fatal("hostpci2 incorrectly satisfied desired hostpci0")
	}
	pci := observed["hostPCI"].([]any)
	if len(pci) != 3 || pci[2].(map[string]any)["device"] != "0000:01:00.0" {
		t.Fatal("numbered PCI slot was lost")
	}
}
