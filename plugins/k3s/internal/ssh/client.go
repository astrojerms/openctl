package ssh

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client is an SSH client for remote command execution
type Client struct {
	host       string
	port       int
	user       string
	privateKey string
	sshClient  *ssh.Client
}

// NewClient creates a new SSH client
func NewClient(host string, port int, user string, privateKeyPath string) (*Client, error) {
	// Expand ~ in path
	if strings.HasPrefix(privateKeyPath, "~") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		privateKeyPath = filepath.Join(homeDir, privateKeyPath[1:])
	}

	keyData, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}

	return &Client{
		host:       host,
		port:       port,
		user:       user,
		privateKey: string(keyData),
	}, nil
}

// Connect establishes an SSH connection
func (c *Client) Connect() error {
	signer, err := ssh.ParsePrivateKey([]byte(c.privateKey))
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: c.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", c.host, c.port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	c.sshClient = client
	return nil
}

// Close closes the SSH connection
func (c *Client) Close() error {
	if c.sshClient != nil {
		return c.sshClient.Close()
	}
	return nil
}

// Run executes a command and returns stdout
func (c *Client) Run(command string) (string, error) {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(command); err != nil {
		return "", fmt.Errorf("command failed: %w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// RunWithOutput executes a command and streams output
func (c *Client) RunWithOutput(command string, stdout, stderr io.Writer) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = stdout
	session.Stderr = stderr

	return session.Run(command)
}

// RunSudo executes a command with sudo
func (c *Client) RunSudo(command string) (string, error) {
	return c.Run("sudo " + command)
}

// Upload uploads data to a remote file
func (c *Client) Upload(data []byte, remotePath string, mode os.FileMode) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Use cat to write to file (simple approach)
	command := fmt.Sprintf("cat > %s && chmod %o %s", remotePath, mode, remotePath)
	session.Stdin = bytes.NewReader(data)

	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Run(command); err != nil {
		return fmt.Errorf("upload failed: %w: %s", err, stderr.String())
	}

	return nil
}

// Download downloads a file from remote
func (c *Client) Download(remotePath string) ([]byte, error) {
	output, err := c.Run(fmt.Sprintf("cat %s", remotePath))
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

// WaitForSSH waits for SSH to become available
func WaitForSSH(host string, port int, user string, privateKeyPath string, timeout time.Duration) (*Client, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for SSH: %w", lastErr)
		}

		// First check if port is open
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 5*time.Second)
		if err != nil {
			lastErr = err
			<-ticker.C
			continue
		}
		conn.Close()

		// Try to establish SSH connection
		client, err := NewClient(host, port, user, privateKeyPath)
		if err != nil {
			lastErr = err
			<-ticker.C
			continue
		}

		if err := client.Connect(); err != nil {
			lastErr = err
			<-ticker.C
			continue
		}

		return client, nil
	}
}
