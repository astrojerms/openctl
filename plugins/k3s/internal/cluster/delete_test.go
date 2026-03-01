package cluster

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl-k3s/internal/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestNewDeleter(t *testing.T) {
	spec := &resources.ClusterSpec{}
	deleter := NewDeleter("test-cluster", spec)

	if deleter.name != "test-cluster" {
		t.Errorf("expected name=test-cluster, got %s", deleter.name)
	}
	if deleter.spec != spec {
		t.Error("expected spec to match")
	}
}

func TestDeleter_GenerateDispatchRequests(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 1},
			Workers: []resources.WorkerSpec{
				{Name: "worker", Count: 2},
			},
		},
	}

	deleter := NewDeleter("test", spec)
	requests := deleter.GenerateDispatchRequests()

	if len(requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(requests))
	}

	// Check control plane deletion
	if requests[0].ID != "vm-test-cp-0" {
		t.Errorf("expected ID=vm-test-cp-0, got %s", requests[0].ID)
	}
	if requests[0].Provider != "proxmox" {
		t.Errorf("expected provider=proxmox, got %s", requests[0].Provider)
	}
	if requests[0].Action != protocol.ActionDelete {
		t.Errorf("expected action=delete, got %s", requests[0].Action)
	}
	if requests[0].ResourceType != "VirtualMachine" {
		t.Errorf("expected resourceType=VirtualMachine, got %s", requests[0].ResourceType)
	}
	if requests[0].ResourceName != "test-cp-0" {
		t.Errorf("expected resourceName=test-cp-0, got %s", requests[0].ResourceName)
	}

	// Check worker deletions
	if requests[1].ID != "vm-test-worker-0" {
		t.Errorf("expected ID=vm-test-worker-0, got %s", requests[1].ID)
	}
	if requests[1].ResourceName != "test-worker-0" {
		t.Errorf("expected resourceName=test-worker-0, got %s", requests[1].ResourceName)
	}

	if requests[2].ID != "vm-test-worker-1" {
		t.Errorf("expected ID=vm-test-worker-1, got %s", requests[2].ID)
	}
	if requests[2].ResourceName != "test-worker-1" {
		t.Errorf("expected resourceName=test-worker-1, got %s", requests[2].ResourceName)
	}
}

func TestDeleter_GenerateDispatchRequests_HACluster(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 3},
			Workers: []resources.WorkerSpec{
				{Name: "general", Count: 3},
				{Name: "gpu", Count: 2},
			},
		},
	}

	deleter := NewDeleter("prod", spec)
	requests := deleter.GenerateDispatchRequests()

	// 3 CP + 3 general + 2 gpu = 8 total
	if len(requests) != 8 {
		t.Fatalf("expected 8 requests, got %d", len(requests))
	}

	// Verify all are delete actions
	for _, req := range requests {
		if req.Action != protocol.ActionDelete {
			t.Errorf("expected all actions to be delete, got %s for %s", req.Action, req.ID)
		}
	}
}

func TestValidateResults_AllSuccess(t *testing.T) {
	deleter := NewDeleter("test", &resources.ClusterSpec{})

	results := []*protocol.DispatchResult{
		{ID: "vm-test-cp-0", Status: protocol.StatusSuccess},
		{ID: "vm-test-worker-0", Status: protocol.StatusSuccess},
	}

	errors := deleter.ValidateResults(results)
	if len(errors) != 0 {
		t.Errorf("expected no errors, got %v", errors)
	}
}

func TestValidateResults_NotFoundIsOK(t *testing.T) {
	deleter := NewDeleter("test", &resources.ClusterSpec{})

	results := []*protocol.DispatchResult{
		{ID: "vm-test-cp-0", Status: protocol.StatusSuccess},
		{
			ID:     "vm-test-worker-0",
			Status: protocol.StatusError,
			Error:  &protocol.Error{Code: protocol.ErrorCodeNotFound, Message: "VM not found"},
		},
	}

	errors := deleter.ValidateResults(results)
	if len(errors) != 0 {
		t.Errorf("expected NOT_FOUND to be ignored, got %v", errors)
	}
}

func TestValidateResults_RealError(t *testing.T) {
	deleter := NewDeleter("test", &resources.ClusterSpec{})

	results := []*protocol.DispatchResult{
		{ID: "vm-test-cp-0", Status: protocol.StatusSuccess},
		{
			ID:     "vm-test-worker-0",
			Status: protocol.StatusError,
			Error:  &protocol.Error{Code: protocol.ErrorCodeInternal, Message: "Permission denied"},
		},
	}

	errors := deleter.ValidateResults(results)
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	if errors[0] != "vm-test-worker-0: Permission denied" {
		t.Errorf("unexpected error format: %s", errors[0])
	}
}

func TestValidateResults_MultipleErrors(t *testing.T) {
	deleter := NewDeleter("test", &resources.ClusterSpec{})

	results := []*protocol.DispatchResult{
		{
			ID:     "vm-test-cp-0",
			Status: protocol.StatusError,
			Error:  &protocol.Error{Code: protocol.ErrorCodeInternal, Message: "Error 1"},
		},
		{ID: "vm-test-cp-1", Status: protocol.StatusSuccess},
		{
			ID:     "vm-test-worker-0",
			Status: protocol.StatusError,
			Error:  &protocol.Error{Code: protocol.ErrorCodeInternal, Message: "Error 2"},
		},
	}

	errors := deleter.ValidateResults(results)
	if len(errors) != 2 {
		t.Fatalf("expected 2 errors, got %d", len(errors))
	}
}

func TestCleanup(t *testing.T) {
	// Create a temporary cluster directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home directory")
	}

	clusterDir := filepath.Join(homeDir, ".openctl", "k3s", "test-cleanup-cluster")
	if err := os.MkdirAll(clusterDir, 0700); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}

	// Create a test file
	testFile := filepath.Join(clusterDir, "kubeconfig")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Verify it exists
	if _, err := os.Stat(clusterDir); os.IsNotExist(err) {
		t.Fatal("test directory should exist")
	}

	// Run cleanup
	deleter := NewDeleter("test-cleanup-cluster", &resources.ClusterSpec{})
	if err := deleter.Cleanup(); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(clusterDir); !os.IsNotExist(err) {
		t.Error("expected cluster directory to be removed")
	}
}

func TestCleanup_NonexistentDir(t *testing.T) {
	// Cleanup should not error if directory doesn't exist
	deleter := NewDeleter("nonexistent-cluster-12345", &resources.ClusterSpec{})
	err := deleter.Cleanup()
	if err != nil {
		t.Errorf("Cleanup should not error for nonexistent directory: %v", err)
	}
}
