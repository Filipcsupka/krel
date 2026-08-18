package kube

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIsBenignMissingResource(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "operators.coreos.com", Resource: "subscriptions"}, "")
	genericMessage := errors.New(`the server could not find the requested resource (get installplans.operators.coreos.com)`)

	cases := []struct {
		name   string
		kind   string
		err    error
		benign bool
	}{
		{"optional kind, well-formed not-found", "Subscription", notFound, true},
		{"optional kind, message-shaped not-found", "InstallPlan", genericMessage, true},
		{"optional kind, unrelated error", "ClusterServiceVersion", apierrors.NewForbidden(schema.GroupResource{Resource: "clusterserviceversions"}, "", nil), false},
		{"non-optional kind, not-found", "Pod", notFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBenignMissingResource(tc.kind, tc.err); got != tc.benign {
				t.Fatalf("isBenignMissingResource(%q, %v) = %v, want %v", tc.kind, tc.err, got, tc.benign)
			}
		})
	}
}

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
