package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
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

func TestContextCommandOpensPicker(t *testing.T) {
	m := testModel()
	m.snapshot.Contexts = []string{"test-context", "remote"}
	updated, cmd := m.runCommand("ctx")
	got := updated.(model)
	if cmd != nil || !got.contextPicker || got.namespacePicker {
		t.Fatalf("expected context picker, context=%v namespace=%v cmdNil=%v", got.contextPicker, got.namespacePicker, cmd == nil)
	}
	if got.list.Title != "config: default  contexts" {
		t.Fatalf("unexpected context list title: %q", got.list.Title)
	}
	if _, ok := got.list.Items()[0].(contextItem); !ok {
		t.Fatalf("expected context list items, got %T", got.list.Items()[0])
	}
	updated, cmd = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(model)
	if cmd == nil || !got.loading || got.contextPicker {
		t.Fatalf("expected context selection to reload, loading=%v picker=%v cmdNil=%v", got.loading, got.contextPicker, cmd == nil)
	}
}

func TestWatchCommandsToggleLiveRefresh(t *testing.T) {
	m := testModel()
	updated, cmd := m.runCommand("watch")
	got := updated.(model)
	if !got.autoRefresh || cmd == nil {
		t.Fatalf("expected :watch to enable scheduled refresh, auto=%v cmdNil=%v", got.autoRefresh, cmd == nil)
	}
	updated, cmd = got.runCommand("nowatch")
	got = updated.(model)
	if got.autoRefresh || cmd != nil {
		t.Fatalf("expected :nowatch to disable refresh, auto=%v cmdNil=%v", got.autoRefresh, cmd == nil)
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
	if got.list.Title != "config: default  ctx: test-context  ns: app  kind: Pod [1]" {
		t.Fatalf("unexpected list title: %q", got.list.Title)
	}
	if got.list.Items()[0].(item).obj.Ref.Kind != "Pod" {
		t.Fatal("expected pod item after filtering")
	}
}

func TestResourceFilterValueIncludesAnnotations(t *testing.T) {
	obj := testObject("Pod", "app", "api")
	obj.Raw.SetAnnotations(map[string]string{"argocd.argoproj.io/tracking-id": "billing:apps/Deployment:app/api"})
	value := (item{obj: obj}).FilterValue()
	if !strings.Contains(value, "argocd.argoproj.io/tracking-id=billing:apps/Deployment:app/api") {
		t.Fatalf("expected annotations in resource filter value, got %q", value)
	}
}

func TestCommonKubernetesResourceAliases(t *testing.T) {
	want := map[string]string{
		"dp": "Deployment", "sec": "Secret", "pv": "PersistentVolume", "sc": "StorageClass",
		"rb": "RoleBinding", "cr": "ClusterRole", "crb": "ClusterRoleBinding", "app": "Application",
		"gw": "Gateway", "es": "ExternalSecret", "sm": "ServiceMonitor", "pm": "PodMonitor",
	}
	for alias, kind := range want {
		got, ok := resourceAlias(alias)
		if !ok || got != kind {
			t.Errorf("resourceAlias(%q) = %q, %v; want %q, true", alias, got, ok, kind)
		}
	}
}

func TestResourceCommandAcceptsNamespaceAndContextArguments(t *testing.T) {
	m := testModel()
	updated, cmd := m.runCommand("pod other")
	got := updated.(model)
	if cmd == nil || !got.loading {
		t.Fatal("expected namespace-qualified resource command to reload")
	}

	m = testModel()
	updated, cmd = m.runCommand("pod @remote")
	got = updated.(model)
	if cmd == nil || !got.loading {
		t.Fatal("expected context-qualified resource command to reload")
	}
}

func TestResourceCommandAcceptsInlineFilter(t *testing.T) {
	m := testModel()
	updated, cmd := m.runCommand("pod /api")
	got := updated.(model)
	if cmd != nil || got.resourceFilter != "api" || got.list.FilterState() != list.FilterApplied {
		t.Fatalf("expected inline filter without reload, filter=%q state=%v cmdNil=%v", got.resourceFilter, got.list.FilterState(), cmd == nil)
	}
	if len(got.list.VisibleItems()) != 1 || got.list.VisibleItems()[0].(item).obj.Ref.Name != "api" {
		t.Fatalf("expected only api Pod after inline filter, visible=%#v", got.list.VisibleItems())
	}

	m = testModel()
	m.snapshot.Graph.Objects[0].Raw.SetLabels(map[string]string{"app": "backend", "env": "prod"})
	updated, cmd = m.runCommand("pod app=backend,env=prod")
	got = updated.(model)
	if cmd != nil || len(got.list.VisibleItems()) != 1 || got.list.VisibleItems()[0].(item).obj.Ref.Name != "api" {
		t.Fatalf("expected label selector to filter Pod list, filter=%q visible=%#v", got.resourceFilter, got.list.VisibleItems())
	}
}

func TestResourceTitleCountFollowsRegularFilter(t *testing.T) {
	m := testModel()
	worker := testObject("Pod", "app", "worker")
	m.snapshot.Graph = graph.Build(append(m.snapshot.Graph.Objects, worker))
	m.resourceKind = "Pod"
	m.list = newResourceList(m.snapshot, "Pod")
	updated, _ := m.runCommand("pod /api")
	got := updated.(model)
	if !strings.Contains(got.list.Title, "kind: Pod [1]") || len(got.list.VisibleItems()) != 1 {
		t.Fatalf("expected filtered count in title, title=%q visible=%d", got.list.Title, len(got.list.VisibleItems()))
	}
}

func TestResourceCommandSupportsLabelAndInverseFilters(t *testing.T) {
	m := testModel()
	m.snapshot.Graph.Objects[0].Raw.SetLabels(map[string]string{"app": "backend"})
	m.snapshot.Graph.Objects[1].Raw.SetLabels(map[string]string{"app": "frontend"})
	updated, cmd := m.runCommand("pod -l app=backend")
	got := updated.(model)
	if cmd != nil || got.resourceFilter != "-l app=backend" || len(got.list.Items()) != 1 {
		t.Fatalf("expected label selector to filter list, filter=%q items=%d cmdNil=%v", got.resourceFilter, len(got.list.Items()), cmd == nil)
	}

	m = testModel()
	worker := testObject("Pod", "app", "worker")
	m.snapshot.Graph = graph.Build(append(m.snapshot.Graph.Objects, worker))
	m.list = newResourceList(m.snapshot, "Pod")
	updated, cmd = m.runCommand("pod /!api")
	got = updated.(model)
	if cmd != nil || got.resourceFilter != "!api" || len(got.list.Items()) != 1 || got.list.Items()[0].(item).obj.Ref.Name != "worker" {
		t.Fatalf("expected inverse filter to exclude api, filter=%q items=%#v cmdNil=%v", got.resourceFilter, got.list.Items(), cmd == nil)
	}
}

func TestFuzzyResourceFilterStripsK9sPrefix(t *testing.T) {
	m := testModel()
	m.resourceKind = "Pod"
	m.active = paneResources
	m.list = newResourceList(m.snapshot, "Pod")
	m.list.SetFilterState(list.Filtering)
	for _, key := range []rune{'-', 'f', ' ', 'a', 'p'} {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m = updated.(model)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	if got.resourceFilter != "ap" {
		t.Fatalf("expected fuzzy prefix to be stripped, filter=%q", got.resourceFilter)
	}
	if len(got.list.VisibleItems()) != 1 || got.list.VisibleItems()[0].(item).obj.Ref.Name != "api" {
		t.Fatalf("expected fuzzy filter to keep api Pod, visible=%#v", got.list.VisibleItems())
	}
}

func TestInlineResourceCommandLabelSelectorFiltersMultiplePods(t *testing.T) {
	m := testModel()
	backend := testObject("Pod", "app", "backend")
	backend.Raw.SetLabels(map[string]string{"app": "backend", "env": "prod"})
	frontend := testObject("Pod", "app", "frontend")
	frontend.Raw.SetLabels(map[string]string{"app": "frontend", "env": "prod"})
	m.snapshot.Graph = graph.Build([]graph.Object{m.snapshot.Graph.Objects[0], backend, frontend})
	m.snapshot.LoadedKinds = map[string]bool{"Pod": true}
	m.list = newResourceList(m.snapshot, "Pod")

	updated, cmd := m.runCommand("pod app=backend,env=prod")
	got := updated.(model)
	if cmd != nil {
		t.Fatal("expected inline label selector not to reload")
	}
	if got.resourceFilter != "-l app=backend,env=prod" {
		t.Fatalf("expected normalized label selector, got %q", got.resourceFilter)
	}
	if len(got.list.Items()) != 1 || got.list.Items()[0].(item).obj.Ref.Name != "backend" {
		t.Fatalf("expected only backend Pod, items=%#v", got.list.Items())
	}
}

func TestResourceMarksSurviveRebuildAndCanBeCleared(t *testing.T) {
	m := testModel()
	m.active = paneResources
	m.list.Select(0)
	selected := m.selectedKey()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(model)
	if !m.marks[selected] {
		t.Fatalf("expected selected resource to be marked: %#v", m.marks)
	}
	markedItem, ok := m.list.SelectedItem().(item)
	if !ok || !markedItem.marked {
		t.Fatalf("expected rebuilt list item to retain mark: %#v", m.list.SelectedItem())
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlBackslash})
	m = updated.(model)
	if len(m.marks) != 0 || m.markAnchor != "" {
		t.Fatalf("expected ctrl-\\ to clear marks: marks=%#v anchor=%q", m.marks, m.markAnchor)
	}
}

func TestPodTableHeaderTracksTerminalWidth(t *testing.T) {
	m := testModel()
	m.resourceKind = "Pod"
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 220, Height: 60})
	m = updated.(model)
	if !strings.Contains(m.list.Title, "\nNAME") {
		t.Fatalf("expected wide Pod header, title=%q", m.list.Title)
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	if strings.Contains(m.list.Title, "\nNAME") {
		t.Fatalf("expected compact title without table header, title=%q", m.list.Title)
	}
}

