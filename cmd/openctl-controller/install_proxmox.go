package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openctl/openctl/internal/config"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/pkg/protocol"
)

const (
	defaultBootstrapVMName        = "openctl-controller"
	defaultBootstrapUser          = "ubuntu"
	defaultBootstrapCores         = 2
	defaultBootstrapMemory        = 4096
	defaultBootstrapDiskGB        = 32
	defaultBootstrapBridge        = "vmbr0"
	defaultBootstrapSSHWait       = 10 * time.Minute
	defaultBootstrapProbeInterval = 5 * time.Second
)

type proxmoxInstallTarget struct {
	Context string
	Options proxmoxBootstrapOptions
}

type proxmoxBootstrapOptions struct {
	VMName           string
	Node             string
	Template         string
	CloudImage       string
	Storage          string
	DiskStorage      string
	SSHUser          string
	SSHPublicKeyPath string
	SSHPublicKey     string
	Cores            int
	MemoryMB         int
	DiskGB           int
	Bridge           string
	IP               string
	Gateway          string
}

func parseProxmoxTarget(raw string) (*proxmoxInstallTarget, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse target %q: %w", raw, err)
	}
	if u.Scheme != "proxmox" {
		return nil, fmt.Errorf("target must be proxmox://context (got scheme %q)", u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("proxmox target must not include a user; use proxmox://context")
	}
	ctxName := u.Hostname()
	if ctxName == "" {
		return nil, fmt.Errorf("target %q is missing a Proxmox context", raw)
	}
	if u.Port() != "" {
		return nil, fmt.Errorf("proxmox target must not include a port")
	}
	if strings.Trim(u.EscapedPath(), "/") != "" {
		return nil, fmt.Errorf("proxmox target must not include a path")
	}

	opts := proxmoxBootstrapOptions{}
	q := u.Query()
	allowed := map[string]bool{
		"name":           true,
		"node":           true,
		"template":       true,
		"cloud-image":    true,
		"storage":        true,
		"disk-storage":   true,
		"ssh-user":       true,
		"ssh-public-key": true,
		"cores":          true,
		"memory":         true,
		"disk-gb":        true,
		"bridge":         true,
		"ip":             true,
		"gateway":        true,
	}
	for key := range q {
		if !allowed[key] {
			return nil, fmt.Errorf("unknown proxmox target option %q", key)
		}
	}

	opts.VMName = q.Get("name")
	opts.Node = q.Get("node")
	opts.Template = q.Get("template")
	opts.CloudImage = q.Get("cloud-image")
	opts.Storage = q.Get("storage")
	opts.DiskStorage = q.Get("disk-storage")
	opts.SSHUser = q.Get("ssh-user")
	opts.SSHPublicKeyPath = q.Get("ssh-public-key")
	opts.Bridge = q.Get("bridge")
	opts.IP = q.Get("ip")
	opts.Gateway = q.Get("gateway")
	if opts.Cores, err = positiveIntOption(q, "cores"); err != nil {
		return nil, err
	}
	if opts.MemoryMB, err = positiveIntOption(q, "memory"); err != nil {
		return nil, err
	}
	if opts.DiskGB, err = positiveIntOption(q, "disk-gb"); err != nil {
		return nil, err
	}

	return &proxmoxInstallTarget{Context: ctxName, Options: opts}, nil
}

func positiveIntOption(q url.Values, key string) (int, error) {
	raw := q.Get(key)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, nil
}

