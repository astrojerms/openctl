package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestGetChildrenGraphSpansCrossLayer proves the unified cross-layer graph: a
// HelmRelease whose kubeconfigPath $refs a Cluster, queried as the root, must
// span down through the Cluster's Plan expansion (VMs/Nodes) in one graph —
// via (a) root-spec $ref collection and (b) multi-level recursion.
func TestGetChildrenGraphSpansCrossLayer(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer db.Close()
	store := manifests.New(db)

	// Seed the workload manifest with a root-spec $ref to the Cluster.
	hr := &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: "HelmRelease"}
	hr.Metadata.Name = "app"
	hr.Spec = map[string]any{
		"kubeconfigPath": ref("fake.openctl.io/v1", "Cluster", "c", "status.outputs.kubeconfigPath"),
		"chart":          map[string]any{"name": "podinfo"},
	}
	if err := store.Save(ctx, hr); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	fp := &fakePlanner{fakeProvider: &fakeProvider{}, present: map[string]bool{}}
	reg := providers.NewRegistry()
	reg.Register(fp)
	h := newResourceHandler(reg, nil, nil, store)

	resp, err := h.GetChildrenGraph(ctx, &apiv1.GetChildrenGraphRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "HelmRelease", Name: "app",
	})
	if err != nil {
		t.Fatalf("GetChildrenGraph: %v", err)
	}
	edges := resp.GetEdges()

	// The workload's own $ref reaches the Cluster (root-spec collection).
	if !hasEdge(edges, "HelmRelease/app", "Cluster/c", "ref", "status.outputs.kubeconfigPath") {
		t.Errorf("missing HelmRelease→Cluster ref edge; edges: %+v", edges)
	}
	// The Cluster expands (recursion into the ref target's Planner).
	if !hasEdge(edges, "Cluster/c", "VM/vm-0", "owns", "") {
		t.Errorf("missing Cluster→VM owns edge; edges: %+v", edges)
	}
	// A deep sibling ref inside the cluster expansion is present.
	if !hasEdge(edges, "Node/node-0", "VM/vm-0", "ref", "") {
		t.Errorf("missing Node→VM ref edge; edges: %+v", edges)
	}
	// Root + Cluster + 2 VMs + 2 Nodes = 6 nodes across three layers.
	if got := len(resp.GetNodes()); got != 6 {
		t.Fatalf("node count = %d, want 6: %+v", got, resp.GetNodes())
	}
	if root := nodeByID(resp.GetNodes(), "HelmRelease/app"); root == nil || !root.GetRoot() {
		t.Errorf("HelmRelease should be the root: %+v", root)
	}
}

// fakePlanner is a fakeProvider that implements providers.Planner, emitting a
// small cluster-shaped expansion: two VMs and two Nodes, where the second
// Node $ref-joins the first and points at its own VM. Its Get reports vm-0 /
// node-0 as present and vm-1 / node-1 as not-yet-created so the status pills
// exercise both "applied" and "pending".
type fakePlanner struct {
	*fakeProvider
	present map[string]bool // "kind/name" → live resource exists
}

func ref(apiVersion, kind, name, field string) map[string]any {
	inner := map[string]any{"apiVersion": apiVersion, "kind": kind, "name": name}
	if field != "" {
		inner["field"] = field
	}
	return map[string]any{"$ref": inner}
}

func (f *fakePlanner) Plan(_ context.Context, m *protocol.Resource) (*providers.PlanResult, error) {
	// Only the Cluster composes (like the real k3s provider); other kinds are
	// leaves, so the graph BFS doesn't re-expand VMs/Nodes into a fresh cluster.
	if m.Kind != "Cluster" {
		return nil, providers.NotFound(m.Kind, m.Metadata.Name)
	}
	av := "fake.openctl.io/v1"
	child := func(kind, name string, spec map[string]any) *protocol.Resource {
		return &protocol.Resource{APIVersion: av, Kind: kind, Metadata: protocol.ResourceMetadata{Name: name}, Spec: spec}
	}
	return &providers.PlanResult{Children: []*protocol.Resource{
		child("VM", "vm-0", nil),
		child("VM", "vm-1", nil),
		child("Node", "node-0", map[string]any{
			"vmRef": ref(av, "VM", "vm-0", ""),
		}),
		child("Node", "node-1", map[string]any{
			"vmRef":    ref(av, "VM", "vm-1", ""),
			"joinFrom": ref(av, "Node", "node-0", "status.token"),
		}),
	}}, nil
}

func (f *fakePlanner) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	if f.present[kind+"/"+name] {
		return &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: kind, Metadata: protocol.ResourceMetadata{Name: name}}, nil
	}
	return nil, providers.NotFound(kind, name)
}

