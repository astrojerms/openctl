// Package plan computes a batch apply plan over a set of manifests: the
// cross-resource $ref dependency graph, the topological apply order (as waves
// of independently-appliable resources), and references to resources outside
// the set (which must already exist). It is pure and offline — no controller
// connection, no provider calls — so `openctl plan` can preview ordering and
// "what waits on what" before any apply.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
)

// key is the stable identity of a resource in a plan: the full
// (apiVersion, kind, name) triple, so two providers sharing a Kind+Name don't
// collide.
func key(apiVersion, kind, name string) string {
	return apiVersion + "|" + kind + "|" + name
}

// Node is one resource in the plan plus its resolved dependencies.
type Node struct {
	Resource *protocol.Resource
	// Deps are the display labels ("Kind/Name") of in-set resources this one
	// $refs — they must apply first. Sorted, de-duplicated.
	Deps []string
	// External are display labels for $ref targets NOT in the set — they must
	// already exist when this resource applies. Sorted, de-duplicated.
	External []string
}

// Display is the human label for a node ("Kind/Name").
func (n *Node) Display() string {
	return n.Resource.Kind + "/" + n.Resource.Metadata.Name
}

// Plan is the computed batch plan.
type Plan struct {
	// Waves are the topological levels: every resource in wave i depends only
	// on resources in earlier waves, so a wave's resources can apply
	// concurrently. Within a wave, nodes are sorted by display label.
	Waves [][]*Node
}

// Count returns the total number of planned resources.
func (p *Plan) Count() int {
	n := 0
	for _, w := range p.Waves {
		n += len(w)
	}
	return n
}

// Build computes the plan from a set of manifests. Returns an error on a
// duplicate resource identity or a dependency cycle (naming the resources
// stuck in the cycle).
func Build(resources []*protocol.Resource) (*Plan, error) {
	nodes := make(map[string]*Node, len(resources))
	deps := make(map[string][]string, len(resources)) // key -> dependency keys (in-set)
	order := make([]string, 0, len(resources))        // input order for stable iteration
	for _, r := range resources {
		if r == nil {
			continue
		}
		k := key(r.APIVersion, r.Kind, r.Metadata.Name)
		if _, dup := nodes[k]; dup {
			return nil, fmt.Errorf("duplicate resource %s/%s (%s)", r.Kind, r.Metadata.Name, r.APIVersion)
		}
		nodes[k] = &Node{Resource: r}
		order = append(order, k)
	}

	// Derive edges from the $ref markers in each spec.
	for _, k := range order {
		n := nodes[k]
		seenDep := map[string]bool{}
		seenExt := map[string]bool{}
		for _, ref := range refs.Collect(n.Resource.Spec) {
			depKey := key(ref.APIVersion, ref.Kind, ref.Name)
			if depKey == k {
				continue // a resource referencing itself is not a dependency
			}
			if dep, ok := nodes[depKey]; ok {
				if !seenDep[depKey] {
					seenDep[depKey] = true
					deps[k] = append(deps[k], depKey)
					n.Deps = append(n.Deps, dep.Display())
				}
			} else {
				disp := ref.Kind + "/" + ref.Name
				if !seenExt[disp] {
					seenExt[disp] = true
					n.External = append(n.External, disp)
				}
			}
		}
		sort.Strings(n.Deps)
		sort.Strings(n.External)
	}

	waves, err := topoWaves(nodes, deps)
	if err != nil {
		return nil, err
	}
	return &Plan{Waves: waves}, nil
}

// topoWaves runs Kahn's algorithm level-by-level: each wave is the set of nodes
// whose dependencies are all satisfied by earlier waves. Within a wave, nodes
// are sorted by display label for deterministic output. A remaining node with
// nonzero in-degree means a cycle.
func topoWaves(nodes map[string]*Node, deps map[string][]string) ([][]*Node, error) {
	indegree := make(map[string]int, len(nodes))
	dependents := make(map[string][]string)
	for k := range nodes {
		indegree[k] = len(deps[k])
		for _, dep := range deps[k] {
			dependents[dep] = append(dependents[dep], k)
		}
	}

	var frontier []string
	for k := range nodes {
		if indegree[k] == 0 {
			frontier = append(frontier, k)
		}
	}

	var waves [][]*Node
	remaining := len(nodes)
	for len(frontier) > 0 {
		wave := make([]*Node, 0, len(frontier))
		for _, k := range frontier {
			wave = append(wave, nodes[k])
		}
		sort.Slice(wave, func(i, j int) bool { return wave[i].Display() < wave[j].Display() })
		remaining -= len(wave)

		var next []string
		sort.Strings(frontier) // deterministic `next` ordering
		for _, k := range frontier {
			for _, d := range dependents[k] {
				indegree[d]--
				if indegree[d] == 0 {
					next = append(next, d)
				}
			}
		}
		waves = append(waves, wave)
		frontier = next
	}

	if remaining > 0 {
		stuck := make([]string, 0, remaining)
		for k, d := range indegree {
			if d > 0 {
				stuck = append(stuck, nodes[k].Display())
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("dependency cycle among: %s", strings.Join(stuck, ", "))
	}
	return waves, nil
}
