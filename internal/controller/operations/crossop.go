package operations

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
)

// Cross-op dependency scheduling (opt-in, flag-gated).
//
// The composite-apply dependency DAG (see operations.RunGraph / RefChildEdges)
// orders the children *within* one operation. This hoists the same idea one
// level up: when OPENCTL_CROSS_OP_SCHEDULING is set, the dispatcher processes a
// whole batch of pending operations as a graph — running independent ops
// concurrently and ordering dependent ones by their `$ref` edges — instead of
// the default one-at-a-time FIFO drain.
//
// Design + the decisions this reopens are written up in
// docs/cross-op-scheduling.md. This ships the opt-in machinery only; the flag
// defaults off, so the locked single-goroutine / fail-fast-collision behavior
// is unchanged unless an operator explicitly turns it on.

// crossOpSchedulingEnabled reports whether the dispatcher should schedule
// pending operations as a batch graph rather than draining them FIFO. Off by
// default.
func crossOpSchedulingEnabled() bool {
	switch os.Getenv("OPENCTL_CROSS_OP_SCHEDULING") {
	case "1", "true", "TRUE", "yes":
		return true
	}
	return false
}

// crossOpConcurrency bounds how many independent operations run at once under
// cross-op scheduling. Defaults to a small fan-out; OPENCTL_CROSS_OP_CONCURRENCY
// overrides.
func crossOpConcurrency() int {
	if v := os.Getenv("OPENCTL_CROSS_OP_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}

// crossOpEdges derives dependency edges over a batch of operations from the
// `$ref`s in each Apply op's manifest. An op depends on another op in the same
// batch when its spec references the resource that op applies. Refs whose
// target is not being applied by a concurrent op in this batch produce no edge
// — they either already exist (and resolve lazily at apply time, as today) or
// are genuinely absent (and fail resolution, as today). The result maps an
// op ID to the sorted, deduped IDs it depends on.
//
// This is the top-level analog of RefChildEdges, keyed by op ID and matching
// refs on the full (apiVersion, kind, name) triple so distinct providers with
// the same Kind don't collide.
func crossOpEdges(ops []*Operation) (map[string][]string, error) {
	applier := make(map[string]string) // resource key -> op ID that applies it
	specs := make(map[string]map[string]any)
	for _, op := range ops {
		if op.Type != TypeApply || op.ManifestJSON == "" {
			continue
		}
		var r protocol.Resource
		if err := json.Unmarshal([]byte(op.ManifestJSON), &r); err != nil {
			return nil, fmt.Errorf("op %s: decode manifest: %w", op.ID, err)
		}
		applier[resourceKey(r.APIVersion, r.Kind, r.Metadata.Name)] = op.ID
		if r.Spec != nil {
			specs[op.ID] = r.Spec
		}
	}

	edges := make(map[string][]string)
	for opID, spec := range specs {
		seen := make(map[string]bool)
		var deps []string
		for _, ref := range refs.Collect(spec) {
			depOp, ok := applier[resourceKey(ref.APIVersion, ref.Kind, ref.Name)]
			if !ok || depOp == opID || seen[depOp] {
				continue
			}
			seen[depOp] = true
			deps = append(deps, depOp)
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			edges[opID] = deps
		}
	}
	return edges, nil
}

func resourceKey(apiVersion, kind, name string) string {
	return apiVersion + "|" + kind + "|" + name
}

// crossOpAcyclic reports whether the dependency graph over ids with the given
// edges (id -> ids it depends on) is acyclic. A `$ref` cycle between two ops is
// a user error; the dispatcher falls back to unordered scheduling when this is
// false so cyclic ops still run (and fail naturally at ref resolution) rather
// than being left claimed-but-unrun.
func crossOpAcyclic(ids []string, edges map[string][]string) bool {
	indegree := make(map[string]int, len(ids))
	for _, id := range ids {
		indegree[id] = len(edges[id])
	}
	dependents := make(map[string][]string)
	for id, deps := range edges {
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], id)
		}
	}
	queue := make([]string, 0, len(ids))
	for _, id := range ids {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	processed := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		processed++
		for _, d := range dependents[n] {
			indegree[d]--
			if indegree[d] == 0 {
				queue = append(queue, d)
			}
		}
	}
	return processed == len(ids)
}