// TestGetChildrenGraphSurfacesVMHostPlacement proves a VirtualMachine's plain
// spec.node string surfaces as a graph edge to its ProxmoxNode host, and that
// the host is a terminal node — added but not expanded, so a workload-rooted
// graph doesn't fan out into every other guest on the box.
func TestGetChildrenGraphSurfacesVMHostPlacement(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer db.Close()
	store := manifests.New(db)

	// A VM manifest pinned to a host via a plain spec.node string (not a $ref).
	vm := &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: "VirtualMachine"}
	vm.Metadata.Name = "vm-0"
	vm.Spec = map[string]any{"node": "pve1351", "cpu": map[string]any{"cores": 2}}
	if err := store.Save(ctx, vm); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	// fakePlanner.Plan returns NotFound for non-Cluster kinds, so the VM is a
	// leaf structurally — its only outgoing edge is the synthesized placement one.
	fp := &fakePlanner{fakeProvider: &fakeProvider{}, present: map[string]bool{}}
	reg := providers.NewRegistry()
	reg.Register(fp)
	h := newResourceHandler(reg, nil, nil, store)

	resp, err := h.GetChildrenGraph(ctx, &apiv1.GetChildrenGraphRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "VirtualMachine", Name: "vm-0",
	})
	if err != nil {
		t.Fatalf("GetChildrenGraph: %v", err)
	}
	if !hasEdge(resp.GetEdges(), "VirtualMachine/vm-0", "ProxmoxNode/pve1351", "ref", "node") {
		t.Errorf("missing VM→host placement edge; edges: %+v", resp.GetEdges())
	}
	// Root VM + host = 2 nodes; the host is terminal (no sibling fan-out).
	if got := len(resp.GetNodes()); got != 2 {
		t.Fatalf("node count = %d, want 2: %+v", got, resp.GetNodes())
	}
	// The host is observed-only infra: unmanaged, dimmed in the UI.
	host := nodeByID(resp.GetNodes(), "ProxmoxNode/pve1351")
	if host == nil {
		t.Fatalf("missing ProxmoxNode host node")
	}
	if host.GetManaged() || host.GetRoot() {
		t.Errorf("host should be unmanaged and non-root: %+v", host)
	}
}

// TestGetChildrenGraphVMWithoutNodeHasNoPlacementEdge guards the guard: a VM
// with no spec.node (provider-default placement) draws no placement edge.
func TestGetChildrenGraphVMWithoutNodeHasNoPlacementEdge(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer db.Close()
	store := manifests.New(db)

	vm := &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: "VirtualMachine"}
	vm.Metadata.Name = "vm-0"
	vm.Spec = map[string]any{"cpu": map[string]any{"cores": 2}} // no node
	if err := store.Save(ctx, vm); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	fp := &fakePlanner{fakeProvider: &fakeProvider{}, present: map[string]bool{}}
	reg := providers.NewRegistry()
	reg.Register(fp)
	h := newResourceHandler(reg, nil, nil, store)

	resp, err := h.GetChildrenGraph(ctx, &apiv1.GetChildrenGraphRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "VirtualMachine", Name: "vm-0",
	})
	if err != nil {
		t.Fatalf("GetChildrenGraph: %v", err)
	}
	if got := len(resp.GetNodes()); got != 1 {
		t.Fatalf("VM with no node should be root-only, got %d nodes: %+v", got, resp.GetNodes())
	}
}

func nodeByID(nodes []*apiv1.GraphNode, id string) *apiv1.GraphNode {
	for _, n := range nodes {
		if n.GetId() == id {
			return n
		}
	}
	return nil
}

func hasEdge(edges []*apiv1.GraphEdge, from, to, relation, field string) bool {
	for _, e := range edges {
		if e.GetFrom() == from && e.GetTo() == to && e.GetRelation() == relation && e.GetField() == field {
			return true
		}
	}
	return false
}

func TestGetChildrenGraphPlannerExpandsNodesAndEdges(t *testing.T) {
	fp := &fakePlanner{
		fakeProvider: &fakeProvider{},
		present:      map[string]bool{"VM/vm-0": true, "Node/node-0": true},
	}
	reg := providers.NewRegistry()
	reg.Register(fp)
	h := newResourceHandler(reg, nil, nil, nil)

	resp, err := h.GetChildrenGraph(context.Background(), &apiv1.GetChildrenGraphRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "Cluster", Name: "c",
	})
	if err != nil {
		t.Fatalf("GetChildrenGraph: %v", err)
	}

	// Root + 4 children, deduplicated (ref targets coincide with planned nodes).
	if got := len(resp.GetNodes()); got != 5 {
		t.Fatalf("node count = %d, want 5: %+v", got, resp.GetNodes())
	}

	root := nodeByID(resp.GetNodes(), "Cluster/c")
	if root == nil || !root.GetRoot() || !root.GetManaged() || root.GetStatus() != "applied" {
		t.Fatalf("root node wrong: %+v", root)
	}

	// Planner children are all managed regardless of applied-manifest presence.
	for _, id := range []string{"VM/vm-0", "VM/vm-1", "Node/node-0", "Node/node-1"} {
		n := nodeByID(resp.GetNodes(), id)
		if n == nil {
			t.Fatalf("missing node %s", id)
		}
		if !n.GetManaged() {
			t.Errorf("node %s should be managed", id)
		}
		if n.GetRoot() {
			t.Errorf("node %s should not be root", id)
		}
	}

	// Status from the live Get: present → applied, absent → pending.
	wantStatus := map[string]string{"VM/vm-0": "applied", "VM/vm-1": "pending", "Node/node-0": "applied", "Node/node-1": "pending"}
	for id, want := range wantStatus {
		if got := nodeByID(resp.GetNodes(), id).GetStatus(); got != want {
			t.Errorf("node %s status = %q, want %q", id, got, want)
		}
	}

	// Owns edges: root → each direct child.
	for _, id := range []string{"VM/vm-0", "VM/vm-1", "Node/node-0", "Node/node-1"} {
		if !hasEdge(resp.GetEdges(), "Cluster/c", id, "owns", "") {
			t.Errorf("missing owns edge Cluster/c → %s", id)
		}
	}
	// Ref edges: node-0 → its VM (no field), node-1 → its VM + join to node-0.
	if !hasEdge(resp.GetEdges(), "Node/node-0", "VM/vm-0", "ref", "") {
		t.Errorf("missing ref edge node-0 → vm-0")
	}
	if !hasEdge(resp.GetEdges(), "Node/node-1", "VM/vm-1", "ref", "") {
		t.Errorf("missing ref edge node-1 → vm-1")
	}
	if !hasEdge(resp.GetEdges(), "Node/node-1", "Node/node-0", "ref", "status.token") {
		t.Errorf("missing ref edge node-1 → node-0 (status.token)")
	}
}

