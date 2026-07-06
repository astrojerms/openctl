package k3s

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/protocol"
)

// These tests pin the composite-provider Plan() contract for the k3s Cluster —
// the invariants a composite provider must uphold so the controller's
// composite-apply dependency DAG (operations.RunGraph over RefChildEdges) can
// schedule its children safely. They are the composite analog of the atomic
// providertest.Suite (which targets CRUD providers; a Plan-based composite is
// out of that battery's scope). See ROADMAP.

// The child `$ref` graph a Cluster's Plan emits must be acyclic and reference
// only children that exist in the plan — otherwise operations.RunGraph (which
// the dispatcher runs over exactly these edges) would deadlock or error at
// apply time. Assert it directly by feeding the plan's own children through
// the same RefChildEdges + RunGraph the dispatcher uses; a no-op RunGraph
// returns nil iff the graph is acyclic with all deps resolvable.
func TestPlanChildGraphIsSchedulable(t *testing.T) {
	cases := []struct {
		name     string
		manifest *protocol.Resource
	}{
		{"single-cp", clusterManifest("dev")},
		{"three-cp-ha", clusterManifest("ha", func(r *protocol.Resource) {
			nodes := r.Spec["nodes"].(map[string]any)
			nodes["controlPlane"] = map[string]any{"count": float64(3)}
		})},
		{"cp-plus-workers", clusterManifest("mixed", func(r *protocol.Resource) {
			nodes := r.Spec["nodes"].(map[string]any)
			nodes["workers"] = []any{map[string]any{"count": float64(2)}}
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			children := planFor(t, tc.manifest)
			if len(children) == 0 {
				t.Fatal("Plan produced no children")
			}
			edges := operations.RefChildEdges(children)

			tasks := make([]operations.Task, len(children))
			for i, c := range children {
				tasks[i] = operations.Task{
					ID:        operations.ChildKey(c),
					DependsOn: edges[operations.ChildKey(c)],
					Run:       nil, // no-op: we only exercise the graph's validity
				}
			}
			if err := operations.RunGraph(context.Background(), 1, tasks); err != nil {
				t.Fatalf("child $ref graph is not schedulable: %v", err)
			}
		})
	}
}

// Plan must be a pure function of its input: two calls with the same manifest
// produce the same children (same set, same specs). Reconciliation, drift
// surfacing, and the verifying-trace cache all assume a stable plan.
func TestPlanIsDeterministic(t *testing.T) {
	manifest := clusterManifest("ha", func(r *protocol.Resource) {
		nodes := r.Spec["nodes"].(map[string]any)
		nodes["controlPlane"] = map[string]any{"count": float64(3)}
		nodes["workers"] = []any{map[string]any{"count": float64(2)}}
	})

	first := planFor(t, manifest)
	second := planFor(t, manifest)

	if len(first) != len(second) {
		t.Fatalf("child count differs across Plan calls: %d then %d", len(first), len(second))
	}
	// Compare as name-keyed JSON so ordering differences (if any) don't cause a
	// false negative — determinism is about the child set and their specs, not
	// slice order.
	firstByKey := marshalByChildKey(t, first)
	secondByKey := marshalByChildKey(t, second)
	for key, a := range firstByKey {
		b, ok := secondByKey[key]
		if !ok {
			t.Errorf("child %s present in first Plan but not second", key)
			continue
		}
		if a != b {
			t.Errorf("child %s differs across Plan calls:\n first:  %s\n second: %s", key, a, b)
		}
	}
	for key := range secondByKey {
		if _, ok := firstByKey[key]; !ok {
			t.Errorf("child %s present in second Plan but not first", key)
		}
	}
}

func marshalByChildKey(t *testing.T, children []*protocol.Resource) map[string]string {
	t.Helper()
	out := make(map[string]string, len(children))
	for _, c := range children {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal child %s: %v", operations.ChildKey(c), err)
		}
		out[operations.ChildKey(c)] = string(b)
	}
	return out
}
