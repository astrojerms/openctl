package client

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var debugEnabled = os.Getenv("OPENCTL_DEBUG") != ""

func debugf(format string, args ...any) {
	if debugEnabled {
		fmt.Fprintf(os.Stderr, "[proxmox-debug] "+format+"\n", args...)
	}
}

// Client is a Proxmox API client
type Client struct {
	endpoint    string
	tokenID     string
	tokenSecret string
	username    string
	password    string
	ticket      string
	csrfToken   string
	httpClient  *http.Client
}

// New creates a new Proxmox client with API token authentication
func New(endpoint, tokenID, tokenSecret string) *Client {
	return &Client{
		endpoint:    strings.TrimSuffix(endpoint, "/"),
		tokenID:     tokenID,
		tokenSecret: tokenSecret,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		},
	}
}

// NewWithPassword creates a new Proxmox client with username/password authentication
// This allows using arbitrary filesystem paths (which API tokens cannot do)
func NewWithPassword(endpoint, username, password string) *Client {
	return &Client{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		username: username,
		password: password,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		},
	}
}

// authenticate gets a ticket for username/password auth
func (c *Client) authenticate() error {
	if c.username == "" || c.password == "" {
		return nil // Using API token auth
	}

	if c.ticket != "" {
		return nil // Already authenticated
	}

	debugf("Authenticating as user %s", c.username)

	reqURL := c.endpoint + "/api2/json/access/ticket"
	data := url.Values{}
	data.Set("username", c.username)
	data.Set("password", c.password)

	req, err := http.NewRequest("POST", reqURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("authentication failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			Ticket            string `json:"ticket"`
			CSRFPreventionToken string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse auth response: %w", err)
	}

	c.ticket = result.Data.Ticket
	c.csrfToken = result.Data.CSRFPreventionToken
	debugf("Authentication successful, got ticket")

	return nil
}

// VM represents a Proxmox VM
type VM struct {
	VMID      int     `json:"vmid"`
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	Mem       int64   `json:"mem"`
	MaxMem    int64   `json:"maxmem"`
	Disk      int64   `json:"disk"`
	MaxDisk   int64   `json:"maxdisk"`
	CPU       float64 `json:"cpu"`
	CPUs      int     `json:"cpus"`
	Uptime    int64   `json:"uptime"`
	Node      string  `json:"node"`
	Template  int     `json:"template"`
	NetIn     int64   `json:"netin"`
	NetOut    int64   `json:"netout"`
	DiskRead  int64   `json:"diskread"`
	DiskWrite int64   `json:"diskwrite"`
}

// VMConfig represents VM configuration
type VMConfig struct {
	Name      string `json:"name"`
	Cores     int    `json:"cores"`
	Sockets   int    `json:"sockets"`
	Memory    int    `json:"memory"`
	Boot      string `json:"boot"`
	OSType    string `json:"ostype"`
	SCSI0     string `json:"scsi0"`
	Net0      string `json:"net0"`
	IDE2      string `json:"ide2"`
	IPConfig0 string `json:"ipconfig0"`
	CIUser    string `json:"ciuser"`
	SSHKeys   string `json:"sshkeys"`
}

// Template represents a VM template
type Template struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Node   string `json:"node"`
	Status string `json:"status"`
}

// ListVMs lists all VMs across all nodes
func (c *Client) ListVMs() ([]*VM, error) {
	debugf("ListVMs: fetching nodes from %s", c.endpoint)
	nodes, err := c.listNodes()
	if err != nil {
		debugf("ListVMs: failed to list nodes: %v", err)
		return nil, err
	}
	debugf("ListVMs: found %d nodes: %v", len(nodes), nodes)

	var allVMs []*VM
	for _, node := range nodes {
		debugf("ListVMs: fetching VMs from node %s", node)
		vms, err := c.listNodeVMs(node)
		if err != nil {
			debugf("ListVMs: failed to list VMs on node %s: %v", node, err)
			continue
		}
		debugf("ListVMs: found %d VMs on node %s", len(vms), node)
		for _, vm := range vms {
			vm.Node = node
			debugf("ListVMs: VM vmid=%d name=%s template=%d status=%s", vm.VMID, vm.Name, vm.Template, vm.Status)
			allVMs = append(allVMs, vm)
		}
	}

	debugf("ListVMs: total VMs found: %d", len(allVMs))
	return allVMs, nil
}