func applyProxmoxBootstrapDefaults(opts proxmoxBootstrapOptions, cfg *protocol.ProviderConfig) (proxmoxBootstrapOptions, error) {
	defaults := map[string]string{}
	if cfg != nil {
		defaults = cfg.Defaults
		if opts.Node == "" {
			opts.Node = cfg.Node
		}
	}
	opts.VMName = firstNonEmpty(opts.VMName, defaults["controllerVMName"], defaults["name"], defaultBootstrapVMName)
	opts.Node = firstNonEmpty(opts.Node, defaults["node"])
	opts.Template = firstNonEmpty(opts.Template, defaults["template"])
	opts.CloudImage = firstNonEmpty(opts.CloudImage, defaults["cloudImage"])
	opts.Storage = firstNonEmpty(opts.Storage, defaults["storage"])
	opts.DiskStorage = firstNonEmpty(opts.DiskStorage, defaults["diskStorage"], opts.Storage)
	opts.SSHUser = firstNonEmpty(opts.SSHUser, defaults["sshUser"], defaultBootstrapUser)
	opts.SSHPublicKeyPath = firstNonEmpty(opts.SSHPublicKeyPath, defaults["sshPublicKey"])
	opts.Bridge = firstNonEmpty(opts.Bridge, defaults["network"], defaults["bridge"], defaultBootstrapBridge)
	opts.IP = firstNonEmpty(opts.IP, defaults["ip"])
	opts.Gateway = firstNonEmpty(opts.Gateway, defaults["gateway"])

	var err error
	if opts.Cores == 0 {
		if opts.Cores, err = defaultPositiveInt(defaults, "cores", defaultBootstrapCores); err != nil {
			return opts, err
		}
	}
	if opts.MemoryMB == 0 {
		if opts.MemoryMB, err = defaultPositiveInt(defaults, "memory", defaultBootstrapMemory); err != nil {
			return opts, err
		}
	}
	if opts.DiskGB == 0 {
		if opts.DiskGB, err = defaultPositiveInt(defaults, "diskGB", defaultBootstrapDiskGB); err != nil {
			return opts, err
		}
	}

	if opts.Node == "" {
		return opts, fmt.Errorf("proxmox bootstrap requires node=... or a Proxmox context/default node")
	}
	if (opts.Template == "") == (opts.CloudImage == "") {
		return opts, fmt.Errorf("proxmox bootstrap requires exactly one of template=... or cloud-image=... to be set")
	}
	if opts.CloudImage != "" && opts.Storage == "" {
		return opts, fmt.Errorf("proxmox bootstrap with cloud-image requires storage=... or defaults.storage")
	}
	return opts, nil
}

func defaultPositiveInt(defaults map[string]string, key string, fallback int) (int, error) {
	raw := defaults[key]
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("defaults.%s must be a positive integer", key)
	}
	return n, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func buildProxmoxBootstrapVM(opts proxmoxBootstrapOptions) *protocol.Resource {
	spec := map[string]any{
		"node":          opts.Node,
		"startOnCreate": true,
		"agent": map[string]any{
			"enabled": true,
		},
		"cpu": map[string]any{
			"cores":   opts.Cores,
			"sockets": 1,
		},
		"memory": map[string]any{
			"size": opts.MemoryMB,
		},
		"disks": []any{
			map[string]any{
				"name":    "scsi0",
				"size":    fmt.Sprintf("%dG", opts.DiskGB),
				"ssd":     true,
				"discard": true,
			},
		},
		"networks": []any{
			map[string]any{
				"name":   "net0",
				"bridge": opts.Bridge,
				"model":  "virtio",
			},
		},
		"cloudInit": map[string]any{
			"user":    opts.SSHUser,
			"sshKeys": []any{strings.TrimSpace(opts.SSHPublicKey)},
		},
	}
	if opts.Template != "" {
		spec["template"] = map[string]any{"name": opts.Template}
	}
	if opts.CloudImage != "" {
		cloudImage := map[string]any{
			"url":     opts.CloudImage,
			"storage": opts.Storage,
		}
		if opts.DiskStorage != "" {
			cloudImage["diskStorage"] = opts.DiskStorage
		}
		spec["cloudImage"] = cloudImage
	}
	if opts.DiskStorage != "" {
		spec["disks"].([]any)[0].(map[string]any)["storage"] = opts.DiskStorage
	}
	if opts.IP != "" {
		ipConfig := map[string]any{"ip": opts.IP}
		if opts.Gateway != "" {
			ipConfig["gateway"] = opts.Gateway
		}
		spec["cloudInit"].(map[string]any)["ipConfig"] = map[string]any{"net0": ipConfig}
	}

	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name: opts.VMName,
		},
		Spec: spec,
	}
}