func TestDeleteLastRunePreservesUTF8(t *testing.T) {
	value := "pod-čučoriedka🚀"
	got := deleteLastRune(value)
	if got != "pod-čučoriedka" {
		t.Fatalf("deleteLastRune(%q) = %q", value, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
}

func TestGlobalBracketNavigationReplaysCommandHistory(t *testing.T) {
	m := testModel()
	updated, _ := m.runCommand("help")
	m = updated.(model)
	updated, _ = m.runCommand("aliases")
	m = updated.(model)
	if len(m.commandHistory) != 2 {
		t.Fatalf("expected two history entries, got %#v", m.commandHistory)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = updated.(model)
	if m.mode != "help" || len(m.commandHistory) != 2 {
		t.Fatalf("expected [ to replay help without duplicating history: mode=%q history=%#v", m.mode, m.commandHistory)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	m = updated.(model)
	if m.mode != "aliases" || len(m.commandHistory) != 2 {
		t.Fatalf("expected ] to replay aliases without duplicating history: mode=%q history=%#v", m.mode, m.commandHistory)
	}
}

func TestOwnerChainCtrlFFiltersNavigableOwners(t *testing.T) {
	pod := testObject("Pod", "app", "api")
	rs := testObject("ReplicaSet", "app", "api-rs")
	deploy := testObject("Deployment", "app", "api")
	g := graph.New([]graph.Object{pod, rs, deploy}, []graph.Edge{
		{From: deploy.Ref, To: rs.Ref, Type: "Owns"},
		{From: rs.Ref, To: pod.Ref, Type: "Owns"},
	}, nil)
	m := model{
		snapshot:     kube.Snapshot{Context: "test", Namespace: "app", Graph: g},
		list:         newResourceList(kube.Snapshot{Context: "test", Namespace: "app", Graph: g}, "Pod"),
		resourceKind: "Pod",
		mode:         "relations",
		active:       paneChain,
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = updated.(model)
	if !m.chainSearchMode {
		t.Fatal("expected Ctrl-F to open owner chain search")
	}
	for _, r := range []rune("deploy") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.chainSearch != "deploy" || len(m.chainNavIndexes()) != 1 {
		t.Fatalf("expected only Deployment owner after search, search=%q indexes=%v", m.chainSearch, m.chainNavIndexes())
	}
	body := m.chainPanelBody(g, pod, 80, 10)
	if !strings.Contains(body, "deployment: api") {
		t.Fatalf("expected filtered chain body to contain Deployment, body=%q", body)
	}
}

func TestEscapeClearsAppliedResourceFilter(t *testing.T) {
	m := testModel()
	worker := testObject("Pod", "app", "worker")
	m.snapshot.Graph = graph.Build(append(m.snapshot.Graph.Objects, worker))
	m.resourceKind = "Pod"
	m.list = newResourceList(m.snapshot, "Pod")
	updated, _ := m.runCommand("pod /!api")
	got := updated.(model)
	if got.resourceFilter != "!api" || len(got.list.Items()) != 1 {
		t.Fatalf("expected inverse filter before escape, filter=%q items=%d", got.resourceFilter, len(got.list.Items()))
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.resourceFilter != "" || len(got.list.Items()) != 2 {
		t.Fatalf("expected esc to clear filter, filter=%q items=%d status=%q", got.resourceFilter, len(got.list.Items()), got.status)
	}
}

func TestReadOnlyViewCommandsOpenExpectedViews(t *testing.T) {
	cases := map[string]string{
		"help":       "help",
		"aliases":    "aliases",
		"yaml":       "yaml",
		"describe":   "details",
		"events":     "events",
		"problems":   "problems",
		"impact":     "impact",
		"loaderrors": "loaderrors",
	}
	for command, wantMode := range cases {
		m := testModel()
		updated, cmd := m.runCommand(command)
		got := updated.(model)
		if cmd != nil || got.mode != wantMode || got.active != paneRelations {
			t.Fatalf(":%s: mode=%q active=%d cmdNil=%v", command, got.mode, got.active, cmd == nil)
		}
	}
}

func TestReadOnlyK9sDisplayShortcuts(t *testing.T) {
	m := testModel()
	if m.hideHeader {
		t.Fatal("test model should start with the resource header visible")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	got := updated.(model)
	if !got.hideHeader || got.list.ShowTitle() {
		t.Fatalf("expected Ctrl-E to hide resource header, hide=%v show=%v", got.hideHeader, got.list.ShowTitle())
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	got = updated.(model)
	if got.hideHeader || !got.list.ShowTitle() {
		t.Fatalf("expected Ctrl-E to restore resource header, hide=%v show=%v", got.hideHeader, got.list.ShowTitle())
	}

	event := testObjectWithRaw("Event", "app", "warning", map[string]any{"type": "Warning"})
	got.snapshot.Graph = graph.New([]graph.Object{event}, nil, nil)
	got.snapshot.LoadedKinds = map[string]bool{"Event": true}
	got.resourceKind = "Event"
	got.warningsOnly = false
	got.list = newResourceList(got.snapshot, "Event")
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyCtrlZ})
	got = updated.(model)
	if !got.warningsOnly {
		t.Fatal("expected Ctrl-Z to enable warning-event filtering")
	}
}

func TestCtrlWEnablesWideResourceRows(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	got := updated.(model)
	if !got.wideList {
		t.Fatal("expected Ctrl-W to enable wide resource columns")
	}
	if len(got.list.Items()) == 0 || !got.list.Items()[0].(item).wide {
		t.Fatal("expected rebuilt resource rows to carry wide layout state")
	}
}

func TestCtrlGTogglesResourceBreadcrumbs(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	got := updated.(model)
	if !got.hideBreadcrumbs || !strings.HasPrefix(got.list.Title, "kind: ") {
		t.Fatalf("expected breadcrumbs to be hidden, hide=%v title=%q", got.hideBreadcrumbs, got.list.Title)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	got = updated.(model)
	if got.hideBreadcrumbs || !strings.Contains(got.list.Title, "ctx: test-context") {
		t.Fatalf("expected breadcrumbs to be restored, hide=%v title=%q", got.hideBreadcrumbs, got.list.Title)
	}
}

func TestCommandHistoryUsesBracketNavigation(t *testing.T) {
	m := testModel()
	updated, _ := m.runCommand("pod")
	m = updated.(model)
	updated, _ = m.runCommand("svc")
	m = updated.(model)
	m.commandMode = true
	m.command = ""
	m.historyCursor = len(m.commandHistory)

	updated, _ = m.updateCommand(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	got := updated.(model)
	if got.command != "svc" {
		t.Fatalf("expected [ to recall latest command, got %q", got.command)
	}
	updated, _ = got.updateCommand(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[")})
	got = updated.(model)
	if got.command != "pod" {
		t.Fatalf("expected second [ to recall older command, got %q", got.command)
	}
	updated, _ = got.updateCommand(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	got = updated.(model)
	if got.command != "svc" {
		t.Fatalf("expected ] to move forward in command history, got %q", got.command)
	}
}

func TestDashTogglesLastTwoResourceViews(t *testing.T) {
	m := testModel()
	updated, _ := m.runCommand("pod")
	m = updated.(model)
	updated, _ = m.runCommand("svc")
	m = updated.(model)
	if m.resourceKind != "Service" {
		t.Fatalf("expected latest resource view to be Service, got %q", m.resourceKind)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-")})
	got := updated.(model)
	if got.resourceKind != "Pod" {
		t.Fatalf("expected - to return to previous Pod view, got %q", got.resourceKind)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-")})
	got = updated.(model)
	if got.resourceKind != "Service" {
		t.Fatalf("expected second - to return to Service view, got %q", got.resourceKind)
	}
}

func TestShiftNSortsResourcesByName(t *testing.T) {
	m := testModel()
	m.sortMode = "age-desc"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	got := updated.(model)
	if got.sortMode != "" || got.status != "sort: name" {
		t.Fatalf("expected Shift-N name sort, sort=%q status=%q", got.sortMode, got.status)
	}
}

func TestResourceRebuildPreservesSelectedObject(t *testing.T) {
	m := testModel()
	m.list.Select(1)
	want := m.selectedKey()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	got := updated.(model)
	if got.selectedKey() != want {
		t.Fatalf("resource rebuild changed selected object: want=%q got=%q", want, got.selectedKey())
	}
}

func TestTruncateTextUsesTerminalDisplayWidth(t *testing.T) {
	if got := truncateText("žluťoučký-pod", 8); got != "žluťo..." {
		t.Fatalf("truncateText split or mis-sized Unicode text: got %q", got)
	}
	if got := truncateText("🚀🚀", 1); got != "" {
		t.Fatalf("expected a double-width rune not to overflow one column, got %q", got)
	}
}

func TestShiftSSortsResourcesByStatus(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	got := updated.(model)
	if got.sortMode != "status" || got.status != "sort: status" {
		t.Fatalf("expected Shift-S status sort, sort=%q status=%q", got.sortMode, got.status)
	}
}

func TestShiftPSortsAllNamespacesByNamespace(t *testing.T) {
	m := testModel()
	m.snapshot.Options.AllNamespaces = true
	other := testObject("Pod", "aaa", "first")
	m.snapshot.Graph = graph.Build(append(m.snapshot.Graph.Objects, other))
	m.list = newResourceList(m.snapshot, "Pod")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
	got := updated.(model)
	if got.sortMode != "namespace" || got.list.Items()[0].(item).obj.Ref.Namespace != "aaa" {
		t.Fatalf("expected namespace sort in all-namespaces view, sort=%q first=%#v", got.sortMode, got.list.Items()[0])
	}
}

func TestStatusSortPutsUnhealthyResourcesFirst(t *testing.T) {
	good := testObjectWithRaw("Pod", "app", "healthy", map[string]any{
		"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"ready": true}}},
	})
	bad := testObjectWithRaw("Pod", "app", "broken", map[string]any{
		"status": map[string]any{"phase": "Failed", "containerStatuses": []any{map[string]any{"ready": false}}},
	})
	snapshot := kube.Snapshot{Graph: graph.New([]graph.Object{good, bad}, nil, nil)}
	l := newResourceList(snapshot, "Pod", "", "status")
	if got := l.Items()[0].(item).obj.Ref.Name; got != "broken" {
		t.Fatalf("expected unhealthy Pod first in status sort, got %q", got)
	}
}

func TestOwnerChainViewportKeepsSelectedOwnerVisible(t *testing.T) {
	pod := testObject("Pod", "app", "api")
	owners := make([]graph.Object, 0, 5)
	for i := 0; i < 5; i++ {
		owners = append(owners, testObject("Deployment", "app", fmt.Sprintf("owner-%d", i)))
	}
	objects := append([]graph.Object{pod}, owners...)
	edges := make([]graph.Edge, 0, len(owners))
	child := pod.Ref
	for i := len(owners) - 1; i >= 0; i-- {
		edges = append(edges, graph.Edge{From: owners[i].Ref, To: child, Type: "Owns"})
		child = owners[i].Ref
	}
	g := graph.New(objects, edges, nil)
	m := model{chainCursor: 4}
	body := m.chainPanelBody(g, pod, 80, 3)
	if !strings.Contains(body, "owner-4") {
		t.Fatalf("expected selected end of owner chain to scroll into view, got:\n%s", body)
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

func TestRelationsPanelKeepsMissingConfigReferencesVisible(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name":    "api",
				"envFrom": []any{map[string]any{"secretRef": map[string]any{"name": "missing-secret"}}},
			}},
		},
	})
	g := graph.New([]graph.Object{pod}, nil, nil)
	lines := relationsPanelLines(g, pod)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "! missing secret: missing-secret") {
		t.Fatalf("expected missing Secret reference to remain visible, got:\n%s", joined)
	}
	if _, ok := summaryNavigableRefs(g, pod.Ref.Namespace, lines)[0]; ok {
		t.Fatal("missing Secret must not be treated as navigable")
	}
}

func TestUsedByViewShowsDirectConsumersOnly(t *testing.T) {
	secret := testObject("Secret", "app", "db")
	pod := testObject("Pod", "app", "api")
	deployment := testObject("Deployment", "app", "api")
	g := graph.New([]graph.Object{secret, pod, deployment}, []graph.Edge{
		{From: pod.Ref, To: secret.Ref, Type: "UsesEnv"},
		{From: deployment.Ref, To: pod.Ref, Type: "Owns"},
	}, nil)
	view := usedByView(g, secret)
	if !strings.Contains(view, "Pod/api") || strings.Contains(view, "Deployment/api") {
		t.Fatalf("expected only direct Pod consumer, got:\n%s", view)
	}
}

func TestDeploymentZShortcutOpensReplicaSets(t *testing.T) {
	deployment := testObject("Deployment", "app", "backend")
	replicaset := testObject("ReplicaSet", "app", "backend-abc")
	m := testModel()
	m.snapshot.Graph = graph.New([]graph.Object{deployment, replicaset}, []graph.Edge{{From: deployment.Ref, To: replicaset.Ref, Type: "Owns"}}, nil)
	m.snapshot.LoadedKinds = map[string]bool{"Deployment": true, "ReplicaSet": true}
	m.resourceKind = "Deployment"
	m.list = newResourceList(m.snapshot, "Deployment")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	got := updated.(model)
	if got.resourceKind != "ReplicaSet" || cmd != nil {
		t.Fatalf("expected z to open loaded ReplicaSets, kind=%q cmdNil=%v", got.resourceKind, cmd == nil)
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

func TestPodRowStatusIncludesNodeAndWideIP(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec": map[string]any{"nodeName": "worker-01"},
		"status": map[string]any{
			"phase": "Running", "podIP": "10.0.0.12",
			"containerStatuses": []any{map[string]any{"ready": true, "restartCount": int64(2)}},
		},
	})
	narrow, _ := resourceRowStatus(pod, 80)
	wide, _ := resourceRowStatus(pod, 120)
	if !strings.Contains(narrow, "node:worker-01") || strings.Contains(narrow, "ip:") {
		t.Fatalf("unexpected narrow pod row status: %q", narrow)
	}
	if !strings.Contains(wide, "node:worker-01") || !strings.Contains(wide, "ip:10.0.0.12") {
		t.Fatalf("expected wide pod row status to include node and IP: %q", wide)
	}
}

func TestUsageSummaryShowsPodAndNodeContext(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec": map[string]any{"nodeName": "worker-01"},
	})
	usage := usageSummary(nil, []graph.Object{pod}, 120, 2)
	if !strings.Contains(usage, "pods:1") || !strings.Contains(usage, "nodes:worker-01") {
		t.Fatalf("expected usage context, got:\n%s", usage)
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

func TestDirectQuitShortcutReturnsTeaQuit(t *testing.T) {
	m := testModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected direct q shortcut to quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", cmd())
	}
}

func TestReloadPreservesSelectedResource(t *testing.T) {
	m := testModel()
	m.list.Select(1) // Service is the second item in the fixture's default sort.
	want := m.selectedKey()
	updated, _ := m.Update(reloadResult{snapshot: m.snapshot})
	got := updated.(model)
	if got.selectedKey() != want {
		t.Fatalf("expected refresh to preserve selected key %q, got %q", want, got.selectedKey())
	}
}

func TestReloadPreservesFocusedDetailState(t *testing.T) {
	m := testModel()
	m.active = paneRelations
	m.mode = "yaml"
	m.viewScroll = 4
	m.viewSearch = "apiVersion"
	m.detailFullscreen = true
	m.list.Select(0)
	want := m.selectedKey()

	updated, _ := m.Update(reloadResult{snapshot: m.snapshot})
	got := updated.(model)
	if got.selectedKey() != want || got.active != paneRelations || got.mode != "yaml" || got.viewScroll != 4 || got.viewSearch != "apiVersion" || !got.detailFullscreen {
		t.Fatalf("reload lost detail state: key=%q active=%d mode=%q scroll=%d search=%q fullscreen=%v", got.selectedKey(), got.active, got.mode, got.viewScroll, got.viewSearch, got.detailFullscreen)
	}
}

func TestBackgroundReloadDoesNotOverwriteStatus(t *testing.T) {
	m := testModel()
	m.autoRefresh = true
	m.status = "watching pods"
	updated, _ := m.Update(reloadResult{snapshot: m.snapshot, background: true})
	got := updated.(model)
	if got.status != "watching pods" {
		t.Fatalf("background refresh changed footer status to %q", got.status)
	}
}

func TestReloadCancelsLogsWhenSelectedResourceDisappears(t *testing.T) {
	m := testModel()
	m.logsFullscreen = true
	called := false
	m.logCancel = func() { called = true }
	empty := m.snapshot
	empty.Graph = graph.New(nil, nil, nil)
	empty.LoadedKinds = map[string]bool{"Pod": true}
	updated, _ := m.Update(reloadResult{snapshot: empty, background: true})
	got := updated.(model)
	if !called || got.logCancel != nil || got.logEvents != nil {
		t.Fatalf("expected stale log stream cancellation, called=%v cancelNil=%v eventsNil=%v", called, got.logCancel == nil, got.logEvents == nil)
	}
}

func TestCtrlAShowsResourceAliases(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	got := updated.(model)
	if got.mode != "aliases" || got.active != paneRelations {
		t.Fatalf("expected ctrl-a alias view, mode=%q active=%d", got.mode, got.active)
	}
}

func TestDeleteLastWord(t *testing.T) {
	for input, want := range map[string]string{
		"ctx prod01":    "ctx ",
		"ctx prod01   ": "ctx ",
		"pod":           "",
	} {
		if got := deleteLastWord(input); got != want {
			t.Fatalf("deleteLastWord(%q)=%q, want %q", input, got, want)
		}
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
	annotation := testObjectWithRaw("Deployment", "app", "annotated", map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{
			"argocd.argoproj.io/tracking-id": "billing:apps/Deployment:app/annotated",
		}},
	})
	line, ok = gitopsManagedByLine(annotation)
	if !ok || line != "managed-by: argocd (app:billing)" {
		t.Fatalf("expected Argo tracking annotation identity, got %q ok=%v", line, ok)
	}
}

func TestStatusPaneSurfacesGitopsApplication(t *testing.T) {
	app := testObject("Application", "argocd", "billing-app")
	deploy := testObjectWithRaw("Deployment", "argocd", "billing", map[string]any{
		"metadata": map[string]any{"labels": map[string]any{"argocd.argoproj.io/instance": "billing-app"}},
	})
	g := graph.Build([]graph.Object{app, deploy})
	deploy, _ = g.ObjectByKey(deploy.Ref.Key())
	lines := statusPanelLines(g, deploy)
	found := false
	for _, line := range lines {
		if line.text == "managed-by: argocd (app:billing-app)" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Argo application identity in status pane: %#v", lines)
	}
}

func TestStatusPaneIncludesContainerStateAndReason(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"status": map[string]any{
			"phase": "Running",
			"containerStatuses": []any{map[string]any{
				"name": "api", "ready": false, "restartCount": int64(4),
				"state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
			}},
		},
	})
	lines := statusPanelLines(graph.Build([]graph.Object{pod}), pod)
	for _, line := range lines {
		if line.text == "container/api: waiting:CrashLoopBackOff not-ready restarts:4" {
			return
		}
	}
	t.Fatalf("expected container state in status panel, got %#v", lines)
}

func TestEnvLinesIncludesEnvFromSources(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec": map[string]any{"containers": []any{map[string]any{
			"name": "api",
			"envFrom": []any{
				map[string]any{"secretRef": map[string]any{"name": "db-secret", "optional": true}, "prefix": "DB_"},
				map[string]any{"configMapRef": map[string]any{"name": "app-config"}},
			},
		}}},
	})
	lines := envLines([]graph.Object{pod}, 20)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"envFrom=DB_<- secret:db-secret/* (optional)", "envFrom=<- cm:app-config/*"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in env lines:\n%s", want, joined)
		}
	}
}

