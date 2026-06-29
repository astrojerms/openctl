package k3s

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/protocol"
)

// ipFakeVMs is a fake VMApplier where Get returns the configured IP after
// the configured number of polls (per node). Used to exercise the QGA poll
// loop without spinning up real infrastructure.
type ipFakeVMs struct {
	// pollsBefore[name] = how many Get calls return "no IP yet" before the
	// fake starts returning the real one. Default 0 → first call succeeds.
	pollsBefore map[string]int
	// ips[name] = the IP eventually returned.
	ips map[string]string
	// getErrs[name] = an error to return for the first N calls before
	// flipping to success. Optional.
	getErrs map[string]error
	calls   map[string]*atomic.Int32
}

func newIPFakeVMs(ips map[string]string) *ipFakeVMs {
	f := &ipFakeVMs{
		ips:         ips,
		pollsBefore: map[string]int{},
		getErrs:     map[string]error{},
		calls:       map[string]*atomic.Int32{},
	}
	for n := range ips {
		f.calls[n] = &atomic.Int32{}
	}
	return f
}

func (f *ipFakeVMs) Apply(_ context.Context, _ *protocol.Resource) (*protocol.Resource, error) {
	return nil, nil
}
func (f *ipFakeVMs) Delete(_ context.Context, _, _ string) error { return nil }
func (f *ipFakeVMs) Get(_ context.Context, _, name string) (*protocol.Resource, error) {
	if f.calls[name] == nil {
		f.calls[name] = &atomic.Int32{}
	}
	n := int(f.calls[name].Add(1))
	if err, ok := f.getErrs[name]; ok && n <= f.pollsBefore[name] {
		return nil, err
	}
	if n <= f.pollsBefore[name] {
		return &protocol.Resource{Status: map[string]any{}}, nil
	}
	return &protocol.Resource{Status: map[string]any{"ip": f.ips[name]}}, nil
}

func TestPollVMIPsReturnsImmediatelyWhenAllReady(t *testing.T) {
	vms := newIPFakeVMs(map[string]string{
		"vm-a": "192.168.1.10",
		"vm-b": "192.168.1.11",
	})
	got, err := pollVMIPs(context.Background(), vms, []string{"vm-a", "vm-b"}, time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("pollVMIPs: %v", err)
	}
	if got["vm-a"] != "192.168.1.10" || got["vm-b"] != "192.168.1.11" {
		t.Errorf("got = %v, want vm-a=.10, vm-b=.11", got)
	}
	if c := vms.calls["vm-a"].Load(); c != 1 {
		t.Errorf("vm-a called %d times, want 1", c)
	}
}

func TestPollVMIPsWaitsForLateNode(t *testing.T) {
	vms := newIPFakeVMs(map[string]string{"vm-a": "192.168.1.10"})
	vms.pollsBefore["vm-a"] = 2 // first 2 calls return no IP

	got, err := pollVMIPs(context.Background(), vms, []string{"vm-a"}, time.Second, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("pollVMIPs: %v", err)
	}
	if got["vm-a"] != "192.168.1.10" {
		t.Errorf("got %q, want .10", got["vm-a"])
	}
	if c := vms.calls["vm-a"].Load(); c != 3 {
		t.Errorf("vm-a called %d times, want 3 (2 misses + 1 hit)", c)
	}
}

func TestPollVMIPsToleratesTransientGetErrors(t *testing.T) {
	vms := newIPFakeVMs(map[string]string{"vm-a": "192.168.1.10"})
	vms.pollsBefore["vm-a"] = 2
	vms.getErrs["vm-a"] = errors.New("transient proxmox error")

	got, err := pollVMIPs(context.Background(), vms, []string{"vm-a"}, time.Second, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("pollVMIPs: %v", err)
	}
	if got["vm-a"] != "192.168.1.10" {
		t.Errorf("got %q, want .10", got["vm-a"])
	}
}

func TestPollVMIPsTimesOutWithClearError(t *testing.T) {
	vms := newIPFakeVMs(map[string]string{"vm-a": "192.168.1.10"})
	vms.pollsBefore["vm-a"] = 1000 // effectively never returns an IP within timeout

	_, err := pollVMIPs(context.Background(), vms, []string{"vm-a"}, 30*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout, got nil")
	}
	if got := err.Error(); !contains(got, "timed out") || !contains(got, "vm-a") {
		t.Errorf("error message should mention timeout + missing node, got: %v", err)
	}
}

func TestPollVMIPsHonorsContextCancel(t *testing.T) {
	vms := newIPFakeVMs(map[string]string{"vm-a": "192.168.1.10"})
	vms.pollsBefore["vm-a"] = 1000

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pollVMIPs(ctx, vms, []string{"vm-a"}, time.Second, 5*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
