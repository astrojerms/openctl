package k3s

import (
	"context"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestCheckedOwnershipRefusesCorruptClusterState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "damaged", "children: [invalid\n")
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if _, _, owned, err := p.CheckedOwnerOf(context.Background(), "VirtualMachine", "media"); err == nil || owned {
		t.Fatalf("corrupt ownership state must block deletion, owned=%v err=%v", owned, err)
	}
}
