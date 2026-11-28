package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const guestSSHKeyLimit = 4096
const guestExecResponseLimit = 16 * 1024

// GetVMSSHHostKey reads the guest's existing Ed25519 host public key through
// verified Proxmox TLS. Execution is submitted exactly once; an uncertain
// submission or status response fails closed, without retrying the command.
func (c *Client) GetVMSSHHostKey(ctx context.Context, node string, vmid int) (ssh.PublicKey, error) {
	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify || !strings.HasPrefix(c.endpoint, "https://") {
		return nil, errors.New("SSH host key inspection requires certificate-verified Proxmox HTTPS")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/agent/", url.PathEscape(node), vmid)
	// Repeated form values encode Proxmox's command array. There is no shell,
	// caller-supplied command, stdin, or guest filesystem mutation.
	command := url.Values{"command": {"/bin/cat", "/etc/ssh/ssh_host_ed25519_key.pub"}}
	data, err := c.guestSSHRequest(ctx, http.MethodPost, path+"exec", command)
	if err != nil {
		return nil, err
	}
	if len(data) != 1 {
		return nil, guestSSHFailure(ctx, "invalid execution identity")
	}
	pid, err := guestExecInteger(data["pid"])
	if err != nil || pid <= 0 || pid > 2147483647 {
		return nil, guestSSHFailure(ctx, "invalid execution identity")
	}
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	for {
		data, err = c.guestSSHRequest(ctx, http.MethodGet, path+"exec-status?pid="+strconv.FormatInt(pid, 10), nil)
		if err != nil {
			return nil, err
		}
		for key := range data {
			switch key {
			case "exited", "exitcode", "signal", "out-data", "err-data", "out-truncated", "err-truncated":
			default:
				return nil, guestSSHFailure(ctx, "unknown execution status")
			}
		}
		exited, err := guestExecFlag(data["exited"])
		if err != nil {
			return nil, guestSSHFailure(ctx, "invalid execution status")
		}
		if !exited {
			if len(data) != 1 {
				return nil, guestSSHFailure(ctx, "inconsistent execution status")
			}
			select {
			case <-ctx.Done():
				return nil, guestSSHFailure(ctx, "execution did not finish")
			case <-poll.C:
			}
			continue
		}
		exitcode, err := guestExecInteger(data["exitcode"])
		if err != nil || exitcode != 0 || data["signal"] != nil {
			return nil, guestSSHFailure(ctx, "guest key inspection did not exit successfully")
		}
		// QEMU omits optional truncation flags when false. The key's exact
		// line/length/crypto checks remain mandatory even when flags are absent.
		if value, present := data["out-truncated"]; present {
			truncated, err := guestExecFlag(value)
			if err != nil || truncated {
				return nil, guestSSHFailure(ctx, "invalid or truncated guest key output")
			}
		}
		if value, present := data["err-truncated"]; present {
			truncated, err := guestExecFlag(value)
			if err != nil || truncated {
				return nil, guestSSHFailure(ctx, "invalid guest key diagnostics")
			}
		}
		if value, present := data["err-data"]; present {
			var stderr string
			if json.Unmarshal(value, &stderr) != nil || bytes.Equal(value, []byte("null")) || stderr != "" {
				return nil, guestSSHFailure(ctx, "guest key inspection reported an error")
			}
		}
		var stdout string
		if json.Unmarshal(data["out-data"], &stdout) != nil || len(stdout) == 0 || len(stdout) > guestSSHKeyLimit {
			return nil, guestSSHFailure(ctx, "missing or invalid guest key output")
		}
		// ParseAuthorizedKey can skip comments and preceding garbage. Restrict
		// the input to one actual public-key line before using its crypto parser.
		line := strings.TrimSpace(stdout)
		if strings.ContainsAny(line, "\r\n") || !strings.HasPrefix(line, ssh.KeyAlgoED25519+" ") {
			return nil, guestSSHFailure(ctx, "expected one Ed25519 SSH host public key")
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(options) != 0 || len(rest) != 0 || key.Type() != ssh.KeyAlgoED25519 {
			return nil, guestSSHFailure(ctx, "invalid Ed25519 SSH host public key")
		}
		if ctx.Err() != nil {
			return nil, guestSSHFailure(ctx, "inspection was cancelled")
		}
		return key, nil
	}
}

func guestSSHFailure(ctx context.Context, reason string) error {
	const prerequisite = "; requires a running VM with QEMU guest agent enabled and running, and Proxmox guest-agent execution access (VM.Monitor on Proxmox 8, VM.GuestAgent.Unrestricted on newer versions)"
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("SSH host key inspection: %s%s: %w", reason, prerequisite, err)
	}
	return errors.New("SSH host key inspection: " + reason + prerequisite)
}

// This request deliberately bypasses doRequest: provider response bodies and
// guest output must neither enter diagnostics/debug logs nor be read unbounded.
func (c *Client) guestSSHRequest(ctx context.Context, method, path string, values url.Values) (map[string]json.RawMessage, error) {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return nil, guestSSHFailure(ctx, "cannot construct guest-agent request")
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.tokenID+"="+c.tokenSecret)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, guestSSHFailure(ctx, "guest-agent request failed; execution was not resubmitted")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, guestSSHFailure(ctx, "guest-agent request refused (HTTP "+strconv.Itoa(resp.StatusCode)+"); execution was not resubmitted")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, guestExecResponseLimit+1))
	if err != nil || len(raw) > guestExecResponseLimit {
		return nil, guestSSHFailure(ctx, "invalid or oversized guest-agent response")
	}
	envelope, err := guestExecObject(raw)
	if err != nil || len(envelope) != 1 {
		return nil, guestSSHFailure(ctx, "unknown guest-agent response")
	}
	data, err := guestExecObject(envelope["data"])
	if err != nil {
		return nil, guestSSHFailure(ctx, "invalid guest-agent response")
	}
	return data, nil
}

func guestExecObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errors.New("expected object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || fields[name] != nil {
			return nil, errors.New("invalid or duplicate field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing response")
	}
	return fields, nil
}

func guestExecInteger(raw json.RawMessage) (int64, error) {
	return strconv.ParseInt(string(raw), 10, 64)
}

func guestExecFlag(raw json.RawMessage) (bool, error) {
	// Proxmox converts QGA booleans to JSON 0/1; also accept native booleans.
	switch string(raw) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, errors.New("expected boolean")
	}
}