// GetVM gets a specific VM by name
func (c *Client) GetVM(name string) (*VM, error) {
	vms, err := c.ListVMs()
	if err != nil {
		return nil, err
	}

	for _, vm := range vms {
		if vm.Name == name {
			return vm, nil
		}
	}

	return nil, fmt.Errorf("VM %q not found", name)
}

// GetVMConfig gets the configuration for a VM
func (c *Client) GetVMConfig(node string, vmid int) (*VMConfig, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", node, vmid)
	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data VMConfig `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse VM config: %w", err)
	}

	return &result.Data, nil
}

// CreateVM creates a new VM with the given parameters
func (c *Client) CreateVM(node string, params map[string]any) (int, error) {
	nextID, err := c.getNextVMID()
	if err != nil {
		return 0, err
	}

	params["vmid"] = nextID

	path := fmt.Sprintf("/api2/json/nodes/%s/qemu", node)
	_, err = c.post(path, params)
	if err != nil {
		return 0, err
	}

	return nextID, nil
}

// CreateVMFromImage creates a new VM and imports a disk image using a two-step process.
// Step 1: Create VM with basic config (no disk)
// Step 2: Import the disk using the config endpoint (which properly handles paths)
//
// This approach allows using arbitrary filesystem paths for the source image.
// Returns vmid and any task UPID for the import operation.
func (c *Client) CreateVMFromImage(node string, name string, imageStorage string, imageFile string, contentType string, targetStorage string, params map[string]any) (int, string, error) {
	nextID, err := c.getNextVMID()
	if err != nil {
		return 0, "", err
	}

	// Determine target storage for the disk
	diskStorage := targetStorage
	if diskStorage == "" {
		diskStorage = imageStorage
	}

	// Step 1: Create VM with basic config but NO disk
	createParams := map[string]any{
		"vmid": nextID,
		"name": name,
	}
	for k, v := range params {
		// Copy all params except disk-related ones (we'll add disk in step 2)
		if k != "scsi0" && k != "sata0" && k != "ide0" && k != "virtio0" {
			createParams[k] = v
		}
	}

	// Set scsihw if not specified (virtio-scsi-pci is recommended for cloud images)
	if _, hasScsihw := createParams["scsihw"]; !hasScsihw {
		createParams["scsihw"] = "virtio-scsi-pci"
	}

	debugf("CreateVMFromImage: Step 1 - creating VM %d with params: %v", nextID, createParams)

	path := fmt.Sprintf("/api2/json/nodes/%s/qemu", node)
	_, err = c.post(path, createParams)
	if err != nil {
		return 0, "", fmt.Errorf("failed to create VM: %w", err)
	}

	// Step 2: Import the disk using the config endpoint
	// Build the import-from path
	var importPath string
	if strings.HasPrefix(imageFile, "/") {
		// Absolute path provided - use as-is
		// Note: This requires root privileges which API tokens don't have
		importPath = imageFile
	} else if strings.Contains(imageFile, ":") {
		// Full volume ID provided (e.g., "local:iso/image.img") - use as-is
		importPath = imageFile
	} else {
		// Build volume ID from storage + contentType + filename
		// For import to work, we need to use a volume ID format
		if contentType == "" {
			contentType = "iso"
		}
		importPath = fmt.Sprintf("%s:%s/%s", imageStorage, contentType, imageFile)
	}

	// Configure the disk with import-from
	// Format: <storage>:0,import-from=<path>
	diskConfig := fmt.Sprintf("%s:0,import-from=%s", diskStorage, importPath)

	configParams := map[string]any{
		"scsi0": diskConfig,
	}

	debugf("CreateVMFromImage: Step 2 - importing disk with config: %v", configParams)

	configPath := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", node, nextID)
	resp, err := c.post(configPath, configParams)
	if err != nil {
		// If disk import fails, try to clean up the VM
		debugf("CreateVMFromImage: disk import failed, cleaning up VM %d", nextID)
		_ = c.DeleteVM(node, nextID)
		return 0, "", fmt.Errorf("failed to import disk: %w", err)
	}

	// Set boot order after disk is attached
	bootParams := map[string]any{
		"boot": "order=scsi0",
	}
	_, _ = c.put(configPath, bootParams)

	// Extract task UPID if present (for async import tracking)
	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		// Not all operations return a task
		return nextID, "", nil
	}

	return nextID, result.Data, nil
}

// CloneVM clones a template to create a new VM
func (c *Client) CloneVM(node string, templateID int, name string, params map[string]any) (int, string, error) {
	nextID, err := c.getNextVMID()
	if err != nil {
		return 0, "", err
	}

	cloneParams := map[string]any{
		"newid": nextID,
		"name":  name,
		"full":  1,
	}
	for k, v := range params {
		cloneParams[k] = v
	}

	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/clone", node, templateID)
	resp, err := c.post(path, cloneParams)
	if err != nil {
		return 0, "", err
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nextID, "", nil
	}

	return nextID, result.Data, nil
}

// ConfigureVM updates VM configuration
func (c *Client) ConfigureVM(node string, vmid int, params map[string]any) error {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", node, vmid)
	_, err := c.put(path, params)
	return err
}

// ResizeVMDisk resizes a VM disk
func (c *Client) ResizeVMDisk(node string, vmid int, disk string, size string) error {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/resize", node, vmid)
	params := map[string]any{
		"disk": disk,
		"size": size,
	}
	_, err := c.put(path, params)
	return err
}

// StartVM starts a VM
func (c *Client) StartVM(node string, vmid int) (string, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/start", node, vmid)
	resp, err := c.post(path, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", nil
	}

	return result.Data, nil
}

// StopVM stops a VM
func (c *Client) StopVM(node string, vmid int) (string, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/stop", node, vmid)
	resp, err := c.post(path, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", nil
	}

	return result.Data, nil
}

// DeleteVM deletes a VM
func (c *Client) DeleteVM(node string, vmid int) error {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d", node, vmid)
	_, err := c.delete(path)
	return err
}

// ListTemplates lists all VM templates
func (c *Client) ListTemplates() ([]*Template, error) {
	vms, err := c.ListVMs()
	if err != nil {
		return nil, err
	}

	var templates []*Template
	for _, vm := range vms {
		if vm.Template == 1 {
			templates = append(templates, &Template{
				VMID:   vm.VMID,
				Name:   vm.Name,
				Node:   vm.Node,
				Status: vm.Status,
			})
		}
	}

	return templates, nil
}

// GetTemplate gets a template by name
func (c *Client) GetTemplate(name string) (*Template, error) {
	templates, err := c.ListTemplates()
	if err != nil {
		return nil, err
	}

	for _, t := range templates {
		if t.Name == name {
			return t, nil
		}
	}

	return nil, fmt.Errorf("template %q not found", name)
}

// StorageContent represents a file in Proxmox storage
type StorageContent struct {
	Volid   string `json:"volid"`
	Format  string `json:"format"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
}