func runInstallProxmox(rawTarget, sshKeyFlag string) error {
	target, err := parseProxmoxTarget(rawTarget)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	pcfg, err := cfg.GetProviderConfig("proxmox", target.Context)
	if err != nil {
		return fmt.Errorf("load proxmox context %q: %w", target.Context, err)
	}
	if pcfg.Endpoint == "" {
		return fmt.Errorf("proxmox context %q has no endpoint", target.Context)
	}
	opts, err := applyProxmoxBootstrapDefaults(target.Options, pcfg)
	if err != nil {
		return err
	}
	if opts.SSHPublicKeyPath == "" {
		key, err := resolveSSHKey(sshKeyFlag)
		if err != nil {
			return err
		}
		opts.SSHPublicKeyPath = key + ".pub"
	}
	opts.SSHPublicKeyPath, err = config.ExpandPath(opts.SSHPublicKeyPath)
	if err != nil {
		return fmt.Errorf("expand SSH public key path: %w", err)
	}
	pub, err := os.ReadFile(opts.SSHPublicKeyPath) // #nosec G304 -- operator-configured install key
	if err != nil {
		return fmt.Errorf("read SSH public key %s: %w", opts.SSHPublicKeyPath, err)
	}
	opts.SSHPublicKey = string(pub)
	if strings.TrimSpace(opts.SSHPublicKey) == "" {
		return fmt.Errorf("SSH public key %s is empty", opts.SSHPublicKeyPath)
	}

	ctx := context.Background()
	prov := pmprovider.New(pcfg)
	manifest := buildProxmoxBootstrapVM(opts)

	fmt.Printf("Creating controller VM %q on Proxmox context %q ...\n", opts.VMName, target.Context)
	applied, err := prov.Apply(ctx, manifest)
	if err != nil {
		return fmt.Errorf("create controller VM: %w", err)
	}
	ip := observedIP(applied)
	if ip == "" {
		ip, err = waitForProxmoxBootstrapIP(ctx, prov, opts.VMName, opts.IP, 10*time.Minute)
		if err != nil {
			return err
		}
	}
	host := sshHostFromIP(ip)
	if host == "" {
		return fmt.Errorf("controller VM has unusable IP %q", ip)
	}
	sshAddr := net.JoinHostPort(host, "22")
	fmt.Printf("Waiting for SSH on controller VM %s ...\n", sshAddr)
	if err := waitForTCPListener(ctx, sshAddr, defaultBootstrapSSHWait, defaultBootstrapProbeInterval); err != nil {
		return fmt.Errorf("wait for controller VM SSH: %w", err)
	}
	fmt.Printf("Controller VM is reachable as %s; installing Linux controller over SSH ...\n", host)
	return runInstallRemote(sshInstallTarget(opts.SSHUser, host), sshKeyFlag)
}

type proxmoxVMGetter interface {
	Get(ctx context.Context, kind, name string) (*protocol.Resource, error)
}

func waitForProxmoxBootstrapIP(ctx context.Context, getter proxmoxVMGetter, name, configuredIP string, budget time.Duration) (string, error) {
	if configuredIP != "" && configuredIP != "dhcp" {
		return configuredIP, nil
	}
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		r, err := getter.Get(ctx, "VirtualMachine", name)
		if err == nil {
			if ip := observedIP(r); ip != "" {
				return ip, nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr != nil {
		return "", fmt.Errorf("controller VM did not report an IP within %s: %w", budget, lastErr)
	}
	return "", fmt.Errorf("controller VM did not report an IP within %s", budget)
}

func observedIP(r *protocol.Resource) string {
	if r == nil || r.Status == nil {
		return ""
	}
	if ip, ok := r.Status["ip"].(string); ok {
		return ip
	}
	return ""
}

func sshHostFromIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" || ip == "dhcp" {
		return ""
	}
	host, _, err := net.ParseCIDR(ip)
	if err == nil {
		return host.String()
	}
	return ip
}

func waitForTCPListener(ctx context.Context, addr string, budget, interval time.Duration) error {
	if budget <= 0 {
		return fmt.Errorf("wait budget must be positive")
	}
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.Now().Add(budget)
	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", addr, minDuration(interval, time.Second))
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		sleep := minDuration(interval, remaining)
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("%s did not accept TCP within %s: %w", addr, budget, lastErr)
}

func sshInstallTarget(user, host string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return "ssh://" + user + "@" + host
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
