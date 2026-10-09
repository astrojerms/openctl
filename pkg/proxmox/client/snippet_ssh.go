package client

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os/exec"
	"strconv"
	"strings"
)

// SetSnippetSSH configures snippet uploads to explicitly mapped Proxmox nodes.
// Configure before using the client concurrently; subsequent caller map changes
// cannot change the client's destinations. An empty user selects root.
func (c *Client) SetSnippetSSH(hosts map[string]string, user, identityFile string) {
	c.snippetSSHHosts = maps.Clone(hosts)
	c.snippetSSHUser = user
	c.snippetSSHIdentityFile = identityFile
}

// snippetUploadScript runs on the selected node. pvesm resolves the storage's
// real path (including directory and CIFS storage). Only a new temporary file
// is chmod'ed; rename publishes it atomically after the complete input arrives.
// Keep the temporary file in the destination directory to avoid cross-device
// moves, and leave an existing destination untouched on any preceding failure.
const snippetUploadScript = `set -eu
volid=$1
expected=$2
path=$(pvesm path "$volid")
case "$path" in
  /*) ;;
  *) printf '%s\n' 'pvesm returned a non-absolute snippet path' >&2; exit 1 ;;
esac
dir=${path%/*}
mkdir -p -m 0755 -- "$dir"
umask 077
tmp=$(mktemp "$dir/.openctl-snippet.XXXXXXXXXX")
trap 'rm -f -- "$tmp"' EXIT
trap 'exit 1' HUP INT TERM
cat > "$tmp"
actual=$(wc -c < "$tmp")
if ! [ "$actual" -eq "$expected" ]; then
  printf '%s\n' 'incomplete snippet input' >&2
  exit 1
fi
chmod 0644 -- "$tmp"
mv -fT -- "$tmp" "$path"
`

// shellQuote quotes a single word for the remote POSIX shell. OpenSSH joins
// command arguments into a shell command, so argv separation alone is not safe.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func safeSnippetComponent(value string, dots bool) bool {
	if value == "" || value[0] == '-' || value[0] == '.' {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || dots && r == '.' {
			continue
		}
		return false
	}
	return true
}

func safeSnippetSSHHost(host string) bool {
	if host == "" || host[0] == '-' {
		return false
	}
	for _, r := range host {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_:[]%", r) {
			continue
		}
		return false
	}
	return true
}

// UploadSnippet writes a snippet through node SSH, not the REST upload endpoint
// (which supports ISO/template/import content, not snippets). A trusted host key
// and normal OpenSSH authentication must already be available on the caller.
func (c *Client) UploadSnippet(ctx context.Context, node, storage, filename, content string) error {
	host := c.snippetSSHHosts[node]
	if host == "" {
		return fmt.Errorf("snippet SSH host is not configured for Proxmox node %q; set snippetSSH.hosts[%q] in the local context", node, node)
	}
	if !safeSnippetSSHHost(host) {
		return fmt.Errorf("invalid snippet SSH host for Proxmox node %q", node)
	}
	user := c.snippetSSHUser
	if user == "" {
		user = "root"
	}
	if !safeSnippetComponent(user, true) {
		return fmt.Errorf("invalid snippet SSH user")
	}
	if !safeSnippetComponent(storage, false) {
		return fmt.Errorf("invalid snippet storage ID %q", storage)
	}
	if !safeSnippetComponent(filename, true) {
		return fmt.Errorf("invalid snippet filename %q; use a filename without directory components", filename)
	}
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "-l", user}
	if c.snippetSSHIdentityFile != "" {
		args = append(args, "-i", c.snippetSSHIdentityFile)
	}
	volid := storage + ":snippets/" + filename
	command := "sh -c " + shellQuote(snippetUploadScript) + " sh " + shellQuote(volid) + " " + strconv.Itoa(len(content))
	args = append(args, "--", host, command)
	cmd := exec.CommandContext(ctx, "ssh", args...) // #nosec G204 -- fixed executable; validated host/user/components, -- ends options, remote arguments are shell-quoted
	cmd.Stdin = strings.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("snippet SSH upload to node %q: %w", node, ctx.Err())
		}
		return fmt.Errorf("snippet SSH upload to node %q failed: %w: %s", node, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