// ListStorageContent lists contents of a storage
func (c *Client) ListStorageContent(node, storage, contentType string) ([]*StorageContent, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/storage/%s/content", node, storage)
	if contentType != "" {
		path += "?content=" + contentType
	}

	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []*StorageContent `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse storage content: %w", err)
	}

	return result.Data, nil
}

// StorageInfo represents Proxmox storage configuration
type StorageInfo struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Path    string `json:"path"`
}

// GetStorageInfo gets information about a storage
func (c *Client) GetStorageInfo(storage string) (*StorageInfo, error) {
	path := fmt.Sprintf("/api2/json/storage/%s", storage)
	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data StorageInfo `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse storage info: %w", err)
	}

	return &result.Data, nil
}

// DownloadToStorage downloads a file from URL to Proxmox storage
// Returns the task UPID for tracking progress
func (c *Client) DownloadToStorage(node, storage, url, filename, contentType string) (string, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/storage/%s/download-url", node, storage)

	params := map[string]any{
		"url":      url,
		"filename": filename,
		"content":  contentType,
	}

	debugf("DownloadToStorage: downloading %s to %s:%s/%s", url, storage, contentType, filename)

	resp, err := c.post(path, params)
	if err != nil {
		return "", fmt.Errorf("failed to start download: %w", err)
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", nil
	}

	return result.Data, nil
}

// ConvertToTemplate converts a VM to a template
func (c *Client) ConvertToTemplate(node string, vmid int) error {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/template", node, vmid)
	debugf("ConvertToTemplate: converting VM %d to template", vmid)

	_, err := c.post(path, nil)
	return err
}

// AddCloudInitDrive adds a cloud-init drive to a VM
func (c *Client) AddCloudInitDrive(node string, vmid int, storage string) error {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", node, vmid)

	params := map[string]any{
		"ide2": fmt.Sprintf("%s:cloudinit", storage),
	}

	debugf("AddCloudInitDrive: adding cloud-init drive to VM %d on storage %s", vmid, storage)

	_, err := c.put(path, params)
	return err
}

// SetCloudInitConfig sets cloud-init configuration for a VM
func (c *Client) SetCloudInitConfig(node string, vmid int, config map[string]any) error {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", node, vmid)

	debugf("SetCloudInitConfig: setting cloud-init config for VM %d: %v", vmid, config)

	_, err := c.put(path, config)
	return err
}