func TestEnvLinesIncludesResourceFieldRef(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec": map[string]any{"containers": []any{map[string]any{
			"name": "api", "env": []any{map[string]any{
				"name": "CPU_LIMIT", "valueFrom": map[string]any{"resourceFieldRef": map[string]any{
					"resource": "limits.cpu", "divisor": "1m",
				}},
			}},
		}}},
	})
	joined := strings.Join(envLines([]graph.Object{pod}, 20), "\n")
	if !strings.Contains(joined, "CPU_LIMIT=<- resource:limits.cpu divisor:1m") {
		t.Fatalf("expected resourceFieldRef in env lines:\n%s", joined)
	}
}

func TestStatusPaneKeepsCompleteContainersEventsAndEnvForSearch(t *testing.T) {
	containers := make([]any, 0, 32)
	statuses := make([]any, 0, 32)
	for i := 0; i < 32; i++ {
		name := fmt.Sprintf("worker-%02d", i)
		containers = append(containers, map[string]any{
			"name": name,
			"env":  []any{map[string]any{"name": "VAR_" + name, "value": name}},
		})
		statuses = append(statuses, map[string]any{
			"name": name, "ready": true, "restartCount": int64(0),
			"state": map[string]any{"running": map[string]any{}},
		})
	}
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec":   map[string]any{"containers": containers},
		"status": map[string]any{"phase": "Running", "containerStatuses": statuses},
	})
	pod.Events = make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		pod.Events = append(pod.Events, fmt.Sprintf("event-%02d", i))
	}

	lines := statusPanelLines(graph.Build([]graph.Object{pod}), pod)
	joined := ""
	for _, line := range lines {
		joined += line.text + "\n"
	}
	for _, want := range []string{"containers:", "container/worker-31", "VAR_worker-31=worker-31", "events:", "event-31"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected complete status content to include %q", want)
		}
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

