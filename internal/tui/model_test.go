package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/filipcsupka/krel/internal/graph"
	"github.com/filipcsupka/krel/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestNamespaceCommandOpensPicker(t *testing.T) {
	m := testModel()

	updated, cmd := m.runCommand("ns")
	if cmd != nil {
		t.Fatal("expected namespace picker to open without reload command")
	}
	got := updated.(model)
	if !got.namespacePicker {
		t.Fatal("expected namespace picker mode")
	}
	if got.list.Title != "config: default  ctx: test-context  namespaces" {
		t.Fatalf("unexpected list title: %q", got.list.Title)
	}
}

func TestResourceAliasFiltersList(t *testing.T) {
	m := testModel()

	updated, cmd := m.runCommand("po")
	if cmd != nil {
		t.Fatal("expected resource alias not to stream logs while log view is closed")
	}
	got := updated.(model)
	if got.resourceKind != "Pod" {
		t.Fatalf("expected Pod filter, got %q", got.resourceKind)
	}
	if got.list.Title != "config: default  ctx: test-context  ns: app  kind: Pod" {
		t.Fatalf("unexpected list title: %q", got.list.Title)
	}
	if got.list.Items()[0].(item).obj.Ref.Kind != "Pod" {
		t.Fatal("expected pod item after filtering")
	}
}

func TestWorkloadAliases(t *testing.T) {
	cases := map[string]string{
		"sts": "StatefulSet",
		"ds":  "DaemonSet",
		"job": "Job",
		"cj":  "CronJob",
	}
	for command, want := range cases {
		got, ok := resourceAlias(command)
		if !ok {
			t.Fatalf("expected %q alias", command)
		}
		if got != want {
			t.Fatalf("expected %q alias to resolve to %q, got %q", command, want, got)
		}
	}
}

func TestAgeSortOrdersResources(t *testing.T) {
	oldPod := testObject("Pod", "app", "old")
	oldPod.Raw.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-2 * time.Hour)))
	newPod := testObject("Pod", "app", "new")
	newPod.Raw.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-10 * time.Minute)))
	snapshot := kube.Snapshot{
		Context:   "test-context",
		Namespace: "app",
		Graph:     graph.New([]graph.Object{oldPod, newPod}, nil, nil),
	}

	newest := newResourceList(snapshot, "Pod", "", "age-desc")
	if got := newest.Items()[0].(item).obj.Ref.Name; got != "new" {
		t.Fatalf("expected newest first, got %q", got)
	}
	oldest := newResourceList(snapshot, "Pod", "", "age-asc")
	if got := oldest.Items()[0].(item).obj.Ref.Name; got != "old" {
		t.Fatalf("expected oldest first, got %q", got)
	}
}

func TestAgeSortCycle(t *testing.T) {
	if got := nextAgeSortMode(""); got != "age-desc" {
		t.Fatalf("expected age-desc, got %q", got)
	}
	if got := nextAgeSortMode("age-desc"); got != "age-asc" {
		t.Fatalf("expected age-asc, got %q", got)
	}
	if got := nextAgeSortMode("age-asc"); got != "" {
		t.Fatalf("expected default sort, got %q", got)
	}
}

func TestRelationsSummaryUsesCompactDirectRefs(t *testing.T) {
	pod := testObject("Pod", "app", "api")
	secret := testObject("Secret", "app", "api-secret")
	config := testObject("ConfigMap", "app", "api-config")
	pvc := testObject("PersistentVolumeClaim", "app", "api-data")
	g := graph.New(
		[]graph.Object{pod, secret, config, pvc},
		[]graph.Edge{
			{From: pod.Ref, To: secret.Ref, Type: "UsesEnv"},
			{From: pod.Ref, To: config.Ref, Type: "Mounts"},
			{From: pod.Ref, To: pvc.Ref, Type: "Mounts"},
		},
		nil,
	)

	summary := relationsSummaryView(g, pod)
	for _, want := range []string{
		"secret: api-secret",
		"configmap: api-config",
		"pvc: api-data",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected %q in summary:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "Reason:") || strings.Contains(summary, "->") {
		t.Fatalf("expected compact refs only, got:\n%s", summary)
	}
}