// fakeComposite implements ChildrenLister but not Planner — the U9.4 fallback:
// owns edges only, no ref metadata, managed-ness from applied-manifest
// presence (none here, so children come back dim/observed).
type fakeComposite struct {
	*fakeProvider
}

func (f *fakeComposite) ChildrenOf(kind, name string) []providers.ResourceRef {
	if kind != "Host" {
		return nil
	}
	return []providers.ResourceRef{
		{APIVersion: "fake.openctl.io/v1", Kind: "VM", Name: "guest-a"},
		{APIVersion: "fake.openctl.io/v1", Kind: "VM", Name: "guest-b"},
	}
}

func TestGetChildrenGraphFallbackObservedChildrenAreUnmanaged(t *testing.T) {
	fc := &fakeComposite{fakeProvider: &fakeProvider{getErr: providers.NotFound("VM", "x")}}
	reg := providers.NewRegistry()
	reg.Register(fc)
	h := newResourceHandler(reg, nil, nil, nil)

	resp, err := h.GetChildrenGraph(context.Background(), &apiv1.GetChildrenGraphRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "Host", Name: "hv1",
	})
	if err != nil {
		t.Fatalf("GetChildrenGraph: %v", err)
	}
	if got := len(resp.GetNodes()); got != 3 {
		t.Fatalf("node count = %d, want 3", got)
	}
	for _, id := range []string{"VM/guest-a", "VM/guest-b"} {
		n := nodeByID(resp.GetNodes(), id)
		if n == nil {
			t.Fatalf("missing node %s", id)
		}
		// No applied manifest and not planned → unmanaged / observed (dim in UI).
		if n.GetManaged() {
			t.Errorf("node %s should be unmanaged (no applied manifest)", id)
		}
		if n.GetStatus() != "observed" {
			t.Errorf("node %s status = %q, want observed", id, n.GetStatus())
		}
		if !hasEdge(resp.GetEdges(), "Host/hv1", id, "owns", "") {
			t.Errorf("missing owns edge Host/hv1 → %s", id)
		}
	}
	// Fallback path draws no ref edges.
	for _, e := range resp.GetEdges() {
		if e.GetRelation() == "ref" {
			t.Errorf("fallback path should have no ref edges, got %+v", e)
		}
	}
}

func TestGetChildrenGraphAtomicResourceIsRootOnly(t *testing.T) {
	fp := &fakeProvider{getReturn: &protocol.Resource{}}
	reg := providers.NewRegistry()
	reg.Register(fp)
	h := newResourceHandler(reg, nil, nil, nil)

	resp, err := h.GetChildrenGraph(context.Background(), &apiv1.GetChildrenGraphRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "solo",
	})
	if err != nil {
		t.Fatalf("GetChildrenGraph: %v", err)
	}
	if len(resp.GetNodes()) != 1 || len(resp.GetEdges()) != 0 {
		t.Fatalf("atomic resource should yield 1 node / 0 edges, got %d nodes / %d edges", len(resp.GetNodes()), len(resp.GetEdges()))
	}
	if !resp.GetNodes()[0].GetRoot() {
		t.Errorf("sole node should be root")
	}
}

func TestGetChildrenGraphValidatesRequest(t *testing.T) {
	reg := providers.NewRegistry()
	reg.Register(&fakeProvider{})
	h := newResourceHandler(reg, nil, nil, nil)
	for _, req := range []*apiv1.GetChildrenGraphRequest{
		{Kind: "Cluster", Name: "c"},
		{ApiVersion: "fake.openctl.io/v1", Name: "c"},
		{ApiVersion: "fake.openctl.io/v1", Kind: "Cluster"},
	} {
		if _, err := h.GetChildrenGraph(context.Background(), req); err == nil {
			t.Errorf("expected InvalidArgument for %+v", req)
		}
	}
}
