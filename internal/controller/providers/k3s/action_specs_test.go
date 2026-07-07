package k3s

import "testing"

// ActionSpecs declares the Cluster actions with their parameter schemas so the
// UI can render inputs. `upgrade` takes a required `version`; `get-kubeconfig`
// is parameterless. The names must match Actions.
func TestActionSpecs_ClusterUpgradeDeclaresVersionParam(t *testing.T) {
	p := &Provider{}

	specs := p.ActionSpecs(kindCluster)
	byName := map[string]int{}
	for i, s := range specs {
		byName[s.Name] = i
	}

	// Every declared name must also appear in Actions (contract).
	for _, name := range p.Actions(kindCluster) {
		if _, ok := byName[name]; !ok {
			t.Errorf("Actions lists %q but ActionSpecs does not", name)
		}
	}

	up, ok := byName["upgrade"]
	if !ok {
		t.Fatal("no upgrade spec")
	}
	params := specs[up].Parameters
	if len(params) != 1 {
		t.Fatalf("upgrade has %d params, want 1", len(params))
	}
	if params[0].Name != "version" || params[0].Type != "string" || !params[0].Required {
		t.Errorf("upgrade param = %+v, want required string 'version'", params[0])
	}

	// get-kubeconfig is parameterless.
	if kc, ok := byName["get-kubeconfig"]; ok {
		if len(specs[kc].Parameters) != 0 {
			t.Errorf("get-kubeconfig should have no params, got %+v", specs[kc].Parameters)
		}
	}
}

// Non-Cluster kinds have no actions.
func TestActionSpecs_NonClusterIsEmpty(t *testing.T) {
	p := &Provider{}
	if specs := p.ActionSpecs(kindK3sNode); specs != nil {
		t.Errorf("K3sNode ActionSpecs = %+v, want nil", specs)
	}
}
