package graph

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildServiceSelectorEdgesAndProblem(t *testing.T) {
	service := testObject("Service", "default", "api", "svc-1", map[string]any{
		"spec": map[string]any{
			"selector": map[string]any{"app": "api"},
		},
	})
	pod := testObject("Pod", "default", "api-abc", "pod-1", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"app": "api"}},
	})
	emptyService := testObject("Service", "default", "empty", "svc-2", map[string]any{
		"spec": map[string]any{
			"selector": map[string]any{"app": "missing"},
		},
	})

	g := Build([]Object{service, pod, emptyService})

	if !hasEdge(g.Edges, service.Ref, pod.Ref, "Selects") {
		t.Fatalf("expected Service to select matching Pod, got %#v", g.Edges)
	}
	if !hasProblem(g.Problems, emptyService.Ref, "Service selector matches zero Pods.") {
		t.Fatalf("expected zero-pod service problem, got %#v", g.Problems)
	}
}

func TestBuildPodReferenceEdgesAndMissingRefs(t *testing.T) {
	secret := testObject("Secret", "default", "api-secret", "secret-1", nil)
	pvc := testObject("PersistentVolumeClaim", "default", "data", "pvc-1", map[string]any{
		"status": map[string]any{"phase": "Pending"},
	})
	pod := testObject("Pod", "default", "api", "pod-1", map[string]any{
		"spec": map[string]any{
			"serviceAccountName": "missing-sa",
			"volumes": []any{
				map[string]any{"name": "cfg", "configMap": map[string]any{"name": "missing-config"}},
				map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "data"}},
			},
			"containers": []any{
				map[string]any{
					"name": "api",
					"env": []any{
						map[string]any{
							"name": "PASSWORD",
							"valueFrom": map[string]any{
								"secretKeyRef": map[string]any{"name": "api-secret", "key": "password"},
							},
						},
					},
				},
			},
		},
	})

	g := Build([]Object{secret, pvc, pod})

	if !hasEdge(g.Edges, pod.Ref, secret.Ref, "UsesEnv") {
		t.Fatalf("expected Pod to reference Secret through env, got %#v", g.Edges)
	}
	if !hasEdge(g.Edges, pod.Ref, pvc.Ref, "Mounts") {
		t.Fatalf("expected Pod to mount PVC, got %#v", g.Edges)
	}
	if !hasProblem(g.Problems, pod.Ref, "Pod/api references missing ConfigMap/missing-config.") {
		t.Fatalf("expected missing ConfigMap problem, got %#v", g.Problems)
	}
	if !hasProblem(g.Problems, pvc.Ref, "PVC phase is Pending, not Bound.") {
		t.Fatalf("expected unbound PVC problem, got %#v", g.Problems)
	}
}

func testObject(kind, namespace, name, uid string, extra map[string]any) Object {
	raw := map[string]any{
		"apiVersion": "v1",
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"uid":       uid,
		},
	}
	merge(raw, extra)
	u := &unstructured.Unstructured{Object: raw}
	return Object{
		Ref: ObjectRef{
			Kind:      kind,
			Namespace: namespace,
			Name:      name,
			UID:       types.UID(uid),
		},
		Labels: u.GetLabels(),
		Raw:    u,
	}
}

func merge(dst, src map[string]any) {
	for key, value := range src {
		if dstChild, ok := dst[key].(map[string]any); ok {
			if srcChild, ok := value.(map[string]any); ok {
				merge(dstChild, srcChild)
				continue
			}
		}
		dst[key] = value
	}
}

func hasEdge(edges []Edge, from, to ObjectRef, typ string) bool {
	for _, edge := range edges {
		if edge.From.Key() == from.Key() && edge.To.Key() == to.Key() && edge.Type == typ {
			return true
		}
	}
	return false
}

func hasProblem(problems []Problem, ref ObjectRef, message string) bool {
	for _, problem := range problems {
		if problem.Object.Key() == ref.Key() && problem.Message == message {
			return true
		}
	}
	return false
}
