package k3s

import "testing"

func TestK8sDeleteNodeCommand(t *testing.T) {
	got := k8sDeleteNodeCommand("dev-w-0")
	want := "k3s kubectl delete node dev-w-0 --ignore-not-found=true"
	if got != want {
		t.Errorf("k8sDeleteNodeCommand = %q, want %q", got, want)
	}
}
