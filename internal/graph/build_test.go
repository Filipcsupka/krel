package graph

import (
	"strings"
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

func TestBuildArgoApplicationEdgesAcrossDestinationNamespace(t *testing.T) {
	app := testObject("Application", "argocd", "billing", "app-1", map[string]any{
		"spec": map[string]any{"destination": map[string]any{"namespace": "billing-prod"}},
	})
	managed := testObject("Deployment", "billing-prod", "api", "deploy-1", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"argocd.argoproj.io/instance": "billing"}},
	})
	wrongNamespace := testObject("Deployment", "other", "api", "deploy-2", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"argocd.argoproj.io/instance": "billing"}},
	})

	g := Build([]Object{app, managed, wrongNamespace})
	if !hasEdge(g.Edges, app.Ref, managed.Ref, "Manages") {
		t.Fatalf("expected cross-namespace Argo destination edge, got %#v", g.Edges)
	}
	if hasEdge(g.Edges, app.Ref, wrongNamespace.Ref, "Manages") {
		t.Fatalf("did not expect Argo edge outside destination namespace, got %#v", g.Edges)
	}
}

func TestBuildWorkloadTemplateAndRBACReferences(t *testing.T) {
	secret := testObject("Secret", "app", "api-secret", "secret-1", nil)
	serviceAccount := testObject("ServiceAccount", "app", "api", "sa-1", nil)
	role := testObject("Role", "app", "reader", "role-1", nil)
	deployment := testObject("Deployment", "app", "api", "deploy-1", map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"serviceAccountName": "api",
			"containers": []any{map[string]any{
				"name":    "api",
				"envFrom": []any{map[string]any{"secretRef": map[string]any{"name": "api-secret"}}},
			}},
		}}},
	})
	binding := testObject("RoleBinding", "app", "api-reader", "binding-1", map[string]any{
		"roleRef":  map[string]any{"kind": "Role", "name": "reader"},
		"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "api", "namespace": "app"}},
	})

	g := Build([]Object{secret, serviceAccount, role, deployment, binding})
	for _, want := range []struct {
		from, to ObjectRef
		typ      string
	}{
		{deployment.Ref, secret.Ref, "UsesEnv"},
		{deployment.Ref, serviceAccount.Ref, "UsesServiceAccount"},
		{binding.Ref, role.Ref, "GrantsRole"},
		{binding.Ref, serviceAccount.Ref, "GrantsTo"},
	} {
		if !hasEdge(g.Edges, want.from, want.to, want.typ) {
			t.Fatalf("expected %s edge %s -> %s, got %#v", want.typ, want.from.Key(), want.to.Key(), g.Edges)
		}
	}
}

func TestBuildGenericCRDReferencesAndConditions(t *testing.T) {
	secret := testObject("Secret", "app", "database-auth", "secret-1", nil)
	custom := testObject("Database", "app", "primary", "db-1", map[string]any{
		"spec": map[string]any{"credentials": map[string]any{"secretRef": map[string]any{"name": "database-auth"}}},
		"status": map[string]any{"conditions": []any{map[string]any{
			"type": "Ready", "status": "False", "reason": "AuthFailed", "message": "credentials rejected",
		}}},
	})

	g := Build([]Object{secret, custom})
	if !hasEdge(g.Edges, custom.Ref, secret.Ref, "References") {
		t.Fatalf("expected generic CRD Secret reference, got %#v", g.Edges)
	}
	if !hasProblem(g.Problems, custom.Ref, "Ready False AuthFailed: credentials rejected.") {
		t.Fatalf("expected generic negative condition problem, got %#v", g.Problems)
	}
}

func TestSecretYAMLRedactsPayloads(t *testing.T) {
	secret := testObject("Secret", "app", "credentials", "secret-1", map[string]any{
		"data":       map[string]any{"password": "c3VwZXJzZWNyZXQ="},
		"stringData": map[string]any{"token": "plain-secret"},
	})
	yaml := secret.YAML()
	if strings.Contains(yaml, "c3VwZXJzZWNyZXQ=") || strings.Contains(yaml, "plain-secret") {
		t.Fatalf("expected Secret payloads to be redacted, got:\n%s", yaml)
	}
	if strings.Count(yaml, "<redacted>") != 2 {
		t.Fatalf("expected both Secret fields to retain redacted keys, got:\n%s", yaml)
	}
}

