package operations

import (
	"sort"

	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
)

// ChildKey is the stable graph-node identity for a plan child: "Kind/Name".
// Providers use it to key Tasks and to name barrier dependencies so they line
// up with the edges RefChildEdges derives.
func ChildKey(r *protocol.Resource) string {
	return r.Kind + "/" + r.Metadata.Name
}

// RefChildEdges derives the dependency edges among a set of plan children from
// the $refs in their specs: if child B's spec references child A, then B
// depends on A (A must be applied first). The returned map is keyed by
// ChildKey and lists the ChildKeys each child depends on, sorted and
// de-duplicated.
//
// Refs to resources NOT in the child set are ignored — those resolve against
// live external state at apply time and impose no intra-plan ordering. A ref
// to the child itself is ignored. This turns the $ref graph the resolver
// already understands into the execution order, so ordering is data-driven
// rather than hand-coded.
func RefChildEdges(children []*protocol.Resource) map[string][]string {
	present := make(map[string]bool, len(children))
	for _, c := range children {
		present[ChildKey(c)] = true
	}

	edges := make(map[string][]string, len(children))
	for _, c := range children {
		key := ChildKey(c)
		seen := make(map[string]bool)
		var deps []string
		for _, ref := range refs.Collect(c.Spec) {
			dep := ref.Kind + "/" + ref.Name
			if dep == key || !present[dep] || seen[dep] {
				continue
			}
			seen[dep] = true
			deps = append(deps, dep)
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			edges[key] = deps
		}
	}
	return edges
}