func TestPodStatusColors(t *testing.T) {
	failed := testObjectWithRaw("Pod", "app", "failed", map[string]any{
		"status": map[string]any{
			"phase": "Failed",
			"containerStatuses": []any{
				map[string]any{"name": "job", "ready": false, "restartCount": int64(0), "state": map[string]any{"terminated": map[string]any{"reason": "Error"}}},
			},
		},
	})
	completed := testObjectWithRaw("Pod", "app", "done", map[string]any{
		"status": map[string]any{
			"phase": "Succeeded",
			"containerStatuses": []any{
				map[string]any{"name": "job", "ready": false, "restartCount": int64(0), "state": map[string]any{"terminated": map[string]any{"reason": "Completed"}}},
			},
		},
	})

	if _, health := resourceStatus(failed); health != "bad" {
		t.Fatalf("expected failed pod to be bad, got %q", health)
	}
	if _, health := resourceStatus(completed); health != "neutral" {
		t.Fatalf("expected completed pod to be neutral, got %q", health)
	}
}

func TestGenericConditionStatusUnderstandsPositiveAndNegativePolarity(t *testing.T) {
	operator := testObjectWithRaw("ClusterOperator", "", "console", map[string]any{
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Available", "status": "True"},
			map[string]any{"type": "Progressing", "status": "False"},
			map[string]any{"type": "Degraded", "status": "False"},
		}},
	})
	status, health := resourceStatus(operator)
	if status != "Available" || health != "good" {
		t.Fatalf("expected healthy ClusterOperator status, got status=%q health=%q", status, health)
	}

	degraded := testObjectWithRaw("ClusterOperator", "", "storage", map[string]any{
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Available", "status": "True"},
			map[string]any{"type": "Degraded", "status": "True", "reason": "OperatorDown"},
		}},
	})
	status, health = resourceStatus(degraded)
	if status != "Degraded True OperatorDown" || health != "bad" {
		t.Fatalf("expected degraded status, got status=%q health=%q", status, health)
	}
}

func TestQuitCommandReturnsTeaQuit(t *testing.T) {
	m := testModel()

	_, cmd := m.runCommand("q")
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestOwnerChainWalksOwnerReferences(t *testing.T) {
	deploy := testObject("Deployment", "app", "api")
	deploy.Ref.UID = "deploy-uid"
	deploy.Raw.SetUID(deploy.Ref.UID)

	rs := testObject("ReplicaSet", "app", "api-1")
	rs.Ref.UID = "rs-uid"
	rs.Raw.SetUID(rs.Ref.UID)
	rs.Raw.SetOwnerReferences([]metav1.OwnerReference{{Kind: "Deployment", Name: "api", UID: deploy.Ref.UID}})

	pod := testObject("Pod", "app", "api-1-xyz")
	pod.Raw.SetOwnerReferences([]metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-1", UID: rs.Ref.UID}})

	g := graph.Build([]graph.Object{deploy, rs, pod})
	podObj, _ := g.ObjectByKey(pod.Ref.Key())

	chain := ownerChain(g, podObj)
	if len(chain) != 3 {
		t.Fatalf("expected 3-object chain, got %d: %+v", len(chain), chain)
	}
	if chain[0].Ref.Kind != "Deployment" || chain[1].Ref.Kind != "ReplicaSet" || chain[2].Ref.Kind != "Pod" {
		t.Fatalf("expected Deployment -> ReplicaSet -> Pod order, got %s -> %s -> %s", chain[0].Ref.Kind, chain[1].Ref.Kind, chain[2].Ref.Kind)
	}
}

func TestOwnerChainPrependsArgoApplication(t *testing.T) {
	app := testObject("Application", "argocd", "billing-app")

	deploy := testObjectWithRaw("Deployment", "argocd", "billing-app", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"argocd.argoproj.io/instance": "billing-app"}},
	})
	deploy.Ref.UID = "deploy-uid"
	deploy.Raw.SetUID(deploy.Ref.UID)

	pod := testObject("Pod", "argocd", "billing-app-xyz")
	pod.Raw.SetOwnerReferences([]metav1.OwnerReference{{Kind: "Deployment", Name: "billing-app", UID: deploy.Ref.UID}})

	g := graph.Build([]graph.Object{app, deploy, pod})
	podObj, _ := g.ObjectByKey(pod.Ref.Key())

	chain := ownerChain(g, podObj)
	if len(chain) != 3 {
		t.Fatalf("expected 3-object chain, got %d: %+v", len(chain), chain)
	}
	if chain[0].Ref.Kind != "Application" || chain[0].Ref.Name != "billing-app" {
		t.Fatalf("expected chain to start with the Application, got %s/%s", chain[0].Ref.Kind, chain[0].Ref.Name)
	}
	if chain[1].Ref.Kind != "Deployment" || chain[2].Ref.Kind != "Pod" {
		t.Fatalf("expected Application -> Deployment -> Pod order, got %s -> %s -> %s", chain[0].Ref.Kind, chain[1].Ref.Kind, chain[2].Ref.Kind)
	}
}

