package resources

import "testing"

// A cluster with per-context network blocks allocates each node from its own
// context's range (separate-L2 spread).
func TestAllocateIPs_PerContext(t *testing.T) {
	spec := &ClusterSpec{
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 2, Context: "siteA"},
			Workers:      []WorkerSpec{{Name: "default", Count: 2, Context: "siteB"}},
		},
		Network: NetworkSpec{
			PerContext: map[string]NetworkBlock{
				"siteA": {StaticIPs: &StaticIPSpec{StartIP: "10.1.0.10", Gateway: "10.1.0.1", Netmask: "24"}},
				"siteB": {StaticIPs: &StaticIPSpec{StartIP: "10.2.0.10", Gateway: "10.2.0.1", Netmask: "24"}},
			},
		},
	}

	ips, err := AllocateIPs("dev", spec)
	if err != nil {
		t.Fatalf("AllocateIPs: %v", err)
	}
	// Control planes on siteA get 10.1.0.10/.11 (name-sorted); workers on
	// siteB get 10.2.0.10/.11.
	want := map[string]string{
		"dev-cp-0":      "10.1.0.10",
		"dev-cp-1":      "10.1.0.11",
		"dev-default-0": "10.2.0.10",
		"dev-default-1": "10.2.0.11",
	}
	for name, wantIP := range want {
		if ips[name] != wantIP {
			t.Errorf("ip[%s] = %q, want %q (full map: %v)", name, ips[name], wantIP, ips)
		}
	}
	// No cross-contamination: every siteA node is in 10.1.x, siteB in 10.2.x.
	for name, ip := range ips {
		if (name == "dev-cp-0" || name == "dev-cp-1") && ip[:5] != "10.1." {
			t.Errorf("control-plane %s got %q, expected siteA (10.1.x)", name, ip)
		}
	}
}

// A node placed on a context with no network block fails fast at allocation.
func TestAllocateIPs_PerContext_MissingBlock(t *testing.T) {
	spec := &ClusterSpec{
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 1, Context: "siteA"},
			Workers:      []WorkerSpec{{Name: "default", Count: 1, Context: "siteB"}},
		},
		Network: NetworkSpec{
			PerContext: map[string]NetworkBlock{
				"siteA": {StaticIPs: &StaticIPSpec{StartIP: "10.1.0.10"}},
				// siteB deliberately missing.
			},
		},
	}
	if _, err := AllocateIPs("dev", spec); err == nil {
		t.Fatal("expected an error for a node on a context with no network block")
	}
}

// PerContext takes precedence over the single-range path; absent PerContext,
// AllocateIPs behaves exactly as before (guarded by the existing tests).
func TestAllocateIPs_PerContext_EmptyFallsBackToSingleRange(t *testing.T) {
	spec := &ClusterSpec{
		Nodes:   NodesSpec{ControlPlane: ControlPlaneSpec{Count: 2}},
		Network: NetworkSpec{StaticIPs: &StaticIPSpec{StartIP: "192.168.1.100"}},
		// No PerContext.
	}
	ips, err := AllocateIPs("dev", spec)
	if err != nil {
		t.Fatalf("AllocateIPs: %v", err)
	}
	if ips["dev-cp-0"] != "192.168.1.100" || ips["dev-cp-1"] != "192.168.1.101" {
		t.Errorf("single-range allocation changed: %v", ips)
	}
}
