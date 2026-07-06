package resources

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// ParseClusterSpec reads network.perContext into the typed spec, and
// BridgeForContext resolves the right bridge per node.
func TestParseClusterSpec_PerContext(t *testing.T) {
	r := &protocol.Resource{
		Spec: map[string]any{
			"network": map[string]any{
				"bridge": "vmbr0", // cluster-wide default
				"perContext": map[string]any{
					"siteA": map[string]any{
						"bridge":    "vmbr0",
						"staticIPs": map[string]any{"startIP": "10.1.0.10", "gateway": "10.1.0.1", "netmask": "24"},
					},
					"siteB": map[string]any{
						"bridge":    "vmbr1",
						"staticIPs": map[string]any{"startIP": "10.2.0.10"},
					},
				},
			},
		},
	}
	spec, err := ParseClusterSpec(r)
	if err != nil {
		t.Fatalf("ParseClusterSpec: %v", err)
	}
	if len(spec.Network.PerContext) != 2 {
		t.Fatalf("PerContext has %d entries, want 2", len(spec.Network.PerContext))
	}
	if spec.Network.PerContext["siteB"].Bridge != "vmbr1" {
		t.Errorf("siteB bridge = %q, want vmbr1", spec.Network.PerContext["siteB"].Bridge)
	}
	if spec.Network.PerContext["siteA"].StaticIPs.StartIP != "10.1.0.10" {
		t.Errorf("siteA startIP = %q", spec.Network.PerContext["siteA"].StaticIPs.StartIP)
	}
	// perContext implies static IPs.
	if spec.Network.DHCP {
		t.Error("DHCP should be disabled when perContext is set")
	}
}

func TestBridgeForContext(t *testing.T) {
	n := &NetworkSpec{
		Bridge: "vmbr0",
		PerContext: map[string]NetworkBlock{
			"siteB": {Bridge: "vmbr1"},
			"siteC": {}, // no bridge override
		},
	}
	cases := map[string]string{
		"siteB":   "vmbr1", // per-context override
		"siteC":   "vmbr0", // block exists but no bridge → cluster-wide
		"unknown": "vmbr0", // no block → cluster-wide
		"":        "vmbr0", // single-L2 default
	}
	for ctx, want := range cases {
		if got := n.BridgeForContext(ctx); got != want {
			t.Errorf("BridgeForContext(%q) = %q, want %q", ctx, got, want)
		}
	}
}

// A single-L2 cluster (no perContext) parses with an empty PerContext and the
// cluster-wide bridge, unchanged.
func TestParseClusterSpec_SingleL2Unchanged(t *testing.T) {
	r := &protocol.Resource{
		Spec: map[string]any{
			"network": map[string]any{"bridge": "vmbr0", "dhcp": true},
		},
	}
	spec, err := ParseClusterSpec(r)
	if err != nil {
		t.Fatalf("ParseClusterSpec: %v", err)
	}
	if len(spec.Network.PerContext) != 0 {
		t.Errorf("PerContext should be empty for a single-L2 cluster, got %v", spec.Network.PerContext)
	}
	if spec.Network.BridgeForContext("anything") != "vmbr0" {
		t.Error("single-L2 bridge resolution changed")
	}
}
