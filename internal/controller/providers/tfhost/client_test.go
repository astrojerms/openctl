package tfhost_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers/tfhost"
	"github.com/openctl/openctl/pkg/tfplugin6"
)

// buildFakeProvider compiles the tf-fake tfplugin6 provider into a temp binary
// and returns its path. Skips under -short or when the go toolchain is absent.
func buildFakeProvider(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a provider binary; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "tf-fake")
	build := exec.Command("go", "build", "-o", bin, "github.com/openctl/openctl/plugins/tf-fake")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build tf-fake: %v", err)
	}
	return bin
}

func TestLaunchAndGetSchema(t *testing.T) {
	bin := buildFakeProvider(t)

	client, err := tfhost.Launch(bin)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer client.Close()

	resp, err := client.GetProviderSchema(context.Background())
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}

	// Provider config schema came through.
	if resp.Provider == nil || resp.Provider.Block == nil {
		t.Fatal("missing provider schema block")
	}

	// The fake advertises exactly one resource, fake_thing, with name + note
	// input attributes and a computed id.
	rs, ok := resp.ResourceSchemas["fake_thing"]
	if !ok {
		t.Fatalf("fake_thing resource schema missing; got %d resource schemas", len(resp.ResourceSchemas))
	}
	if rs.Version != 1 {
		t.Errorf("fake_thing schema version = %d, want 1", rs.Version)
	}
	attrs := map[string]bool{}
	for _, a := range rs.Block.Attributes {
		attrs[a.Name] = true
	}
	if !attrs["name"] || !attrs["note"] || !attrs["id"] {
		t.Errorf("fake_thing attributes = %v, want name + note + id", attrs)
	}
}

func TestFakeProviderLifecycle(t *testing.T) {
	bin := buildFakeProvider(t)

	client, err := tfhost.Launch(bin)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	config := jsonValue(t, map[string]any{
		"name": "alpha",
		"note": "created from json dynamic value",
		"id":   nil,
	})
	null := &tfplugin6.DynamicValue{Json: []byte("null")}

	plan, err := client.PlanResourceChange(ctx, &tfplugin6.PlanResourceChange_Request{
		TypeName:         "fake_thing",
		PriorState:       null,
		ProposedNewState: config,
		Config:           config,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange: %v", err)
	}
	assertNoDiagnostics(t, plan.Diagnostics)
	assertThing(t, plan.PlannedState, "alpha", "created from json dynamic value", "fake-alpha")
	if got := string(plan.PlannedPrivate); got != "plan:fake-alpha" {
		t.Fatalf("planned private = %q, want plan:fake-alpha", got)
	}

	applied, err := client.ApplyResourceChange(ctx, &tfplugin6.ApplyResourceChange_Request{
		TypeName:       "fake_thing",
		PriorState:     null,
		PlannedState:   plan.PlannedState,
		Config:         config,
		PlannedPrivate: plan.PlannedPrivate,
	})
	if err != nil {
		t.Fatalf("ApplyResourceChange: %v", err)
	}
	assertNoDiagnostics(t, applied.Diagnostics)
	assertThing(t, applied.NewState, "alpha", "created from json dynamic value", "fake-alpha")
	if got := string(applied.Private); got != "state:fake-alpha" {
		t.Fatalf("apply private = %q, want state:fake-alpha", got)
	}

	read, err := client.ReadResource(ctx, &tfplugin6.ReadResource_Request{
		TypeName:     "fake_thing",
		CurrentState: applied.NewState,
		Private:      applied.Private,
	})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	assertNoDiagnostics(t, read.Diagnostics)
	assertThing(t, read.NewState, "alpha", "created from json dynamic value", "fake-alpha")
	if got := string(read.Private); got != "read:fake-alpha" {
		t.Fatalf("read private = %q, want read:fake-alpha", got)
	}

	deletePlan, err := client.PlanResourceChange(ctx, &tfplugin6.PlanResourceChange_Request{
		TypeName:         "fake_thing",
		PriorState:       read.NewState,
		ProposedNewState: null,
		Config:           null,
		PriorPrivate:     read.Private,
	})
	if err != nil {
		t.Fatalf("PlanResourceChange delete: %v", err)
	}
	assertNoDiagnostics(t, deletePlan.Diagnostics)
	assertNullState(t, deletePlan.PlannedState)
	if got := string(deletePlan.PlannedPrivate); got != "read:fake-alpha" {
		t.Fatalf("delete planned private = %q, want read:fake-alpha", got)
	}

	deleted, err := client.ApplyResourceChange(ctx, &tfplugin6.ApplyResourceChange_Request{
		TypeName:       "fake_thing",
		PriorState:     read.NewState,
		PlannedState:   deletePlan.PlannedState,
		Config:         null,
		PlannedPrivate: deletePlan.PlannedPrivate,
	})
	if err != nil {
		t.Fatalf("ApplyResourceChange delete: %v", err)
	}
	assertNoDiagnostics(t, deleted.Diagnostics)
	assertNullState(t, deleted.NewState)

	missing, err := client.ReadResource(ctx, &tfplugin6.ReadResource_Request{
		TypeName:     "fake_thing",
		CurrentState: read.NewState,
		Private:      read.Private,
	})
	if err != nil {
		t.Fatalf("ReadResource after delete: %v", err)
	}
	assertNoDiagnostics(t, missing.Diagnostics)
	assertNullState(t, missing.NewState)
}

func TestLaunchBadBinary(t *testing.T) {
	// A non-provider binary must fail the handshake, not hang.
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	_, err := tfhost.Launch("/bin/echo")
	if err == nil {
		t.Fatal("expected launch of a non-provider binary to fail")
	}
}

func jsonValue(t *testing.T, v any) *tfplugin6.DynamicValue {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal dynamic value: %v", err)
	}
	return &tfplugin6.DynamicValue{Json: b}
}

func assertNoDiagnostics(t *testing.T, diags []*tfplugin6.Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d.GetSeverity() == tfplugin6.Diagnostic_ERROR {
			t.Fatalf("unexpected diagnostic: %s: %s", d.GetSummary(), d.GetDetail())
		}
	}
}

func assertThing(t *testing.T, dv *tfplugin6.DynamicValue, wantName, wantNote, wantID string) {
	t.Helper()
	if dv == nil {
		t.Fatal("dynamic value is nil")
	}
	var got struct {
		Name string `json:"name"`
		Note string `json:"note"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(dv.Json, &got); err != nil {
		t.Fatalf("unmarshal dynamic value JSON %q: %v", string(dv.Json), err)
	}
	if got.Name != wantName || got.Note != wantNote || got.ID != wantID {
		t.Fatalf("state = %+v, want name=%q note=%q id=%q", got, wantName, wantNote, wantID)
	}
}

func assertNullState(t *testing.T, dv *tfplugin6.DynamicValue) {
	t.Helper()
	if dv == nil {
		t.Fatal("dynamic value is nil")
	}
	var got any
	if err := json.Unmarshal(dv.Json, &got); err != nil {
		t.Fatalf("unmarshal dynamic value JSON %q: %v", string(dv.Json), err)
	}
	if got != nil {
		t.Fatalf("state = %v, want null", got)
	}
}