func TestGitopsManagedByLine(t *testing.T) {
	obj := testObject("Deployment", "app", "api")
	obj.Raw.SetLabels(map[string]string{"argocd.argoproj.io/instance": "my-app"})
	line, ok := gitopsManagedByLine(obj)
	if !ok || line != "managed-by: argocd (app:my-app)" {
		t.Fatalf("expected argocd managed-by line, got %q ok=%v", line, ok)
	}

	none := testObject("Deployment", "app", "plain")
	if _, ok := gitopsManagedByLine(none); ok {
		t.Fatal("expected no managed-by line for unlabeled object")
	}
}

func TestLogScrollFollowsTailByDefaultAndKMovesIntoHistory(t *testing.T) {
	m := testModel()
	m.logsFullscreen = true
	m.logLines = []string{"line1", "line2", "line3"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	got := updated.(model)
	if got.logScroll != 1 {
		t.Fatalf("expected k to scroll into history (scroll=1), got %d", got.logScroll)
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	got = updated.(model)
	if got.logScroll != 0 {
		t.Fatalf("expected j to move back toward the live tail (scroll=0), got %d", got.logScroll)
	}
}

func TestLiveLogEventsFollowTailAndPreserveScrolledViewport(t *testing.T) {
	m := testModel()
	m.logsFullscreen = true
	m.logRequestID = m.currentLogRequestID()
	m.logLines = []string{"old-1", "old-2"}

	updated, _ := m.Update(logEventMsg{id: m.logRequestID, event: kube.LogEvent{Line: "live-1"}, open: true})
	got := updated.(model)
	if got.logScroll != 0 {
		t.Fatalf("expected live tail to remain pinned, got scroll=%d", got.logScroll)
	}
	if got.logLines[len(got.logLines)-1] != "live-1" {
		t.Fatalf("expected live line to append, got %#v", got.logLines)
	}

	got.logScroll = 2
	updated, _ = got.Update(logEventMsg{id: got.logRequestID, event: kube.LogEvent{Line: "live-2", Source: "api-1/main"}, open: true})
	got = updated.(model)
	if got.logScroll != 3 {
		t.Fatalf("expected scrolled viewport offset to grow as live lines arrive, got %d", got.logScroll)
	}
	if got.logLines[len(got.logLines)-1] != "[api-1/main] live-2" {
		t.Fatalf("expected merged stream source prefix, got %#v", got.logLines)
	}
}

func TestLogsStartOnlyWhenFullscreen(t *testing.T) {
	m := testModel()
	if cmd := m.Init(); cmd != nil {
		t.Fatal("expected no background log stream at startup")
	}
	if cmd := m.loadSelectedLogs(); cmd != nil {
		t.Fatal("expected no log stream while fullscreen logs are closed")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	got := updated.(model)
	if !got.logsFullscreen || cmd == nil {
		t.Fatalf("expected l to open logs and start a stream, fullscreen=%v cmdNil=%v", got.logsFullscreen, cmd == nil)
	}
}

func TestPausedLogsKeepBufferingWithoutMovingViewport(t *testing.T) {
	m := testModel()
	m.logsFullscreen = true
	m.logRequestID = m.currentLogRequestID()
	m.logLines = []string{"visible-tail"}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	got := updated.(model)
	if !got.logPaused {
		t.Fatal("expected logs to pause")
	}
	updated, _ = got.Update(logEventMsg{id: got.logRequestID, event: kube.LogEvent{Line: "buffered"}, open: true})
	got = updated.(model)
	if got.logScroll != 1 || got.logLines[len(got.logLines)-1] != "buffered" {
		t.Fatalf("expected paused viewport with buffered line, scroll=%d lines=%#v", got.logScroll, got.logLines)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeySpace})
	got = updated.(model)
	if got.logPaused || got.logScroll != 0 {
		t.Fatalf("expected resume to jump to live tail, paused=%v scroll=%d", got.logPaused, got.logScroll)
	}
}

func TestEnterAndEscapeMoveBetweenResourcesAndRelations(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	if got.active != paneRelations {
		t.Fatalf("expected enter to focus relations, got pane %d", got.active)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.active != paneResources {
		t.Fatalf("expected esc to return to resources, got pane %d", got.active)
	}
}

func TestRelationsPanelDeduplicatesDetailedReferences(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec": map[string]any{
			"serviceAccountName": "default",
			"volumes":            []any{map[string]any{"name": "cfg", "configMap": map[string]any{"name": "api-config"}}},
			"containers":         []any{map[string]any{"name": "api", "volumeMounts": []any{map[string]any{"name": "cfg", "mountPath": "/etc/api"}}}},
		},
	})
	config := testObjectWithRaw("ConfigMap", "app", "api-config", map[string]any{"data": map[string]any{"config.yaml": "value"}})
	serviceAccount := testObject("ServiceAccount", "app", "default")
	g := graph.Build([]graph.Object{pod, config, serviceAccount})
	pod, _ = g.ObjectByKey(pod.Ref.Key())
	lines := relationsPanelLines(g, pod)
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "configmap: api-config") {
			count++
			if !strings.Contains(line, "mount:/etc/api") {
				t.Fatalf("expected detailed mount usage, got %q", line)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected one deduplicated ConfigMap relation, got %d in %#v", count, lines)
	}
}

