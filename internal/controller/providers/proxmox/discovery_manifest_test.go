package proxmox

import (
	"context"
	"fmt"
	"testing"

	"github.com/openctl/openctl/internal/schema"
)

func TestDiscoveryOffersSchemaValidObservedAdoptionIntent(t *testing.T) {
	for _, tc := range []struct {
		name, tag string
		adoptable bool
	}{
		{name: "untagged", adoptable: true},
		{name: "tagged", tag: ",tag=150", adoptable: true},
		{name: "unsupported tag", tag: ",tag=5000", adoptable: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := mockProxmox(t, map[string]string{
				"/api2/json/nodes":                     `{"data":[{"node":"pve"}]}`,
				"/api2/json/nodes/pve/qemu":            `{"data":[{"vmid":101,"name":"media-vm","status":"stopped"}]}`,
				"/api2/json/nodes/pve/qemu/101/config": fmt.Sprintf(`{"data":{"cores":2,"memory":"4096","net0":"virtio=02:00:00:00:00:01,bridge=vmbr0%s","smbios1":"uuid=11111111-1111-4111-8111-111111111111"}}`, tc.tag),
			})
			defer srv.Close()
			p := mustNewMultiProvider(t, map[string]*Config{"lab": {Endpoint: srv.URL, CAFile: fixtureCA(t, srv)}}, "lab")
			found, err := p.Discover(context.Background(), "lab")
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != 1 || found[0].Adoptable != tc.adoptable {
				t.Fatalf("inventory=%+v", found)
			}
			if tc.adoptable {
				if err := schema.Validate(found[0].Resource); err != nil {
					t.Fatalf("discovered intent cannot be adopted: %v", err)
				}
			} else if found[0].Reason == "" {
				t.Fatal("unsupported settings need an actionable adoption reason")
			}
		})
	}
}
