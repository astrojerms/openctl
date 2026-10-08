package resources

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/openctl/openctl/pkg/proxmox/client"
)

// observeVMConfig reads native configuration only; inventory is not a substitute
// for a successfully retrieved VM configuration.
func observeVMConfig(spec map[string]any, config *client.VMConfig) {
	text := func(key, fallback string) string {
		if value, ok := config.Raw[key]; ok {
			if s, ok := value.(string); ok {
				return s
			}
			return fmt.Sprint(value)
		}
		return fallback
	}
	cores, sockets, memory := config.Cores, config.Sockets, config.Memory
	if cores == 0 {
		cores = 1
	}
	if sockets == 0 {
		sockets = 1
	}
	if memory == 0 {
		memory = 512
	}
	cpu, _ := configParts(text("cpu", "kvm64"))
	if strings.HasPrefix(cpu, "cputype=") {
		cpu = strings.TrimPrefix(cpu, "cputype=")
	}
	spec["cpu"] = map[string]any{"cores": cores, "sockets": sockets, "type": cpu}
	spec["memory"] = map[string]any{"size": memory}
	spec["agent"] = map[string]any{"enabled": configFlag(text("agent", "0"), false)}
	spec["osType"] = text("ostype", config.OSType)
	if spec["osType"] == "" {
		spec["osType"] = "other"
	}
	spec["bios"] = text("bios", "seabios")
	spec["machine"] = text("machine", "pc")

	// Typed fields preserve compatibility with explicitly constructed configs.
	slots := make(map[string]string)
	for key, value := range config.Raw {
		if numberedSlot(key, "scsi", "sata", "ide", "virtio", "net", "ipconfig", "hostpci") || key == "efidisk0" {
			if s, ok := value.(string); ok {
				slots[key] = s
			}
		}
	}
	for key, value := range map[string]string{"scsi0": config.SCSI0, "net0": config.Net0, "ide2": config.IDE2, "ipconfig0": config.IPConfig0} {
		if _, exists := slots[key]; !exists && value != "" {
			slots[key] = value
		}
	}
	keys := make([]string, 0, len(slots))
	for key := range slots {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, ni := slotIndex(keys[i])
		pj, nj := slotIndex(keys[j])
		if pi == pj {
			return ni < nj
		}
		return pi < pj
	})
	disks, networks, pci := []any{}, []any{}, []any{}
	ips := map[string]any{}
	for _, key := range keys {
		head, opts := configParts(slots[key])
		switch {
		case numberedSlot(key, "net"):
			model, mac, _ := strings.Cut(head, "=")
			vlan, _ := strconv.Atoi(opts["tag"])
			networks = append(networks, map[string]any{"name": key, "model": model, "macAddress": mac, "bridge": opts["bridge"], "vlan": vlan, "firewall": configFlag(opts["firewall"], false)})
		case numberedSlot(key, "ipconfig"):
			ips["net"+strings.TrimPrefix(key, "ipconfig")] = map[string]any{"ip": opts["ip"], "gateway": opts["gw"]}
		case numberedSlot(key, "hostpci"):
			device := head
			if strings.Contains(head, "=") {
				device = opts["host"]
			}
			_, index := slotIndex(key)
			for len(pci) <= index {
				pci = append(pci, nil)
			}
			pci[index] = map[string]any{"device": device, "mapping": opts["mapping"], "pcie": configFlag(opts["pcie"], false), "primaryGPU": configFlag(opts["x-vga"], false), "rombar": configFlag(opts["rombar"], true), "mdev": opts["mdev"], "romfile": opts["romfile"]}
		case key == "efidisk0":
			head = strings.TrimPrefix(head, "file=")
			storage, _, _ := strings.Cut(head, ":")
			typeName := opts["efitype"]
			if typeName == "" {
				typeName = "2m"
			}
			spec["efiDisk"] = map[string]any{"storage": storage, "type": typeName, "preEnrolledKeys": configFlag(opts["pre-enrolled-keys"], false)}
		default:
			head = strings.TrimPrefix(head, "file=")
			if opts["media"] == "cdrom" || strings.Contains(head, "cloudinit") || head == "none" {
				continue
			}
			storage, _, _ := strings.Cut(head, ":")
			cache := opts["cache"]
			if cache == "" {
				cache = "none"
			}
			disk := map[string]any{"name": key, "storage": storage, "ssd": configFlag(opts["ssd"], false), "discard": configFlag(opts["discard"], false), "iothread": configFlag(opts["iothread"], false), "backup": configFlag(opts["backup"], true), "cache": cache}
			if size, ok := opts["size"]; ok {
				disk["size"] = size
			}
			disks = append(disks, disk)
		}
	}
	spec["disks"], spec["networks"], spec["hostPCI"] = disks, networks, pci
	ci := map[string]any{"ipConfig": ips, "packageUpgrade": configFlag(text("ciupgrade", "1"), true)}
	if user := text("ciuser", config.CIUser); user != "" {
		ci["user"] = user
	}
	keysText := text("sshkeys", config.SSHKeys)
	// Proxmox uses percent escapes, not form encoding: literal '+' in public
	// key base64 must survive. Invalid escapes are retained rather than erased.
	if decoded, err := url.PathUnescape(keysText); err == nil {
		keysText = decoded
	}
	sshKeys := []any{}
	for _, key := range strings.Split(keysText, "\n") {
		if key = strings.TrimSpace(key); key != "" {
			sshKeys = append(sshKeys, key)
		}
	}
	ci["sshKeys"] = sshKeys
	ci["searchDomain"] = text("searchdomain", "")
	nameservers := []any{}
	for _, server := range strings.Fields(text("nameserver", "")) {
		nameservers = append(nameservers, server)
	}
	ci["nameservers"] = nameservers
	spec["cloudInit"] = ci
}

func configParts(value string) (string, map[string]string) {
	opts := make(map[string]string, strings.Count(value, ",")+1)
	head, _, _ := strings.Cut(value, ",")
	for part := range strings.SplitSeq(value, ",") {
		if key, value, ok := strings.Cut(part, "="); ok {
			opts[key] = value
		}
	}
	return head, opts
}

func configFlag(value string, fallback bool) bool {
	if value == "" {
		return fallback
	}
	head, _, _ := strings.Cut(value, ",")
	for part := range strings.SplitSeq(value, ",") {
		if enabled, ok := strings.CutPrefix(part, "enabled="); ok {
			head = enabled
			break
		}
	}
	return head == "1" || head == "on" || head == "yes" || head == "true"
}

func slotIndex(key string) (string, int) {
	i := len(key)
	for i > 0 && key[i-1] >= '0' && key[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(key[i:])
	return key[:i], n
}

func numberedSlot(key string, prefixes ...string) bool {
	prefix, _ := slotIndex(key)
	if prefix == key {
		return false
	}
	for _, candidate := range prefixes {
		if prefix == candidate {
			return true
		}
	}
	return false
}
