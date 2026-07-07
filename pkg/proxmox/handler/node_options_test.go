package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
	"github.com/openctl/openctl/pkg/proxmox/client"
)

type fakeNodeOptsClient struct {
	storages []*client.NodeStorage
	bridges  []*client.NodeBridge
	storeErr error
	brErr    error
}

func (f *fakeNodeOptsClient) ListNodeStorages(_ context.Context, _ string) ([]*client.NodeStorage, error) {
	return f.storages, f.storeErr
}
func (f *fakeNodeOptsClient) ListNodeBridges(_ context.Context, _ string) ([]*client.NodeBridge, error) {
	return f.bridges, f.brErr
}

// enrichNodeOptions adds storage + bridge name lists to a node's status.
func TestEnrichNodeOptions(t *testing.T) {
	res := &protocol.Resource{Status: map[string]any{"state": "online"}}
	c := &fakeNodeOptsClient{
		storages: []*client.NodeStorage{{Storage: "local-lvm"}, {Storage: "nfs"}},
		bridges:  []*client.NodeBridge{{Iface: "vmbr0"}, {Iface: "vmbr1"}},
	}
	enrichNodeOptions(context.Background(), c, "pve1", res)

	st := res.Status
	storages, _ := st["storages"].([]string)
	bridges, _ := st["bridges"].([]string)
	if len(storages) != 2 || storages[0] != "local-lvm" {
		t.Errorf("storages = %v", storages)
	}
	if len(bridges) != 2 || bridges[1] != "vmbr1" {
		t.Errorf("bridges = %v", bridges)
	}
}

// A storage-list failure is best-effort: the node stays observable, and the
// half that succeeded is still populated.
func TestEnrichNodeOptions_BestEffort(t *testing.T) {
	res := &protocol.Resource{Status: map[string]any{}}
	c := &fakeNodeOptsClient{
		storeErr: errors.New("boom"),
		bridges:  []*client.NodeBridge{{Iface: "vmbr0"}},
	}
	enrichNodeOptions(context.Background(), c, "pve1", res)

	if _, ok := res.Status["storages"]; ok {
		t.Error("storages should be absent when the fetch failed")
	}
	if b, _ := res.Status["bridges"].([]string); len(b) != 1 {
		t.Errorf("bridges should still be populated: %v", res.Status["bridges"])
	}
}

// A nil status is tolerated (no panic).
func TestEnrichNodeOptions_NilStatus(t *testing.T) {
	res := &protocol.Resource{}
	enrichNodeOptions(context.Background(), &fakeNodeOptsClient{}, "pve1", res)
}
