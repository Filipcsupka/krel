package kube

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKubeconfigShortcut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	kubeDir := filepath.Join(home, ".kube")
	if err := os.MkdirAll(kubeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pft := filepath.Join(kubeDir, "pft")
	if err := os.WriteFile(pft, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := ResolveKubeconfig("pft"); got != pft {
		t.Fatalf("expected shortcut to resolve to %q, got %q", pft, got)
	}
	if got := ResolveKubeconfig("missing"); got != "missing" {
		t.Fatalf("expected missing shortcut to pass through, got %q", got)
	}
}
