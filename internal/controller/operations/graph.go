package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Task is a node in a dependency graph: a unit of work with a stable ID and
// the IDs of the tasks that must succeed before it runs. Run may be nil for a
// pure synchronization point (a barrier that only orders other tasks).
type Task struct {
	ID        string
	DependsOn []string
	Run       func(ctx context.Context) error
}

// RunGraph executes tasks in dependency order — every task runs only after
// all of its DependsOn have completed successfully — with cycle detection.
//
// concurrency bounds how many tasks run at once; 1 is fully serial and, with
// serial execution, ready tasks run in deterministic (ID-sorted) order. The
// first task error stops scheduling of not-yet-started tasks; already-running
// tasks are awaited, and that error is returned. A cycle (or any task left
// unschedulable) is reported as an error naming the stuck tasks.
//
// This replaces hand-ordered phase loops in composite providers: edges come
// from the resources' $ref dependencies (see RefChildEdges) plus any explicit
// barrier edges the provider adds, and the order falls out of the graph.
func RunGraph(ctx context.Context, concurrency int, tasks []Task) error {
	if concurrency < 1 {
		concurrency = 1
	}

	byID := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		if _, dup := byID[t.ID]; dup {
			return fmt.Errorf("graph: duplicate task id %q", t.ID)
		}
		byID[t.ID] = t
	}

	// indegree = unmet dependency count; dependents = reverse edges.
	indegree := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := byID[dep]; !ok {
				return fmt.Errorf("graph: task %q depends on unknown task %q", t.ID, dep)
			}
			indegree[t.ID]++
			dependents[dep] = append(dependents[dep], t.ID)
		}
	}

	ready := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if indegree[t.ID] == 0 {
			ready = append(ready, t.ID)
		}
	}
	sort.Strings(ready)

	type result struct {
		id  string
		err error
	}
	results := make(chan result)

	inFlight := 0
	completed := 0
	stopLaunch := false
	var firstErr error

	launch := func(id string) {
		inFlight++
		t := byID[id]
		go func() {
			var err error
			if t.Run != nil {
				err = t.Run(ctx)
			}
			results <- result{id: id, err: err}
		}()
	}

	fill := func() {
		for len(ready) > 0 && inFlight < concurrency && !stopLaunch {
			id := ready[0]
			ready = ready[1:]
			launch(id)
		}
	}

	// A canceled context stops new launches but still drains in-flight work.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fill()

	for inFlight > 0 {
		res := <-results
		inFlight--
		completed++

		if res.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("task %q: %w", res.id, res.err)
			}
			stopLaunch = true
		}
		if stopLaunch || ctx.Err() != nil {
			stopLaunch = true
			continue // drain remaining in-flight, launch nothing new
		}

		newlyReady := ready[:len(ready):len(ready)]
		for _, dep := range dependents[res.id] {
			indegree[dep]--
			if indegree[dep] == 0 {
				newlyReady = append(newlyReady, dep)
			}
		}
		ready = newlyReady
		sort.Strings(ready)
		fill()
	}

	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if completed < len(tasks) {
		return fmt.Errorf("graph: %d task(s) unschedulable (cycle?): %s",
			len(tasks)-completed, strings.Join(stuckTasks(tasks, indegree), ", "))
	}
	return nil
}

// stuckTasks returns the sorted IDs of tasks that never reached zero
// indegree — the members of a dependency cycle (or downstream of one).
func stuckTasks(tasks []Task, indegree map[string]int) []string {
	var stuck []string
	for _, t := range tasks {
		if indegree[t.ID] > 0 {
			stuck = append(stuck, t.ID)
		}
	}
	sort.Strings(stuck)
	return stuck
}
