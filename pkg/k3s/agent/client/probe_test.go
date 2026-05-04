package client

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/k3s/agent"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
)

// TestProbeNodesOneUpOneDown spins up one real agent, leaves a phantom
// endpoint dangling, and confirms ProbeNodes reports each correctly without
// failing the whole call.
func TestProbeNodesOneUpOneDown(t *testing.T) {
	bundle, err := certs.GenerateBundle("probe-test", []certs.NodeIdentity{
		{Name: "alive", IP: "127.0.0.1"},
		{Name: "dead", IP: "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("certs: %v", err)
	}
	dir := t.TempDir()
	caPath := writeFile(t, dir, "ca.pem", bundle.CACertPEM)
	clientCertPath := writeFile(t, dir, "client.pem", bundle.ClientCertPEM)
	clientKeyPath := writeFile(t, dir, "client.key", bundle.ClientKeyPEM)

	// Start a real server for "alive" only.
	aliveServer := bundle.ServerCerts["alive"]
	aliveCertPath := writeFile(t, dir, "alive.pem", aliveServer.CertPEM)
	aliveKeyPath := writeFile(t, dir, "alive.key", aliveServer.KeyPEM)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen alive: %v", err)
	}
	aliveAddr := ln.Addr().String()
	_, alivePortStr, _ := net.SplitHostPort(aliveAddr)
	_ = ln.Close()

	srv, err := agent.New(agent.Options{
		Listen:   aliveAddr,
		CertFile: aliveCertPath,
		KeyFile:  aliveKeyPath,
		CAFile:   caPath,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitForPort(t, aliveAddr, 2*time.Second)

	// Pick a free port for "dead" but never start anything on it.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dead: %v", err)
	}
	_, deadPortStr, _ := net.SplitHostPort(deadLn.Addr().String())
	_ = deadLn.Close()

	// Both endpoints share an IP but ProbeOptions has a single port, so we
	// run two probes — one against alive, one against dead — to cover mixed
	// reachability without baking port-per-node into the API.
	alivePort, _ := strconv.Atoi(alivePortStr)
	statuses, err := ProbeNodes(context.Background(), ProbeOptions{
		CAPath:         caPath,
		ClientCertPath: clientCertPath,
		ClientKeyPath:  clientKeyPath,
		Port:           alivePort,
		Timeout:        2 * time.Second,
	}, map[string]string{"alive": "127.0.0.1"})
	if err != nil {
		t.Fatalf("probe alive: %v", err)
	}
	if len(statuses) != 1 || !statuses[0].Reachable {
		t.Fatalf("alive: want reachable, got %+v", statuses)
	}
	if statuses[0].Info == nil || statuses[0].Info.AgentVersion != agent.Version {
		t.Errorf("alive info missing or wrong version: %+v", statuses[0].Info)
	}

	deadPort, _ := strconv.Atoi(deadPortStr)
	statuses, err = ProbeNodes(context.Background(), ProbeOptions{
		CAPath:         caPath,
		ClientCertPath: clientCertPath,
		ClientKeyPath:  clientKeyPath,
		Port:           deadPort,
		Timeout:        500 * time.Millisecond,
	}, map[string]string{"dead": "127.0.0.1"})
	if err != nil {
		t.Fatalf("probe dead: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Reachable {
		t.Fatalf("dead: want unreachable, got %+v", statuses)
	}
	if statuses[0].Error == "" {
		t.Errorf("dead: want non-empty Error, got %+v", statuses[0])
	}
}

func TestProbeNodesReturnsErrorOnMissingCerts(t *testing.T) {
	_, err := ProbeNodes(context.Background(), ProbeOptions{
		CAPath:         "/nonexistent/ca.pem",
		ClientCertPath: "/nonexistent/client.pem",
		ClientKeyPath:  "/nonexistent/client.key",
		Port:           9443,
	}, map[string]string{"node1": "127.0.0.1"})
	if err == nil {
		t.Fatal("want error when cert files missing")
	}
}
