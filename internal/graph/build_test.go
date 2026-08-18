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

func TestBuildOLMChainEdges(t *testing.T) {
	sub := testObject("Subscription", "operators", "billing-operator", "sub-1", map[string]any{
		"status": map[string]any{
			"installPlanRef": map[string]any{"name": "install-x7f2k", "namespace": "operators"},
			"installedCSV":   "billing-operator.v1.4.2",
		},
	})
	plan := testObject("InstallPlan", "operators", "install-x7f2k", "plan-1", map[string]any{
		"spec": map[string]any{
			"clusterServiceVersionNames": []any{"billing-operator.v1.4.2"},
		},
	})
	csv := testObject("ClusterServiceVersion", "operators", "billing-operator.v1.4.2", "csv-1", nil)
	unresolved := testObject("Subscription", "operators", "pending-operator", "sub-2", nil)

	g := Build([]Object{sub, plan, csv, unresolved})

	if !hasEdge(g.Edges, sub.Ref, plan.Ref, "Resolves") {
		t.Fatalf("expected Subscription to resolve InstallPlan, got %#v", g.Edges)
	}
	if !hasEdge(g.Edges, sub.Ref, csv.Ref, "Installs") {
		t.Fatalf("expected Subscription to install CSV, got %#v", g.Edges)
	}
	if !hasEdge(g.Edges, plan.Ref, csv.Ref, "Installs") {
		t.Fatalf("expected InstallPlan to install CSV, got %#v", g.Edges)
	}
	if len(g.ProblemsFor(unresolved.Ref)) != 0 {
		t.Fatalf("expected no problems for a Subscription with empty status fields, got %#v", g.ProblemsFor(unresolved.Ref))
	}
}

func TestBuildArgoApplicationEdges(t *testing.T) {
	app := testObject("Application", "argocd", "billing-app", "app-1", nil)
	managed := testObject("Deployment", "argocd", "billing-app", "deploy-1", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"argocd.argoproj.io/instance": "billing-app"}},
	})
	unlabeled := testObject("Deployment", "argocd", "other-app", "deploy-2", nil)
	orphanLabel := testObject("Deployment", "argocd", "orphan", "deploy-3", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"argocd.argoproj.io/instance": "no-such-application"}},
	})

	g := Build([]Object{app, managed, unlabeled, orphanLabel})

	if !hasEdge(g.Edges, app.Ref, managed.Ref, "Manages") {
		t.Fatalf("expected Application to manage labeled Deployment, got %#v", g.Edges)
	}
	if hasEdge(g.Edges, app.Ref, unlabeled.Ref, "Manages") {
		t.Fatalf("did not expect an edge to an unlabeled Deployment, got %#v", g.Edges)
	}
	if len(g.EdgesFor(orphanLabel.Ref)) != 0 {
		t.Fatalf("expected no edges for a label with no matching Application, got %#v", g.EdgesFor(orphanLabel.Ref))
	}
	if len(g.ProblemsFor(orphanLabel.Ref)) != 0 {
		t.Fatalf("expected no problem raised for an unresolved argocd.argoproj.io/instance label, got %#v", g.ProblemsFor(orphanLabel.Ref))
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