func TestLogPagingAndHomeEndUseTailOffset(t *testing.T) {
	m := testModel()
	m.logsFullscreen = true
	m.height = 10
	m.logLines = []string{"1", "2", "3", "4", "5", "6"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	got := updated.(model)
	if got.logScroll != 5 {
		t.Fatalf("expected PgUp to reach oldest log line, got scroll=%d", got.logScroll)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnd})
	got = updated.(model)
	if got.logScroll != 0 {
		t.Fatalf("expected End to return to live tail, got scroll=%d", got.logScroll)
	}
}

func TestEmptyLogViewPagingNeverProducesNegativeScroll(t *testing.T) {
	m := testModel()
	m.logsFullscreen = true
	m.height = 20
	m.logLines = nil

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	got := updated.(model)
	if got.logScroll != 0 {
		t.Fatalf("expected empty log PgUp to stay at zero, got %d", got.logScroll)
	}
	if rendered := got.logsView(80, 10); rendered == "" {
		t.Fatal("expected empty log view to render a loading state")
	}
}

func TestStatusPagingAndHomeEnd(t *testing.T) {
	m := testModel()
	m.active = paneStatus
	m.height = 10
	m.statusScroll = 0

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	got := updated.(model)
	if got.statusScroll == 0 {
		t.Fatal("expected End to move status viewport to the bottom")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyHome})
	got = updated.(model)
	if got.statusScroll != 0 {
		t.Fatalf("expected Home to return status viewport to top, got scroll=%d", got.statusScroll)
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

func TestShiftJFocusesOwnerChain(t *testing.T) {
	m := testModel()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("J")})
	got := updated.(model)
	if got.active != paneChain {
		t.Fatalf("expected Shift-J to focus owner chain, active=%d", got.active)
	}
}

