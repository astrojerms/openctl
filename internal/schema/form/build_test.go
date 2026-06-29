package form

import (
	"testing"
)

func TestBuildForKindReturnsClusterForm(t *testing.T) {
	f, ok, err := BuildForKind("k3s.openctl.io/v1", "Cluster")
	if err != nil {
		t.Fatalf("BuildForKind: %v", err)
	}
	if !ok {
		t.Fatal("BuildForKind ok=false; expected k3s Cluster schema to be present")
	}
	if f.Type != FieldObject {
		t.Fatalf("root type = %s, want object", f.Type)
	}
	top := byName(f.Fields)
	// #Resource top-level structure: apiVersion (const), kind (const),
	// metadata, spec.
	if top["apiVersion"].Const != "k3s.openctl.io/v1" {
		t.Errorf("apiVersion const = %v, want \"k3s.openctl.io/v1\"", top["apiVersion"].Const)
	}
	if top["kind"].Const != "Cluster" {
		t.Errorf("kind const = %v, want \"Cluster\"", top["kind"].Const)
	}

	spec := top["spec"]
	if spec.Type != FieldObject {
		t.Fatalf("spec type = %s, want object", spec.Type)
	}
	specFields := byName(spec.Fields)

	// Spec.nodes.controlPlane.count: bounded int with default.
	nodes := specFields["nodes"]
	if nodes.Type != FieldObject {
		t.Fatalf("nodes type = %s, want object", nodes.Type)
	}
	cp := byName(nodes.Fields)["controlPlane"]
	cpFields := byName(cp.Fields)
	count := cpFields["count"]
	if count.Type != FieldInt {
		t.Errorf("controlPlane.count type = %s, want int", count.Type)
	}
	if count.Min == nil || *count.Min != 1 {
		t.Errorf("controlPlane.count min = %v, want 1", count.Min)
	}

	// Spec.nodes.workers: optional array of struct.
	workers := byName(nodes.Fields)["workers"]
	if !workers.Optional {
		t.Error("nodes.workers should be optional")
	}
	if workers.Type != FieldArray {
		t.Fatalf("workers type = %s, want array", workers.Type)
	}
	if workers.Items == nil || workers.Items.Type != FieldObject {
		t.Errorf("workers.items = %+v, want object", workers.Items)
	}

	// Spec.ssh.user: string with default "ubuntu".
	ssh := byName(spec.Fields)["ssh"]
	user := byName(ssh.Fields)["user"]
	if user.Default != "ubuntu" {
		t.Errorf("ssh.user default = %v, want \"ubuntu\"", user.Default)
	}
}

func TestBuildForKindUnknownReturnsNotFound(t *testing.T) {
	_, ok, err := BuildForKind("unknown.openctl.io/v1", "Bogus")
	if err != nil {
		t.Fatalf("BuildForKind: %v", err)
	}
	if ok {
		t.Error("BuildForKind ok=true for unknown kind; want false")
	}
}
