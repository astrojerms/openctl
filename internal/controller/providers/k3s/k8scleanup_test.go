package k3s

import "testing"

func TestK8sDeleteNodeCommand(t *testing.T) {
	got := k8sDeleteNodeCommand("dev-w-0")
	want := "k3s kubectl delete node dev-w-0 --ignore-not-found=true"
	if got != want {
		t.Errorf("k8sDeleteNodeCommand = %q, want %q", got, want)
	}
}

func TestK8sDeleteNodePasswordSecretCommand(t *testing.T) {
	got := k8sDeleteNodePasswordSecretCommand("dev-w-0")
	want := "k3s kubectl delete secret dev-w-0.node-password.k3s -n kube-system --ignore-not-found=true"
	if got != want {
		t.Errorf("k8sDeleteNodePasswordSecretCommand = %q, want %q", got, want)
	}
}