func TestOpeningRelationEscapesBackToPreviousResource(t *testing.T) {
	pod := testObject("Pod", "app", "api")
	secret := testObject("Secret", "app", "api-secret")
	g := graph.New([]graph.Object{pod, secret}, []graph.Edge{{From: pod.Ref, To: secret.Ref, Type: "UsesEnv"}}, nil)
	snapshot := kube.Snapshot{
		Context:     "test-context",
		Namespace:   "app",
		Graph:       g,
		LoadedKinds: map[string]bool{"Pod": true, "Secret": true},
	}
	m := model{snapshot: snapshot, list: newResourceList(snapshot, "Pod"), resourceKind: "Pod", mode: "relations", active: paneRelations}
	updated, _ := m.jumpToSummaryRef()
	got := updated.(model)
	if got.resourceKind != "Secret" || got.mode != "yaml" || len(got.navStack) != 1 {
		t.Fatalf("expected relation to open as nested YAML, kind=%q mode=%q stack=%d", got.resourceKind, got.mode, len(got.navStack))
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.resourceKind != "Pod" || got.mode != "relations" || got.selectedKey() != pod.Ref.Key() || len(got.navStack) != 0 {
		t.Fatalf("expected esc to restore pod relations, kind=%q mode=%q selected=%q stack=%d", got.resourceKind, got.mode, got.selectedKey(), len(got.navStack))
	}
}

func TestNestedRelationClearsChildFilterAndRestoresParentFilter(t *testing.T) {
	pod := testObject("Pod", "app", "api")
	secret := testObject("Secret", "app", "api-secret")
	worker := testObject("Pod", "app", "worker")
	g := graph.New([]graph.Object{pod, secret, worker}, []graph.Edge{{From: pod.Ref, To: secret.Ref, Type: "UsesEnv"}}, nil)
	snapshot := kube.Snapshot{Context: "test-context", Namespace: "app", Graph: g, LoadedKinds: map[string]bool{"Pod": true, "Secret": true}}
	m := model{snapshot: snapshot, list: newResourceList(snapshot, "Pod"), resourceKind: "Pod", mode: "relations", active: paneRelations}
	updated, _ := m.runCommand("pod /api")
	m = updated.(model)
	if m.resourceFilter != "api" {
		t.Fatalf("expected parent filter, got %q", m.resourceFilter)
	}
	updated, _ = m.jumpToSummaryRef()
	child := updated.(model)
	if child.resourceKind != "Secret" || child.resourceFilter != "" || child.selectedKey() != secret.Ref.Key() {
		t.Fatalf("expected unfiltered Secret child, kind=%q filter=%q selected=%q", child.resourceKind, child.resourceFilter, child.selectedKey())
	}
	updated, _ = child.Update(tea.KeyMsg{Type: tea.KeyEsc})
	parent := updated.(model)
	if parent.resourceKind != "Pod" || parent.resourceFilter != "api" || parent.selectedKey() != pod.Ref.Key() {
		t.Fatalf("expected parent filter restoration, kind=%q filter=%q selected=%q", parent.resourceKind, parent.resourceFilter, parent.selectedKey())
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

func TestRelationsPanelIncludesProjectedVolumeSources(t *testing.T) {
	pod := testObjectWithRaw("Pod", "app", "api", map[string]any{
		"spec": map[string]any{
			"imagePullSecrets": []any{map[string]any{"name": "pull-secret"}},
			"volumes": []any{map[string]any{
				"name": "bundle", "projected": map[string]any{"sources": []any{
					map[string]any{"secret": map[string]any{"name": "runtime-secret"}},
					map[string]any{"configMap": map[string]any{"name": "runtime-config"}},
				}},
			}}},
	})
	secret := testObject("Secret", "app", "runtime-secret")
	config := testObject("ConfigMap", "app", "runtime-config")
	pullSecret := testObject("Secret", "app", "pull-secret")
	g := graph.Build([]graph.Object{pod, secret, config, pullSecret})
	pod, _ = g.ObjectByKey(pod.Ref.Key())
	joined := strings.Join(relationsPanelLines(g, pod), "\n")
	for _, want := range []string{"secret: runtime-secret", "configmap: runtime-config", "secret: pull-secret"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected projected source %q in relations:\n%s", want, joined)
		}
	}
}

func TestRelationsViewportKeepsSelectedReferenceVisible(t *testing.T) {
	pod := testObject("Pod", "app", "api")
	objects := []graph.Object{pod}
	var edges []graph.Edge
	for i := 0; i < 12; i++ {
		secret := testObject("Secret", "app", fmt.Sprintf("secret-%02d", i))
		objects = append(objects, secret)
		edges = append(edges, graph.Edge{From: pod.Ref, To: secret.Ref, Type: "UsesEnv"})
	}
	g := graph.New(objects, edges, nil)
	pod, _ = g.ObjectByKey(pod.Ref.Key())
	m := model{summaryCursor: 11}
	body := m.relationsPanelBody(g, pod, 80, 4)
	if !strings.Contains(body, "secret-11") {
		t.Fatalf("expected selected relation to remain visible in viewport:\n%s", body)
	}
}

func TestRelationsCtrlFFiltersReferences(t *testing.T) {
	pod := testObject("Pod", "app", "api")
	secret := testObject("Secret", "app", "db-secret")
	config := testObject("ConfigMap", "app", "app-config")
	snapshot := kube.Snapshot{
		Context:     "test-context",
		Namespace:   "app",
		Graph:       graph.New([]graph.Object{pod, secret, config}, []graph.Edge{{From: pod.Ref, To: secret.Ref, Type: "UsesEnv"}, {From: pod.Ref, To: config.Ref, Type: "Mounts"}}, nil),
		LoadedKinds: map[string]bool{"Pod": true, "Secret": true, "ConfigMap": true},
	}
	m := model{snapshot: snapshot, list: newResourceList(snapshot, "Pod"), resourceKind: "Pod", active: paneRelations, mode: "relations", width: 100, height: 30}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := updated.(model)
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("secret")})
	got = updated.(model)
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(model)
	view := got.View()
	if got.relationSearch != "secret" || !strings.Contains(view, "db-secret") || strings.Contains(view, "app-config") {
		t.Fatalf("expected relation filter to keep only secret ref, search=%q view:\n%s", got.relationSearch, view)
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

func TestResourceViewCtrlFAndScroll(t *testing.T) {
	m := testModel()
	m.active = paneRelations
	m.mode = "yaml"
	m.width = 100
	m.height = 10

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := updated.(model)
	if !got.viewSearchMode {
		t.Fatal("expected ctrl-f to open resource view search")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("apiVersion")})
	got = updated.(model)
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(model)
	if got.viewSearchMode || got.viewSearch != "apiVersion" {
		t.Fatalf("expected committed view search, mode=%v search=%q", got.viewSearchMode, got.viewSearch)
	}
	if !strings.Contains(got.View(), "apiVersion") {
		t.Fatalf("expected matching YAML to remain visible:\n%s", got.View())
	}
	before := got.viewScroll
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	got = updated.(model)
	if got.viewScroll <= before {
		t.Fatalf("expected j to scroll resource view, before=%d after=%d", before, got.viewScroll)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.mode != "relations" || got.viewScroll != 0 || got.viewSearch != "" {
		t.Fatalf("expected esc to leave detail view cleanly, mode=%q scroll=%d search=%q", got.mode, got.viewScroll, got.viewSearch)
	}
}

func TestDetailFullscreenTogglePreservesViewState(t *testing.T) {
	m := testModel()
	m.active = paneRelations
	m.mode = "yaml"
	m.width = 100
	m.height = 20
	m.viewScroll = 2
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	got := updated.(model)
	if !got.detailFullscreen || got.viewScroll != 2 {
		t.Fatalf("expected fullscreen detail without resetting scroll, fullscreen=%v scroll=%d", got.detailFullscreen, got.viewScroll)
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got = updated.(model)
	if got.detailFullscreen || got.viewScroll != 2 {
		t.Fatalf("expected esc to return to split detail, fullscreen=%v scroll=%d", got.detailFullscreen, got.viewScroll)
	}
}

func TestStatusPaneCtrlFSearchesStatusContent(t *testing.T) {
	m := testModel()
	m.active = paneStatus
	m.width = 100
	m.height = 12
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := updated.(model)
	if !got.statusSearchMode {
		t.Fatal("expected ctrl-f to open status search")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("status")})
	got = updated.(model)
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(model)
	if got.statusSearchMode || got.statusSearch != "status" || !strings.Contains(got.View(), "status:") {
		t.Fatalf("expected status search result, mode=%v search=%q view:\n%s", got.statusSearchMode, got.statusSearch, got.View())
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
