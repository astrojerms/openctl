package k3s

import (
	"slices"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
)

func TestDryRunFreshClusterEmitsCreatesForEveryNodeWithoutGates(t *testing.T) {
	spec := newSpec(1, workerPool{"worker", 2})
	res := dryRunFreshCluster("dev", spec)

	if len(res.Children) != 3 {
		t.Fatalf("children = %d, want 3 (1 cp + 2 workers)", len(res.Children))
	}
	for _, c := range res.Children {
		if c.Verb != "create" {
			t.Errorf("child %s verb = %q, want create", c.Name, c.Verb)
		}
	}
	if len(res.RequiredGates) != 0 {
		t.Errorf("fresh cluster should require no gates, got %v", res.RequiredGates)
	}
	if res.Summary == "" {
		t.Error("expected human-readable summary")
	}
}

func TestSummarisePlanFormatsVerbCounts(t *testing.T) {
	cases := []struct {
		name    string
		plan    *changePlan
		respecs int
		want    string
	}{
		{
			name: "scale-down",
			plan: &changePlan{removeWorkers: []string{"dev-worker-1"}},
			want: "would remove 1 node(s)",
		},
		{
			name: "scale-up",
			plan: &changePlan{addWorkers: []string{"dev-worker-2", "dev-worker-3"}},
			want: "would add 2 node(s)",
		},
		{
			name:    "respec-only",
			plan:    &changePlan{},
			respecs: 1,
			want:    "would respec 1 node(s)",
		},
		{
			name:    "mixed",
			plan:    &changePlan{addWorkers: []string{"dev-worker-2"}, removeWorkers: []string{"dev-worker-0"}},
			respecs: 1,
			want:    "would add 1, remove 1, respec 1 node(s)",
		},
		{
			name: "no-op",
			plan: &changePlan{},
			want: "no-op",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarisePlan(tc.plan, tc.respecs); got != tc.want {
				t.Errorf("summarisePlan = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAppendUniqueDoesntDuplicateGate(t *testing.T) {
	gates := appendUnique(nil, providers.GateAllowDestructive)
	gates = appendUnique(gates, providers.GateAllowDestructive)
	gates = appendUnique(gates, providers.GateIKnowThisBreaks)
	if len(gates) != 2 {
		t.Fatalf("len = %d, want 2 distinct gates", len(gates))
	}
	if !slices.Contains(gates, providers.GateAllowDestructive) ||
		!slices.Contains(gates, providers.GateIKnowThisBreaks) {
		t.Errorf("gates = %v, want both gates present", gates)
	}
}
