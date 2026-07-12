package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openctl/openctl/pkg/pluginproto"
)

func findRef(refs []pluginproto.ResourceRef, kind, name string) *pluginproto.ResourceRef {
	for i := range refs {
		if refs[i].Kind == kind && refs[i].Name == name {
			return &refs[i]
		}
	}
	return nil
}

// TestRefsFromReleaseManifest proves a rendered Helm manifest maps to graph
// refs: grouped and core apiVersions are preserved, and undecodable/nameless
// documents are skipped rather than producing junk nodes.
func TestRefsFromReleaseManifest(t *testing.T) {
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: demo
---
apiVersion: v1
kind: Service
metadata:
  name: web
---
# a comment-only / empty doc is skipped
---
apiVersion: v1
kind: ConfigMap
metadata:
  generateName: cfg-
`
	refs := refsFromReleaseManifest(manifest)
	if len(refs) != 2 {
		t.Fatalf("want 2 addressable refs, got %d: %+v", len(refs), refs)
	}
	if d := findRef(refs, "Deployment", "web"); d == nil || d.APIVersion != "apps/v1" {
		t.Errorf("Deployment ref wrong: %+v", d)
	}
	if s := findRef(refs, "Service", "web"); s == nil || s.APIVersion != "v1" {
		t.Errorf("Service ref wrong: %+v", s)
	}
	if c := findRef(refs, "ConfigMap", ""); c != nil {
		t.Errorf("generateName-only object should be skipped, got %+v", c)
	}
}

// TestChildrenOfManifest proves a Manifest reports its applied objects straight
// from persisted state, reconstructing apiVersion from group+version.
func TestChildrenOfManifest(t *testing.T) {
	st := manifestState{Objects: []objectRef{
		{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespace: "demo", Name: "api"},
		{Group: "", Version: "v1", Resource: "services", Kind: "Service", Name: "api"},
		{Group: "", Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Name: ""}, // skipped
	}}
	raw, _ := json.Marshal(st)

	refs, err := (&provider{}).childrenOfManifest(raw)
	if err != nil {
		t.Fatalf("childrenOfManifest: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %d: %+v", len(refs), refs)
	}
	if d := findRef(refs, "Deployment", "api"); d == nil || d.APIVersion != "apps/v1" {
		t.Errorf("Deployment ref wrong: %+v", d)
	}
	if s := findRef(refs, "Service", "api"); s == nil || s.APIVersion != "v1" {
		t.Errorf("Service ref wrong (core group should be bare version): %+v", s)
	}
}

// TestChildrenOfDispatch covers kind routing and the no-state degradation.
func TestChildrenOfDispatch(t *testing.T) {
	p := &provider{}

	// Unknown/leaf kinds compose nothing.
	if refs, _ := p.ChildrenOf(context.Background(), pluginproto.RefParams{Kind: kindArgoApplications}); refs != nil {
		t.Errorf("ArgoApplications should have no children, got %+v", refs)
	}

	// A HelmRelease with no prior state can't reach a cluster → no children, no error.
	refs, err := p.ChildrenOf(context.Background(), pluginproto.RefParams{Kind: kindHelmRelease, Name: "x"})
	if err != nil {
		t.Fatalf("ChildrenOf with empty state should not error: %v", err)
	}
	if refs != nil {
		t.Errorf("HelmRelease with no state should yield no children, got %+v", refs)
	}
}
