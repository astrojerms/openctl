package server

import (
	"testing"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestComputeDriftIdenticalSpecsHasNoDrift(t *testing.T) {
	desired := map[string]any{"cpus": 2, "memoryMB": 4096}
	observed := map[string]any{"cpus": float64(2), "memoryMB": float64(4096)}
	if got := computeDrift(desired, observed); len(got) != 0 {
		t.Errorf("got drift entries %+v on identical-but-typed specs, want none", got)
	}
}

func TestComputeDriftScalarMismatch(t *testing.T) {
	desired := map[string]any{"cpus": 2, "memoryMB": 4096}
	observed := map[string]any{"cpus": float64(4), "memoryMB": float64(4096)}
	got := computeDrift(desired, observed)
	if len(got) != 1 {
		t.Fatalf("got %d drift entries, want 1: %+v", len(got), got)
	}
	want := &apiv1.DriftEntry{Path: "spec.cpus", Desired: "2", Observed: "4"}
	if got[0].Path != want.Path || got[0].Desired != want.Desired || got[0].Observed != want.Observed {
		t.Errorf("entry = %+v, want %+v", got[0], want)
	}
}

func TestComputeDriftIgnoresUnmanagedFields(t *testing.T) {
	// Loose comparison: observed has extra keys (provider defaults). Should
	// not produce drift entries.
	desired := map[string]any{"cpus": 2}
	observed := map[string]any{"cpus": float64(2), "boot": "order=scsi0", "vmid": float64(101)}
	if got := computeDrift(desired, observed); len(got) != 0 {
		t.Errorf("got drift %+v, want none (unmanaged fields should be ignored)", got)
	}
}

func TestComputeDriftNestedMap(t *testing.T) {
	desired := map[string]any{
		"network": map[string]any{"bridge": "vmbr0"},
	}
	observed := map[string]any{
		"network": map[string]any{"bridge": "vmbr1"},
	}
	got := computeDrift(desired, observed)
	if len(got) != 1 || got[0].Path != "spec.network.bridge" {
		t.Errorf("got %+v, want one entry at spec.network.bridge", got)
	}
}

func TestComputeDriftSliceLengthChange(t *testing.T) {
	desired := map[string]any{
		"workers": []any{
			map[string]any{"name": "worker", "count": 2},
		},
	}
	observed := map[string]any{
		"workers": []any{
			map[string]any{"name": "worker", "count": float64(2)},
			map[string]any{"name": "extra", "count": float64(1)},
		},
	}
	got := computeDrift(desired, observed)
	var foundLen bool
	for _, e := range got {
		if e.Path == "spec.workers.length" && e.Desired == "1" && e.Observed == "2" {
			foundLen = true
		}
	}
	if !foundLen {
		t.Errorf("expected spec.workers.length entry, got %+v", got)
	}
}

func TestComputeDriftMissingObservedKey(t *testing.T) {
	desired := map[string]any{"cpus": 2}
	observed := map[string]any{}
	got := computeDrift(desired, observed)
	if len(got) != 1 || got[0].Path != "spec.cpus" || got[0].Observed != "<unset>" {
		t.Errorf("got %+v, want one entry with Observed=<unset>", got)
	}
}

func TestComputeDriftStableOrdering(t *testing.T) {
	desired := map[string]any{"z": 1, "a": 2, "m": 3}
	observed := map[string]any{"z": float64(99), "a": float64(98), "m": float64(97)}
	got := computeDrift(desired, observed)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Path != "spec.a" || got[1].Path != "spec.m" || got[2].Path != "spec.z" {
		t.Errorf("paths = %v %v %v, want a/m/z", got[0].Path, got[1].Path, got[2].Path)
	}
}