func TestGroupAwareReferencesDisambiguateSameKindCRDs(t *testing.T) {
	aws := testObject("ProviderConfig", "", "default", "aws-1", nil)
	aws.Ref.Group = "aws.example.io"
	aws.Raw.SetAPIVersion("aws.example.io/v1")
	vault := testObject("ProviderConfig", "", "default", "vault-1", nil)
	vault.Ref.Group = "vault.example.io"
	vault.Raw.SetAPIVersion("vault.example.io/v1")
	consumer := testObject("Database", "app", "primary", "db-1", map[string]any{
		"spec": map[string]any{"providerConfigRef": map[string]any{
			"apiGroup": "vault.example.io", "kind": "ProviderConfig", "name": "default",
		}},
	})

	g := Build([]Object{aws, vault, consumer})
	if !hasEdge(g.Edges, consumer.Ref, vault.Ref, "References") {
		t.Fatalf("expected group-qualified reference to Vault ProviderConfig, got %#v", g.Edges)
	}
	if hasEdge(g.Edges, consumer.Ref, aws.Ref, "References") {
		t.Fatalf("did not expect reference to same-kind AWS ProviderConfig, got %#v", g.Edges)
	}
	if _, ok := g.ObjectByKey((ObjectRef{Kind: "ProviderConfig", Name: "default"}).Key()); ok {
		t.Fatal("expected group-less lookup to reject ambiguous same-kind CRDs")
	}
}

func TestServiceMonitorSelectsAcrossConfiguredNamespaces(t *testing.T) {
	monitor := testObject("ServiceMonitor", "monitoring", "apps", "sm-1", map[string]any{
		"spec": map[string]any{
			"selector":          map[string]any{"matchLabels": map[string]any{"metrics": "enabled"}},
			"namespaceSelector": map[string]any{"matchNames": []any{"app-b"}},
		},
	})
	serviceA := testObject("Service", "monitoring", "local", "svc-1", map[string]any{"metadata": map[string]any{"labels": map[string]any{"metrics": "enabled"}}})
	serviceB := testObject("Service", "app-b", "remote", "svc-2", map[string]any{"metadata": map[string]any{"labels": map[string]any{"metrics": "enabled"}}})

	g := Build([]Object{monitor, serviceA, serviceB})
	if !hasEdge(g.Edges, monitor.Ref, serviceB.Ref, "Monitors") {
		t.Fatalf("expected cross-namespace ServiceMonitor edge, got %#v", g.Edges)
	}
	if hasEdge(g.Edges, monitor.Ref, serviceA.Ref, "Monitors") {
		t.Fatalf("did not expect local Service outside matchNames, got %#v", g.Edges)
	}
}

func TestImpactAndProblemPath(t *testing.T) {
	secret := testObject("Secret", "app", "credentials", "secret-1", nil)
	serviceAccount := testObject("ServiceAccount", "app", "default", "sa-1", nil)
	deploy := testObject("Deployment", "app", "api", "deploy-1", nil)
	pod := testObject("Pod", "app", "api-1", "pod-1", map[string]any{
		"metadata": map[string]any{"ownerReferences": []any{
			map[string]any{"uid": "deploy-1", "kind": "Deployment", "name": "api"},
		}},
		"spec": map[string]any{
			"serviceAccountName": "default",
			"containers": []any{
				map[string]any{"name": "api", "envFrom": []any{
					map[string]any{"secretRef": map[string]any{"name": "credentials"}},
				}},
			},
		},
		"status": map[string]any{"phase": "Failed"},
	})
	g := Build([]Object{secret, serviceAccount, deploy, pod})

	impact := g.ImpactFor(secret.Ref, 4)
	if !containsRef(impact, pod.Ref) || !containsRef(impact, deploy.Ref) {
		t.Fatalf("expected Secret impact to include Pod and owning Deployment, got %#v", impact)
	}
	path, problem, ok := g.ProblemPath(deploy.Ref, 4)
	if !ok || len(path) != 2 || path[1].Key() != pod.Ref.Key() || problem.Message != "Pod phase is Failed." {
		t.Fatalf("expected Deployment -> failed Pod cause path, got path=%#v problem=%#v ok=%v", path, problem, ok)
	}
}

func containsRef(refs []ObjectRef, want ObjectRef) bool {
	for _, ref := range refs {
		if ref.Key() == want.Key() {
			return true
		}
	}
	return false
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