// RegenerateCloudInit regenerates the cloud-init image
func (c *Client) RegenerateCloudInit(node string, vmid int) error {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/cloudinit", node, vmid)

	debugf("RegenerateCloudInit: regenerating cloud-init for VM %d", vmid)

	_, err := c.put(path, nil)
	return err
}

// WaitForTask waits for a task to complete
func (c *Client) WaitForTask(node string, upid string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		status, err := c.getTaskStatus(node, upid)
		if err != nil {
			return err
		}

		if status == "stopped" {
			return nil
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("task did not complete within timeout")
}

func (c *Client) getTaskStatus(node, upid string) (string, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/tasks/%s/status", node, url.PathEscape(upid))
	resp, err := c.get(path)
	if err != nil {
		return "", err
	}

	var result struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
	}

	return result.Data.Status, nil
}

func (c *Client) listNodes() ([]string, error) {
	resp, err := c.get("/api2/json/nodes")
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []struct {
			Node string `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse nodes: %w", err)
	}

	nodes := make([]string, len(result.Data))
	for i, n := range result.Data {
		nodes[i] = n.Node
	}

	return nodes, nil
}

func (c *Client) listNodeVMs(node string) ([]*VM, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu", node)
	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []*VM `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("failed to parse VMs: %w", err)
	}

	return result.Data, nil
}

func (c *Client) getNextVMID() (int, error) {
	resp, err := c.get("/api2/json/cluster/nextid")
	if err != nil {
		return 0, err
	}

	var result struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return 0, fmt.Errorf("failed to parse next VMID: %w", err)
	}

	var vmid int
	fmt.Sscanf(result.Data, "%d", &vmid)

	return vmid, nil
}

func (c *Client) get(path string) ([]byte, error) {
	return c.doRequest("GET", path, nil)
}

func (c *Client) post(path string, params map[string]any) ([]byte, error) {
	return c.doRequest("POST", path, params)
}

func (c *Client) put(path string, params map[string]any) ([]byte, error) {
	return c.doRequest("PUT", path, params)
}

func (c *Client) delete(path string) ([]byte, error) {
	return c.doRequest("DELETE", path, nil)
}

func (c *Client) doRequest(method, path string, params map[string]any) ([]byte, error) {
	reqURL := c.endpoint + path
	debugf("HTTP %s %s", method, reqURL)

	var body io.Reader
	if params != nil {
		values := url.Values{}
		for k, v := range params {
			values.Set(k, fmt.Sprintf("%v", v))
		}
		body = bytes.NewBufferString(values.Encode())
	}

	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("PVEAPIToken=%s=%s", c.tokenID, c.tokenSecret))
	if params != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		debugf("HTTP request failed: %v", err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	debugf("HTTP response status: %d", resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	debugf("HTTP response body: %s", truncate(string(respBody), 500))

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// AgentNetworkInterface represents a network interface from QEMU guest agent
type AgentNetworkInterface struct {
	Name        string             `json:"name"`
	HWAddr      string             `json:"hardware-address"`
	IPAddresses []AgentIPAddress   `json:"ip-addresses"`
}

// AgentIPAddress represents an IP address from QEMU guest agent
type AgentIPAddress struct {
	Type    string `json:"ip-address-type"` // ipv4 or ipv6
	Address string `json:"ip-address"`
	Prefix  int    `json:"prefix"`
}

// GetVMIPAddress gets the IP address of a VM using the QEMU guest agent
func (c *Client) GetVMIPAddress(node string, vmid int) (string, error) {
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/network-get-interfaces", node, vmid)
	resp, err := c.get(path)
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces from agent: %w", err)
	}

	var result struct {
		Data struct {
			Result []AgentNetworkInterface `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("failed to parse agent response: %w", err)
	}

	// Find the first non-loopback IPv4 address
	for _, iface := range result.Data.Result {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if addr.Type == "ipv4" && !strings.HasPrefix(addr.Address, "127.") {
				return addr.Address, nil
			}
		}
	}

	return "", fmt.Errorf("no IPv4 address found")
}

// WaitForVMIP waits for a VM to get an IP address
func (c *Client) WaitForVMIP(node string, vmid int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timeout waiting for VM IP")
		}

		ip, err := c.GetVMIPAddress(node, vmid)
		if err == nil && ip != "" {
			return ip, nil
		}

		<-ticker.C
	}
}
