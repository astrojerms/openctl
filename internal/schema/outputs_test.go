package schema

import "testing"

func pathsOf(outs []Output) []string {
	p := make([]string, 0, len(outs))
	for _, o := range outs {
		p = append(p, o.Path)
	}
	return p
}

func TestOutputsForCluster(t *testing.T) {
	outs, ok := OutputsFor("k3s.openctl.io/v1", "Cluster")
	if !ok {
		t.Fatal("expected k3s Cluster to declare status outputs")
	}
	byPath := map[string]Output{}
	for _, o := range outs {
		byPath[o.Path] = o
	}

	kc, found := byPath["status.outputs.kubeconfigPath"]
	if !found {
		t.Fatalf("missing status.outputs.kubeconfigPath; got %v", pathsOf(outs))
	}
	if kc.Type != "string" {
		t.Errorf("kubeconfigPath type = %q, want string", kc.Type)
	}
	if kc.Doc == "" {
		t.Error("kubeconfigPath should carry its doc comment")
	}
	for _, want := range []string{"status.outputs.serverIP", "status.outputs.agent.bundleDir", "status.phase"} {
		if _, found := byPath[want]; !found {
			t.Errorf("missing declared output %s; got %v", want, pathsOf(outs))
		}
	}
	// A {[string]: string} map (endpoints) is a single leaf, not recursed into.
	if ep, found := byPath["status.outputs.agent.endpoints"]; !found || ep.Type != "object" {
		t.Errorf("endpoints should be one object leaf; got %+v found=%v", ep, found)
	}
}

func TestOutputsForVM(t *testing.T) {
	outs, ok := OutputsFor("proxmox.openctl.io/v1", "VirtualMachine")
	if !ok {
		t.Fatal("expected VM to declare status outputs")
	}
	byPath := map[string]Output{}
	for _, o := range outs {
		byPath[o.Path] = o
	}
	if ip, found := byPath["status.ip"]; !found || ip.Type != "string" {
		t.Errorf("VM should declare status.ip:string; got %+v found=%v", ip, found)
	}
}

func TestOutputsForExternalAndAbsent(t *testing.T) {
	t.Cleanup(ResetExternal)

	// A schema with NO declared status → ok=false (status stays the open base _).
	RegisterExternal("demo.openctl.io/v1", "Thing",
		`#Thing: {apiVersion: "demo.openctl.io/v1", kind: "Thing", metadata: {name: string, ...}, spec: {x?: string}, ...}`)
	if _, ok := OutputsFor("demo.openctl.io/v1", "Thing"); ok {
		t.Error("a kind with no declared status should return ok=false")
	}

	// An external plugin kind CAN declare status the same way — proving the
	// mechanism works for external providers, not just built-ins.
	RegisterExternal("demo.openctl.io/v1", "Widget",
		`#Widget: {apiVersion: "demo.openctl.io/v1", kind: "Widget", metadata: {name: string, ...}, spec: {}, status?: {token?: string, ...}, ...}`)
	outs, ok := OutputsFor("demo.openctl.io/v1", "Widget")
	if !ok || len(outs) != 1 || outs[0].Path != "status.token" || outs[0].Type != "string" {
		t.Errorf("external Widget outputs = %+v (ok=%v), want [status.token:string]", outs, ok)
	}

	// Unknown kind → ok=false.
	if _, ok := OutputsFor("nope.openctl.io/v1", "Nope"); ok {
		t.Error("unknown kind should return ok=false")
	}
}
