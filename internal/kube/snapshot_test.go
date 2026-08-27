package kube

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func TestObjectAndResourcePreserveAPIGroupIdentity(t *testing.T) {
	item := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kafka.strimzi.io/v1beta2",
		"kind":       "KafkaTopic",
		"metadata":   map[string]any{"name": "orders", "namespace": "kafka"},
	}}
	obj := objectFromUnstructured("KafkaTopic", item)
	if obj.Ref.Group != "kafka.strimzi.io" {
		t.Fatalf("expected object API group identity, got %#v", obj.Ref)
	}
	resource := ResourceType{GVR: schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1beta2", Resource: "kafkatopics"}, Kind: "KafkaTopic"}
	if !resourceMatches(resource, "kafkatopics.kafka.strimzi.io") {
		t.Fatal("expected fully qualified resource command to match")
	}
}

func TestResolveResourceRequiresGroupWhenKindIsAmbiguous(t *testing.T) {
	snapshot := Snapshot{Resources: []ResourceType{
		{GVR: schema.GroupVersionResource{Group: "aws.example.io", Version: "v1", Resource: "providerconfigs"}, Kind: "ProviderConfig"},
		{GVR: schema.GroupVersionResource{Group: "vault.example.io", Version: "v1", Resource: "providerconfigs"}, Kind: "ProviderConfig"},
	}}
	if _, ok := snapshot.ResolveResource("ProviderConfig"); ok {
		t.Fatal("expected ambiguous kind to require an API group")
	}
	resource, ok := snapshot.ResolveResource("providerconfigs.vault.example.io")
	if !ok || resource.GVR.Group != "vault.example.io" {
		t.Fatalf("expected fully qualified Vault ProviderConfig, got %#v ok=%v", resource, ok)
	}
}

func TestResourceTypesFromDiscoveryAndRequestedKind(t *testing.T) {
	lists := []*metav1.APIResourceList{{
		GroupVersion: "example.io/v1",
		APIResources: []metav1.APIResource{
			{Name: "widgets", Kind: "Widget", Namespaced: true, ShortNames: []string{"wd"}, Verbs: metav1.Verbs{"get", "list"}},
			{Name: "widgets/status", Kind: "Widget", Namespaced: true, Verbs: metav1.Verbs{"get"}},
			{Name: "writeonly", Kind: "WriteOnly", Namespaced: true, Verbs: metav1.Verbs{"create"}},
		},
	}}
	resources := resourceTypesFromLists(lists)
	if len(resources) != 1 || resources[0].Kind != "Widget" {
		t.Fatalf("expected only listable top-level Widget, got %#v", resources)
	}
	for _, query := range []string{"Widget", "widgets", "wd"} {
		if !resourceMatches(resources[0], query) {
			t.Fatalf("expected %q to resolve Widget", query)
		}
	}
	defs := snapshotResourceDefs(resources, "wd", false)
	if len(defs) != 1 || defs[0].kind != "Widget" || defs[0].gvr.Version != "v1" {
		t.Fatalf("expected requested discovered Widget definition, got %#v", defs)
	}
}

func TestAllNamespaceProfileLoadsOnlyRequestedNeighborhood(t *testing.T) {
	resources := []ResourceType{
		{GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, Kind: "Pod", Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}, Kind: "Secret", Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}, Kind: "Service", Namespaced: true},
		{GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Kind: "Deployment", Namespaced: true},
	}
	defs := snapshotResourceDefs(resources, "Secret", true)
	kinds := loadedKinds(defs)
	for _, want := range []string{"Secret", "Pod", "Deployment"} {
		if !kinds[want] {
			t.Fatalf("expected all-namespace Secret neighborhood to load %s, got %#v", want, kinds)
		}
	}
	if kinds["Service"] {
		t.Fatalf("did not expect unrelated Service collection in Secret neighborhood: %#v", kinds)
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
