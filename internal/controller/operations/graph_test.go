package operations

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordOrder returns a Task whose Run appends its ID to a shared slice under
// a mutex, for asserting execution order.
func recordOrder(id string, deps []string, mu *sync.Mutex, order *[]string) Task {
	return Task{ID: id, DependsOn: deps, Run: func(context.Context) error {
		mu.Lock()
		*order = append(*order, id)
		mu.Unlock()
		return nil
	}}
}

// TestRunGraph_SerialOrderRespectsDepsDeterministically: a diamond
// (b,c depend on a; d depends on b,c) runs a first and d last, with the
// independent middle tier in deterministic ID order.
func TestRunGraph_SerialOrderRespectsDepsDeterministically(t *testing.T) {
	var mu sync.Mutex
	var order []string
	tasks := []Task{
		recordOrder("d", []string{"b", "c"}, &mu, &order),
		recordOrder("b", []string{"a"}, &mu, &order),
		recordOrder("c", []string{"a"}, &mu, &order),
		recordOrder("a", nil, &mu, &order),
	}
	if err := RunGraph(context.Background(), 1, tasks); err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	want := []string{"a", "b", "c", "d"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestRunGraph_DetectsCycle(t *testing.T) {
	tasks := []Task{
		{ID: "a", DependsOn: []string{"c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	err := RunGraph(context.Background(), 1, tasks)
	if err == nil || !strings.Contains(err.Error(), "unschedulable") {
		t.Fatalf("want cycle error, got %v", err)
	}
	// The stuck tasks should be named.
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("cycle error should name %q: %v", id, err)
		}
	}
}

func TestRunGraph_UnknownDependency(t *testing.T) {
	err := RunGraph(context.Background(), 1, []Task{{ID: "a", DependsOn: []string{"ghost"}}})
	if err == nil || !strings.Contains(err.Error(), "unknown task") {
		t.Fatalf("want unknown-task error, got %v", err)
	}
}

func TestRunGraph_DuplicateID(t *testing.T) {
	err := RunGraph(context.Background(), 1, []Task{{ID: "a"}, {ID: "a"}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate-id error, got %v", err)
	}
}

// TestRunGraph_ErrorStopsDependentsButReturnsFirst: a failing task's
// dependents never run, the error is returned wrapped with the task ID, and
// independent already-scheduled work isn't corrupted.
func TestRunGraph_ErrorStopsDependents(t *testing.T) {
	var ran sync.Map
	mark := func(id string, deps []string, fail bool) Task {
		return Task{ID: id, DependsOn: deps, Run: func(context.Context) error {
			ran.Store(id, true)
			if fail {
				return fmt.Errorf("boom")
			}
			return nil
		}}
	}
	tasks := []Task{
		mark("a", nil, true),
		mark("b", []string{"a"}, false), // depends on failing a — must not run
	}
	err := RunGraph(context.Background(), 1, tasks)
	if err == nil || !strings.Contains(err.Error(), `task "a"`) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want wrapped error for task a, got %v", err)
	}
	if _, ok := ran.Load("b"); ok {
		t.Error("dependent b ran despite a failing")
	}
}

// TestRunGraph_ParallelIndependentTasks proves independent tasks actually run
// concurrently when concurrency allows: all N rendezvous before any returns.
func TestRunGraph_ParallelIndependentTasks(t *testing.T) {
	const n = 4
	var started sync.WaitGroup
	started.Add(n)
	release := make(chan struct{})
	var cur, maxConc atomic.Int64

	body := func(context.Context) error {
		c := cur.Add(1)
		for {
			m := maxConc.Load()
			if c <= m || maxConc.CompareAndSwap(m, c) {
				break
			}
		}
		started.Done()
		select {
		case <-release:
		case <-time.After(2 * time.Second): // safety net; not hit at concurrency n
		}
		cur.Add(-1)
		return nil
	}

	tasks := make([]Task, n)
	for i := range tasks {
		tasks[i] = Task{ID: fmt.Sprintf("t%d", i), Run: body}
	}
	go func() { started.Wait(); close(release) }()

	if err := RunGraph(context.Background(), n, tasks); err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if got := maxConc.Load(); got != n {
		t.Errorf("max concurrency = %d, want %d", got, n)
	}
}

// TestRunGraph_ParallelStillRespectsDeps: even at high concurrency, a
// dependent never starts before its dependency finishes.
func TestRunGraph_ParallelStillRespectsDeps(t *testing.T) {
	var aDone, violation atomic.Int64
	tasks := []Task{
		{ID: "a", Run: func(context.Context) error {
			time.Sleep(20 * time.Millisecond)
			aDone.Store(1)
			return nil
		}},
	}
	for i := range 8 {
		tasks = append(tasks, Task{ID: fmt.Sprintf("dep%d", i), DependsOn: []string{"a"}, Run: func(context.Context) error {
			if aDone.Load() == 0 {
				violation.Add(1)
			}
			return nil
		}})
	}
	if err := RunGraph(context.Background(), 8, tasks); err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if violation.Load() != 0 {
		t.Errorf("%d dependents started before their dependency finished", violation.Load())
	}
}

// TestRunGraph_NilRunBarrier: a task with a nil Run is a pure ordering point.
func TestRunGraph_NilRunBarrier(t *testing.T) {
	var mu sync.Mutex
	var order []string
	tasks := []Task{
		recordOrder("after", []string{"barrier"}, &mu, &order),
		{ID: "barrier", DependsOn: []string{"before"}}, // nil Run
		recordOrder("before", nil, &mu, &order),
	}
	if err := RunGraph(context.Background(), 1, tasks); err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if strings.Join(order, ",") != "before,after" {
		t.Errorf("order = %v, want [before after]", order)
	}
}

func TestRunGraph_Empty(t *testing.T) {
	if err := RunGraph(context.Background(), 1, nil); err != nil {
		t.Errorf("empty graph: %v", err)
	}
}
