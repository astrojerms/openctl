package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// lastRuleIsCatchAll asserts the ingress ends with exactly one host-less
// catch-all and no earlier catch-all sneaks in.
func hostnames(ingress []map[string]any) []string {
	var hs []string
	for _, r := range ingress {
		if h, _ := r["hostname"].(string); h != "" {
			hs = append(hs, h)
		}
	}
	return hs
}

func catchAllLast(t *testing.T, ingress []map[string]any) {
	t.Helper()
	if len(ingress) == 0 {
		t.Fatal("ingress is empty (needs at least a catch-all)")
	}
	last := ingress[len(ingress)-1]
	if _, hasHost := last["hostname"]; hasHost {
		t.Errorf("last rule must be the host-less catch-all, got %v", last)
	}
	for i, r := range ingress[:len(ingress)-1] {
		if _, hasHost := r["hostname"]; !hasHost {
			t.Errorf("non-final rule %d is a catch-all (only the last may be): %v", i, r)
		}
	}
}

func TestMergeIngressRule(t *testing.T) {
	// Into empty → [rule, catch-all].
	got := mergeIngressRule(nil, "a.example.com", "http://a:80", "")
	if hs := hostnames(got); len(hs) != 1 || hs[0] != "a.example.com" {
		t.Fatalf("hostnames = %v, want [a.example.com]", hs)
	}
	catchAllLast(t, got)

	// Add a second hostname → both preserved.
	got = mergeIngressRule(got, "b.example.com", "http://b:80", "/api")
	if hs := hostnames(got); len(hs) != 2 {
		t.Fatalf("hostnames = %v, want 2", hs)
	}
	catchAllLast(t, got)

	// Upsert an existing hostname → replaced in place, not duplicated.
	got = mergeIngressRule(got, "a.example.com", "http://a-new:80", "")
	hs := hostnames(got)
	countA := 0
	for _, h := range hs {
		if h == "a.example.com" {
			countA++
		}
	}
	if countA != 1 {
		t.Errorf("a.example.com appears %d times, want 1 (upsert)", countA)
	}
	for _, r := range got {
		if r["hostname"] == "a.example.com" && r["service"] != "http://a-new:80" {
			t.Errorf("a.example.com service not updated: %v", r)
		}
	}
	catchAllLast(t, got)
}

func TestRemoveIngressRule(t *testing.T) {
	in := []map[string]any{
		{"hostname": "a.example.com", "service": "http://a:80"},
		{"hostname": "b.example.com", "service": "http://b:80"},
		{"service": "http_status:404"},
	}
	got := removeIngressRule(in, "a.example.com")
	if hs := hostnames(got); len(hs) != 1 || hs[0] != "b.example.com" {
		t.Fatalf("after remove, hostnames = %v, want [b.example.com]", hs)
	}
	catchAllLast(t, got)

	// Removing the last host rule leaves just a catch-all (valid minimal config).
	got = removeIngressRule(got, "b.example.com")
	if hs := hostnames(got); len(hs) != 0 {
		t.Errorf("expected no host rules, got %v", hs)
	}
	catchAllLast(t, got)
}

func applyRoute(t *testing.T, p *provider, name, tunnel, hostname, service string) {
	t.Helper()
	m := &protocol.Resource{
		APIVersion: apiVersion, Kind: kindTunnelRoute,
		Metadata: protocol.ResourceMetadata{Name: name},
		Spec:     map[string]any{"tunnel": tunnel, "hostname": hostname, "service": service},
	}
	if _, err := p.Apply(context.Background(), pluginproto.ApplyParams{Manifest: m}); err != nil {
		t.Fatalf("apply route %s: %v", name, err)
	}
}

// TestTunnelRouteAggregation is the core G1 guarantee: multiple TunnelRoutes
// contribute to one shared Tunnel's ingress without clobbering each other, and
// deleting one leaves the others intact.
func TestTunnelRouteAggregation(t *testing.T) {
	f := newFakeCF(t)
	p := configuredProvider(t, f, map[string]string{"accountId": "acct-1"})
	ctx := context.Background()

	// Create the tunnel the routes attach to.
	tun := &protocol.Resource{
		APIVersion: apiVersion, Kind: kindTunnel,
		Metadata: protocol.ResourceMetadata{Name: "home"},
		Spec:     map[string]any{},
	}
	tunRes, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: tun})
	if err != nil {
		t.Fatalf("apply tunnel: %v", err)
	}
	tunnelID, _ := tunRes.Resource.Status["id"].(string)
	if tunnelID == "" {
		t.Fatal("tunnel got no id")
	}

	// Two apps expose themselves via their own TunnelRoute.
	applyRoute(t, p, "chat", "home", "chat.example.com", "https://traefik:443")
	applyRoute(t, p, "blog", "home", "blog.example.com", "https://traefik:443")

	// The tunnel's ingress (as pushed to Cloudflare) carries both, catch-all last.
	cfg, err := p.readTunnelIngress(ctx, "acct-1", tunnelID)
	if err != nil {
		t.Fatalf("read ingress: %v", err)
	}
	hs := hostnames(cfg)
	if len(hs) != 2 {
		t.Fatalf("ingress hostnames = %v, want chat + blog", hs)
	}
	catchAllLast(t, cfg)

	// Get reports Ready for a live route.
	gr, err := p.getTunnelRoute(ctx, "chat", mustRouteState(t, "acct-1", tunnelID, "home", "chat.example.com"))
	if err != nil {
		t.Fatalf("get chat route: %v", err)
	}
	if gr.Resource.Status["phase"] != "Ready" {
		t.Errorf("chat route phase = %v, want Ready", gr.Resource.Status["phase"])
	}

	// Deleting chat leaves blog intact.
	if err := p.deleteTunnelRoute(ctx, mustRouteState(t, "acct-1", tunnelID, "home", "chat.example.com")); err != nil {
		t.Fatalf("delete chat: %v", err)
	}
	cfg, _ = p.readTunnelIngress(ctx, "acct-1", tunnelID)
	if hs := hostnames(cfg); len(hs) != 1 || hs[0] != "blog.example.com" {
		t.Fatalf("after deleting chat, hostnames = %v, want [blog.example.com]", hs)
	}
	catchAllLast(t, cfg)

	// Get on the deleted route → NotFound.
	if _, err := p.getTunnelRoute(ctx, "chat", mustRouteState(t, "acct-1", tunnelID, "home", "chat.example.com")); err == nil {
		t.Error("expected NotFound for the deleted chat route")
	}
}

func mustRouteState(t *testing.T, acct, tunnelID, tunnelName, hostname string) []byte {
	t.Helper()
	b, err := json.Marshal(tunnelRouteState{AccountID: acct, TunnelID: tunnelID, TunnelName: tunnelName, Hostname: hostname})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