func TestDynamicResourceCompletion(t *testing.T) {
	m := testModel()
	m.snapshot.Resources = []kube.ResourceType{{
		GVR:  schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1beta2", Resource: "kafkatopics"},
		Kind: "KafkaTopic", Namespaced: true, ShortNames: []string{"kt"},
	}}
	for _, prefix := range []string{"kafkat", "kt"} {
		matches := m.resourceSuggestions(prefix)
		if len(matches) == 0 {
			t.Fatalf("expected dynamic completion for %q", prefix)
		}
	}
	if resource, ok := m.snapshot.ResolveResource("kt"); !ok || resource.Kind != "KafkaTopic" {
		t.Fatalf("expected API short name to resolve KafkaTopic, got %#v ok=%v", resource, ok)
	}
}

func TestDynamicResourceCommandLoadsDiscoveredKind(t *testing.T) {
	m := testModel()
	m.snapshot.Resources = []kube.ResourceType{{
		GVR:  schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1beta2", Resource: "kafkatopics"},
		Kind: "KafkaTopic", Namespaced: true, ShortNames: []string{"kt"},
	}}
	updated, cmd := m.runCommand("kt")
	got := updated.(model)
	if cmd == nil || !got.loading || got.resourceKind != "KafkaTopic" {
		t.Fatalf("expected dynamic kind reload, loading=%v kind=%q cmdNil=%v", got.loading, got.resourceKind, cmd == nil)
	}
}

func TestCompactLayoutShowsOnlyFocusedPane(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(model)
	view := got.View()
	if strings.Contains(view, "Owner Chain") || strings.Contains(view, "Relations") {
		t.Fatalf("expected compact resource-focused layout, got:\n%s", view)
	}
	got.active = paneRelations
	view = got.View()
	if !strings.Contains(view, "Relations") {
		t.Fatalf("expected compact relations pane, got:\n%s", view)
	}
}

func TestAllNamespaceCommandAndQualifiedRows(t *testing.T) {
	m := testModel()
	updated, cmd := m.runCommand("ns all")
	got := updated.(model)
	if cmd == nil || !got.loading {
		t.Fatal("expected :ns all to reload")
	}

	m.snapshot.Options.AllNamespaces = true
	m.list = newResourceList(m.snapshot, "Pod")
	if title := m.list.Items()[0].(item).Title(); title != "Pod/app/api" {
		t.Fatalf("expected namespace-qualified all-namespace row, got %q", title)
	}
}

func testModel() model {
	pod := testObject("Pod", "app", "api")
	service := testObject("Service", "app", "api")
	snapshot := kube.Snapshot{
		Context:     "test-context",
		Namespace:   "app",
		Namespaces:  []string{"app", "default"},
		Graph:       graph.New([]graph.Object{pod, service}, nil, nil),
		LoadedKinds: map[string]bool{"Pod": true, "Service": true},
	}
	return model{snapshot: snapshot, list: newResourceList(snapshot), mode: "relations"}
}

func testObject(kind, namespace, name string) graph.Object {
	return testObjectWithRaw(kind, namespace, name, nil)
}

func testObjectWithRaw(kind, namespace, name string, extra map[string]any) graph.Object {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("v1")
	obj.SetKind(kind)
	obj.SetNamespace(namespace)
	obj.SetName(name)
	for key, value := range extra {
		obj.Object[key] = value
	}
	return graph.Object{
		Ref: graph.ObjectRef{
			Kind:      kind,
			Namespace: namespace,
			Name:      name,
		},
		Raw: obj,
	}
}
