package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/filipcsupka/krel/internal/graph"
	"github.com/filipcsupka/krel/internal/kube"
)

type pane int

const (
	paneResources pane = iota
	paneRelations
	paneDetails
)

type model struct {
	snapshot        kube.Snapshot
	list            list.Model
	width           int
	height          int
	active          pane
	mode            string
	commandMode     bool
	namespacePicker bool
	command         string
	status          string
	loading         bool
	resourceKind    string
	warningsOnly    bool
	sortMode        string
	logLines        []string
	logErr          string
	logKey          string
	logPaused       bool
	logWrap         bool
	logGrep         string
	logPrevious     bool
	logTimestamps   bool
	logSince        time.Duration
}

type reloadResult struct {
	snapshot kube.Snapshot
	err      error
}

type logResult struct {
	key   string
	lines []string
	err   error
}

type logTick struct {
	key string
}

type item struct {
	obj graph.Object
}

func (i item) Title() string       { return i.obj.Ref.Label() }
func (i item) Description() string { return shortStatus(i.obj) }
func (i item) FilterValue() string { return i.obj.Ref.Label() }

type namespaceItem struct {
	name    string
	current bool
}

func (i namespaceItem) Title() string { return i.name }
func (i namespaceItem) Description() string {
	if i.current {
		return "current"
	}
	return "namespace"
}
func (i namespaceItem) FilterValue() string { return i.name }

func New(snapshot kube.Snapshot) tea.Model {
	return model{snapshot: snapshot, list: newResourceList(snapshot, "Pod"), mode: "relations", resourceKind: "Pod", logTimestamps: true}
}

func newResourceList(snapshot kube.Snapshot, kinds ...string) list.Model {
	kind := ""
	if len(kinds) > 0 {
		kind = kinds[0]
	}
	warningsOnly := false
	if len(kinds) > 1 && kinds[1] == "warnings" {
		warningsOnly = true
	}
	sortMode := ""
	if len(kinds) > 2 {
		sortMode = kinds[2]
	}
	items := make([]list.Item, 0, len(snapshot.Graph.Objects))
	for _, obj := range snapshot.Graph.Objects {
		if kind != "" && obj.Ref.Kind != kind {
			continue
		}
		if warningsOnly && obj.Ref.Kind == "Event" {
			typ, _, _ := unstructuredNestedString(obj, "type")
			if typ != "Warning" {
				continue
			}
		}
		items = append(items, item{obj: obj})
	}
	sortItems(items, sortMode)
	l := list.New(items, resourceDelegate{}, 34, 20)
	title := fmt.Sprintf("ctx: %s  ns: %s", snapshot.Context, snapshot.Namespace)
	if kind != "" {
		title += "  kind: " + kind
	}
	if warningsOnly {
		title += "  warnings"
	}
	if sortMode != "" {
		title += "  sort:" + sortModeLabel(sortMode)
	}
	l.Title = title
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	return l
}

func warningFilter(enabled bool) string {
	if enabled {
		return "warnings"
	}
	return ""
}

func (m model) resourceList() list.Model {
	return newResourceList(m.snapshot, m.resourceKind, warningFilter(m.warningsOnly), m.sortMode)
}

func newNamespaceList(snapshot kube.Snapshot) list.Model {
	items := make([]list.Item, 0, len(snapshot.Namespaces))
	for _, namespace := range snapshot.Namespaces {
		items = append(items, namespaceItem{name: namespace, current: namespace == snapshot.Namespace})
	}
	l := list.New(items, list.NewDefaultDelegate(), 34, 20)
	l.Title = fmt.Sprintf("ctx: %s  namespaces", snapshot.Context)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	return l
}

func sortItems(items []list.Item, mode string) {
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].(item).obj
		right := items[j].(item).obj
		leftCreated := left.Raw.GetCreationTimestamp().Time
		rightCreated := right.Raw.GetCreationTimestamp().Time
		switch mode {
		case "age-desc":
			return leftCreated.After(rightCreated)
		case "age-asc":
			return leftCreated.Before(rightCreated)
		default:
			if left.Ref.Kind == right.Ref.Kind {
				return left.Ref.Name < right.Ref.Name
			}
			return left.Ref.Kind < right.Ref.Kind
		}
	})
}

func nextAgeSortMode(current string) string {
	switch current {
	case "":
		return "age-desc"
	case "age-desc":
		return "age-asc"
	default:
		return ""
	}
}

func parseSortMode(value string) (string, bool) {
	switch strings.ToLower(value) {
	case "age", "new", "newest", "desc", "age-desc":
		return "age-desc", true
	case "old", "oldest", "asc", "age-asc":
		return "age-asc", true
	case "default", "name", "none", "off":
		return "", true
	default:
		return "", false
	}
}

func sortModeLabel(mode string) string {
	switch mode {
	case "age-desc":
		return "newest"
	case "age-asc":
		return "oldest"
	default:
		return "default"
	}
}

func (m model) Init() tea.Cmd {
	return m.loadSelectedLogs()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(max(28, msg.Width/3), max(8, msg.Height-3))
	case reloadResult:
		m.loading = false
		if msg.err != nil {
			m.status = "reload failed: " + msg.err.Error()
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.namespacePicker = false
		m.list = m.resourceList()
		m.list.SetSize(max(28, m.width/3), max(8, m.height-3))
		m.status = fmt.Sprintf("loaded context %s namespace %s", msg.snapshot.Context, msg.snapshot.Namespace)
		return m, m.loadSelectedLogs()
	case logResult:
		if msg.key != m.selectedKey() {
			return m, nil
		}
		m.logKey = msg.key
		m.logErr = ""
		if msg.err != nil {
			m.logErr = msg.err.Error()
			m.logLines = nil
		} else {
			m.logLines = msg.lines
		}
		if m.logPaused {
			return m, nil
		}
		return m, scheduleLogRefresh(msg.key)
	case logTick:
		if msg.key != m.selectedKey() || m.logPaused {
			return m, nil
		}
		return m, m.loadSelectedLogs()
	case tea.KeyMsg:
		if m.commandMode {
			return m.updateCommand(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.namespacePicker {
				m.namespacePicker = false
				m.list = m.resourceList()
				m.list.SetSize(max(28, m.width/3), max(8, m.height-3))
				m.status = ""
			}
			return m, nil
		case "backspace":
			return m, nil
		case "enter":
			if m.namespacePicker {
				selected, ok := m.list.SelectedItem().(namespaceItem)
				if !ok {
					return m, nil
				}
				opts := m.snapshot.Options
				opts.Namespace = selected.name
				m.loading = true
				m.status = "loading namespace " + selected.name + "..."
				return m, reload(opts)
			}
		case " ":
			m.logPaused = !m.logPaused
			if m.logPaused {
				m.status = "logs paused"
				return m, nil
			}
			m.status = "logs following"
			return m, m.loadSelectedLogs()
		case "w":
			m.logWrap = !m.logWrap
			if m.logWrap {
				m.status = "log wrap on"
			} else {
				m.status = "log wrap off"
			}
			return m, nil
		case "P":
			m.logPrevious = !m.logPrevious
			if m.logPrevious {
				m.status = "previous logs on"
			} else {
				m.status = "previous logs off"
			}
			return m, m.loadSelectedLogs()
		case "t":
			m.logTimestamps = !m.logTimestamps
			if m.logTimestamps {
				m.status = "log timestamps on"
			} else {
				m.status = "log timestamps off"
			}
			return m, m.loadSelectedLogs()
		case "A":
			m.sortMode = nextAgeSortMode(m.sortMode)
			m.list = m.resourceList()
			m.list.SetSize(max(28, m.width/3), max(8, m.height-3))
			if m.sortMode == "" {
				m.status = "sort: default"
			} else {
				m.status = "sort: " + sortModeLabel(m.sortMode)
			}
			m.logLines = nil
			m.logErr = ""
			return m, m.loadSelectedLogs()
		case ":":
			m.commandMode = true
			m.command = ""
		case "tab":
			m.active = (m.active + 1) % 3
		case "r":
			m.mode = "relations"
		case "d":
			m.mode = "details"
		case "y":
			m.mode = "yaml"
		case "e":
			m.mode = "events"
		case "p":
			m.mode = "problems"
		case "?":
			m.mode = "help"
		}
	}

	var cmd tea.Cmd
	before := m.selectedKey()
	if m.active == paneResources || m.list.FilterState() != list.Unfiltered {
		m.list, cmd = m.list.Update(msg)
	}
	after := m.selectedKey()
	if before != after {
		m.logLines = nil
		m.logErr = ""
		return m, tea.Batch(cmd, m.loadSelectedLogs())
	}
	return m, cmd
}

func (m model) updateCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.commandMode = false
		m.command = ""
	case "enter":
		command := strings.TrimSpace(m.command)
		m.commandMode = false
		m.command = ""
		return m.runCommand(command)
	case "backspace":
		if len(m.command) > 0 {
			m.command = m.command[:len(m.command)-1]
		}
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) runCommand(command string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return m, nil
	}
	opts := m.snapshot.Options
	switch fields[0] {
	case "q", "quit":
		return m, tea.Quit
	case "ctx", "context":
		if len(fields) == 1 {
			m.status = "contexts: " + strings.Join(m.snapshot.Contexts, ", ")
			return m, nil
		}
		opts.ContextName = fields[1]
		opts.Namespace = ""
	case "ns", "namespace":
		if len(fields) == 1 {
			if len(m.snapshot.Namespaces) == 0 {
				m.status = "no namespaces loaded"
				return m, nil
			}
			m.namespacePicker = true
			m.list = newNamespaceList(m.snapshot)
			m.list.SetSize(max(28, m.width/3), max(8, m.height-3))
			m.status = "select namespace with enter, esc returns"
			return m, nil
		}
		if len(fields) != 2 {
			m.status = "usage: :ns [namespace]"
			return m, nil
		}
		opts.Namespace = fields[1]
	case "kc", "kubeconfig":
		if len(fields) != 2 {
			m.status = "usage: :kubeconfig <path>"
			return m, nil
		}
		opts.Kubeconfig = fields[1]
		opts.ContextName = ""
		opts.Namespace = ""
	case "refresh", "reload":
	case "grep":
		m.logGrep = strings.Join(fields[1:], " ")
		if m.logGrep == "" {
			m.status = "log grep cleared"
		} else {
			m.status = "log grep: " + m.logGrep
		}
		return m, m.loadSelectedLogs()
	case "nogrep":
		m.logGrep = ""
		m.status = "log grep cleared"
		return m, m.loadSelectedLogs()
	case "since":
		if len(fields) != 2 {
			m.status = "usage: :since 5m|30m|1h|0"
			return m, nil
		}
		if fields[1] == "0" {
			m.logSince = 0
			m.status = "log since cleared"
			return m, m.loadSelectedLogs()
		}
		duration, err := time.ParseDuration(fields[1])
		if err != nil {
			m.status = "invalid duration: " + fields[1]
			return m, nil
		}
		m.logSince = duration
		m.status = "log since: " + fields[1]
		return m, m.loadSelectedLogs()
	case "sort":
		if len(fields) != 2 {
			m.status = "usage: :sort age|new|old|default"
			return m, nil
		}
		mode, ok := parseSortMode(fields[1])
		if !ok {
			m.status = "usage: :sort age|new|old|default"
			return m, nil
		}
		m.sortMode = mode
		m.list = m.resourceList()
		m.list.SetSize(max(28, m.width/3), max(8, m.height-3))
		if m.sortMode == "" {
			m.status = "sort: default"
		} else {
			m.status = "sort: " + sortModeLabel(m.sortMode)
		}
		return m, m.loadSelectedLogs()
	default:
		if kind, ok := resourceAlias(fields[0]); ok {
			m.namespacePicker = false
			m.resourceKind = kind
			m.warningsOnly = kind == "Event"
			m.list = m.resourceList()
			m.list.SetSize(max(28, m.width/3), max(8, m.height-3))
			m.status = fmt.Sprintf("showing %s", kind)
			return m, m.loadSelectedLogs()
		}
		if fields[0] == "all" {
			m.namespacePicker = false
			m.resourceKind = ""
			m.warningsOnly = false
			m.list = m.resourceList()
			m.list.SetSize(max(28, m.width/3), max(8, m.height-3))
			m.status = "showing all loaded resources"
			return m, m.loadSelectedLogs()
		}
		m.status = "unknown command: " + fields[0]
		return m, nil
	}
	m.loading = true
	m.status = "loading..."
	return m, reload(opts)
}

func (m model) selectedKey() string {
	obj, ok := m.selected()
	if !ok {
		return ""
	}
	return obj.Ref.Key()
}

func (m model) loadSelectedLogs() tea.Cmd {
	obj, ok := m.selected()
	if !ok || m.namespacePicker {
		return nil
	}
	key := obj.Ref.Key()
	pods := m.logPods(obj)
	previous := m.logPrevious || shouldPreferPreviousLogs(m.snapshot.Graph, obj)
	return func() tea.Msg {
		result := kube.LoadLogs(context.Background(), kube.LogRequest{
			Options:    m.snapshot.Options,
			Namespace:  m.snapshot.Namespace,
			Pods:       pods,
			TailLines:  160,
			Grep:       m.logGrep,
			Previous:   previous,
			Timestamps: m.logTimestamps,
			Since:      m.logSince,
			MaxPerPane: 400,
		})
		return logResult{key: key, lines: result.Lines, err: result.Err}
	}
}

func shouldPreferPreviousLogs(g *graph.Graph, obj graph.Object) bool {
	for _, pod := range relatedPods(g, obj) {
		status := strings.ToLower(podDisplayStatus(pod))
		if strings.Contains(status, "crashloop") || strings.Contains(status, "restarts:") && podRestarts(pod) > 0 {
			return true
		}
	}
	return false
}

func scheduleLogRefresh(key string) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return logTick{key: key}
	})
}

func reload(opts kube.Options) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := kube.LoadSnapshot(context.Background(), opts)
		return reloadResult{snapshot: snapshot, err: err}
	}
}

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	leftWidth := max(32, m.width/3)
	rightWidth := max(40, m.width-leftWidth-3)
	mainHeight := max(8, m.height-1)
	panelContentHeight := max(6, mainHeight-2)

	left := paneStyle(m.active == paneResources).Width(leftWidth).Height(panelContentHeight).Render(m.list.View())
	right := m.rightView(rightWidth, mainHeight)
	footer := m.footer()

	return lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, left, right), footer)
}

func (m model) selected() (graph.Object, bool) {
	selected, ok := m.list.SelectedItem().(item)
	if !ok {
		return graph.Object{}, false
	}
	return selected.obj, true
}

func resourceAlias(command string) (string, bool) {
	switch strings.ToLower(command) {
	case "po", "pod", "pods":
		return "Pod", true
	case "deploy", "deployment", "deployments":
		return "Deployment", true
	case "rs", "replicaset", "replicasets":
		return "ReplicaSet", true
	case "sts", "statefulset", "statefulsets":
		return "StatefulSet", true
	case "ds", "daemonset", "daemonsets":
		return "DaemonSet", true
	case "job", "jobs":
		return "Job", true
	case "cj", "cronjob", "cronjobs":
		return "CronJob", true
	case "ev", "event", "events":
		return "Event", true
	case "node", "nodes", "no":
		return "Node", true
	case "quota", "quotas", "resourcequota", "resourcequotas":
		return "ResourceQuota", true
	case "limits", "limitrange", "limitranges":
		return "LimitRange", true
	case "np", "netpol", "networkpolicy", "networkpolicies":
		return "NetworkPolicy", true
	case "pdb", "poddisruptionbudget", "poddisruptionbudgets":
		return "PodDisruptionBudget", true
	case "hpa", "horizontalpodautoscaler", "horizontalpodautoscalers":
		return "HorizontalPodAutoscaler", true
	case "cert", "certificate", "certificates":
		return "Certificate", true
	case "issuer", "issuers":
		return "Issuer", true
	case "clusterissuer", "clusterissuers":
		return "ClusterIssuer", true
	case "svc", "service", "services":
		return "Service", true
	case "cm", "configmap", "configmaps":
		return "ConfigMap", true
	case "secret", "secrets":
		return "Secret", true
	case "pvc", "persistentvolumeclaim", "persistentvolumeclaims":
		return "PersistentVolumeClaim", true
	case "sa", "serviceaccount", "serviceaccounts":
		return "ServiceAccount", true
	case "ing", "ingress", "ingresses":
		return "Ingress", true
	case "route", "routes":
		return "Route", true
	case "endpointslice", "endpointslices", "ep", "eps":
		return "EndpointSlice", true
	}
	return "", false
}

func (m model) rightView(width, height int) string {
	if m.loading {
		return paneStyle(false).Width(width).Height(max(1, height-2)).Render("Loading cluster snapshot...")
	}
	if m.namespacePicker {
		return m.namespaceHelp(width, height)
	}
	obj, ok := m.selected()
	if !ok {
		return paneStyle(false).Width(width).Height(max(1, height-2)).Render("No resources loaded.")
	}

	contentBudget := max(4, height-4)
	topHeight := max(7, contentBudget/3)
	if topHeight > contentBudget-4 {
		topHeight = max(2, contentBudget-4)
	}
	logHeight := max(2, contentBudget-topHeight)
	body := workloadSummaryView(m.snapshot.Graph, obj)
	title := "Summary"
	if m.mode != "relations" {
		switch m.mode {
		case "yaml":
			body = truncateLines(obj.YAML(), topHeight-2)
			title = "Yaml"
		case "events":
			body = eventsView(obj)
			title = "Events"
		case "details":
			body = detailsView(m.snapshot.Graph, obj)
			title = "Details"
		case "help":
			body = helpView()
			title = "Help"
		}
	}
	logs := m.logsView(width-4, logHeight-2)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		paneTitle(title, body, width, topHeight, m.active == paneRelations),
		paneTitle("Logs", logs, width, logHeight, false),
	)
}

func (m model) namespaceHelp(width, height int) string {
	body := strings.Join([]string{
		"Select a namespace with enter.",
		"Use / to filter namespaces.",
		"Esc returns to the current resource view.",
		"",
		"K9s-compatible command:",
		":ns / :namespace opens this picker.",
		":ns <name> switches directly.",
	}, "\n")
	return paneStyle(false).Width(width).Height(max(1, height-2)).Render(body)
}

func relationsView(g *graph.Graph, obj graph.Object) string {
	edges := g.EdgesFor(obj.Ref)
	if len(edges) == 0 {
		return "No relationships found."
	}
	lines := make([]string, 0, len(edges)*2)
	for _, edge := range edges {
		arrow := "->"
		left := edge.From.Label()
		right := edge.To.Label()
		if edge.To.Key() == obj.Ref.Key() {
			arrow = "<-"
			left = edge.To.Label()
			right = edge.From.Label()
		}
		lines = append(lines, fmt.Sprintf("%s %s %s  [%s]", left, arrow, right, edge.Type))
		lines = append(lines, "  "+edge.Reason)
	}
	return strings.Join(lines, "\n")
}

func relationsSummaryView(g *graph.Graph, obj graph.Object) string {
	lines := directRefLines(g, obj)
	problems := g.ProblemsFor(obj.Ref)
	for _, problem := range problems {
		lines = append(lines, "problem: "+problem.Message)
	}
	if len(obj.Events) > 0 {
		lines = append(lines, fmt.Sprintf("events: %d", len(obj.Events)))
	}
	if len(lines) == 0 {
		return "no direct refs"
	}
	return strings.Join(lines, "\n")
}

func workloadSummaryView(g *graph.Graph, obj graph.Object) string {
	lines := []string{}
	status, health := resourceStatus(obj)
	lines = append(lines, statusColor(health).Render("status: ")+status)

	pods := relatedPods(g, obj)
	if len(pods) > 0 {
		running, failed, pending, restarts := podRollup(pods)
		lines = append(lines, fmt.Sprintf("pods: %d running / %d failed / %d pending  restarts:%d", running, failed, pending, restarts))
		for _, pod := range limitObjects(pods, 4) {
			podStatus, podHealth := resourceStatus(pod)
			node, _, _ := unstructuredNestedString(pod, "spec", "nodeName")
			line := fmt.Sprintf("pod: %s  %s", pod.Ref.Name, podStatus)
			if node != "" {
				line += "  node:" + node
			}
			lines = append(lines, statusColor(podHealth).Render("pod: ")+strings.TrimPrefix(line, "pod: "))
		}
		images := imageLines(pods)
		lines = append(lines, images...)
	}

	lines = append(lines, serviceLines(g, obj)...)
	lines = append(lines, configUsageLines(g, obj)...)
	lines = append(lines, directRefLines(g, obj)...)
	lines = append(lines, problemLines(g, obj)...)
	lines = append(lines, eventRollupLines(obj, pods)...)

	if len(lines) == 1 {
		lines = append(lines, "no direct refs")
	}
	return strings.Join(lines, "\n")
}

func podRollup(pods []graph.Object) (running, failed, pending int, restarts int64) {
	for _, pod := range pods {
		phase, _, _ := unstructuredNestedString(pod, "status", "phase")
		switch phase {
		case "Running":
			running++
		case "Failed":
			failed++
		case "Pending":
			pending++
		}
		restarts += podRestarts(pod)
	}
	return running, failed, pending, restarts
}

func limitObjects(objects []graph.Object, limit int) []graph.Object {
	if len(objects) <= limit {
		return objects
	}
	return objects[:limit]
}

func imageLines(pods []graph.Object) []string {
	seen := map[string]bool{}
	var lines []string
	for _, pod := range pods {
		containers, _, _ := unstructuredNestedSlice(pod, "spec", "containers")
		for _, container := range containers {
			c, ok := container.(map[string]any)
			if !ok {
				continue
			}
			image, _, _ := nestedString(c, "image")
			name, _, _ := nestedString(c, "name")
			if image == "" {
				continue
			}
			line := "image: " + image
			if name != "" {
				line = "image: " + name + "=" + image
			}
			if !seen[line] {
				seen[line] = true
				lines = append(lines, line)
			}
		}
	}
	sort.Strings(lines)
	if len(lines) > 3 {
		return lines[:3]
	}
	return lines
}

func serviceLines(g *graph.Graph, obj graph.Object) []string {
	services := map[string]bool{}
	for _, pod := range relatedPods(g, obj) {
		for _, edge := range g.EdgesFor(pod.Ref) {
			if edge.Type == "Selects" && edge.To.Key() == pod.Ref.Key() && edge.From.Kind == "Service" {
				services["service: "+edge.From.Name] = true
			}
		}
	}
	if obj.Ref.Kind == "Service" {
		for _, edge := range g.EdgesFor(obj.Ref) {
			if edge.Type == "HasEndpoints" {
				services["endpoint: "+edge.To.Name] = true
			}
		}
	}
	lines := make([]string, 0, len(services))
	for line := range services {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

func configUsageLines(g *graph.Graph, obj graph.Object) []string {
	seen := map[string]bool{}
	for _, pod := range relatedPods(g, obj) {
		for _, line := range podConfigUsageLines(g, pod) {
			seen[line] = true
		}
	}
	if obj.Ref.Kind == "Pod" {
		for _, line := range podConfigUsageLines(g, obj) {
			seen[line] = true
		}
	}
	lines := make([]string, 0, len(seen))
	for line := range seen {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

func podConfigUsageLines(g *graph.Graph, pod graph.Object) []string {
	refs := map[string]map[string]bool{}
	add := func(kind, name, usage string) {
		if name == "" {
			return
		}
		key := summaryKind(kind) + ": " + name
		if refs[key] == nil {
			refs[key] = map[string]bool{}
		}
		if usage != "" {
			refs[key][usage] = true
		}
	}

	sa, _, _ := unstructuredNestedString(pod, "spec", "serviceAccountName")
	if sa == "" {
		sa = "default"
	}
	add("ServiceAccount", sa, "")

	volumes, _, _ := unstructuredNestedSlice(pod, "spec", "volumes")
	for _, volume := range volumes {
		v, ok := volume.(map[string]any)
		if !ok {
			continue
		}
		volumeName, _, _ := nestedString(v, "name")
		if name, _, _ := nestedString(v, "configMap", "name"); name != "" {
			add("ConfigMap", name, "volume:"+volumeName)
		}
		if name, _, _ := nestedString(v, "secret", "secretName"); name != "" {
			add("Secret", name, "volume:"+volumeName)
		}
		if name, _, _ := nestedString(v, "persistentVolumeClaim", "claimName"); name != "" {
			add("PersistentVolumeClaim", name, "volume:"+volumeName)
		}
	}

	for _, path := range [][]string{{"spec", "containers"}, {"spec", "initContainers"}} {
		containers, _, _ := unstructuredNestedSlice(pod, path...)
		for _, container := range containers {
			c, ok := container.(map[string]any)
			if !ok {
				continue
			}
			containerName, _, _ := nestedString(c, "name")
			envFrom, _, _ := nestedSlice(c, "envFrom")
			for _, envSource := range envFrom {
				m, ok := envSource.(map[string]any)
				if !ok {
					continue
				}
				if name, _, _ := nestedString(m, "configMapRef", "name"); name != "" {
					add("ConfigMap", name, "envFrom:"+containerName)
				}
				if name, _, _ := nestedString(m, "secretRef", "name"); name != "" {
					add("Secret", name, "envFrom:"+containerName)
				}
			}
			env, _, _ := nestedSlice(c, "env")
			for _, envVar := range env {
				m, ok := envVar.(map[string]any)
				if !ok {
					continue
				}
				envName, _, _ := nestedString(m, "name")
				if name, _, _ := nestedString(m, "valueFrom", "configMapKeyRef", "name"); name != "" {
					key, _, _ := nestedString(m, "valueFrom", "configMapKeyRef", "key")
					add("ConfigMap", name, "env:"+envName+"="+key)
				}
				if name, _, _ := nestedString(m, "valueFrom", "secretKeyRef", "name"); name != "" {
					key, _, _ := nestedString(m, "valueFrom", "secretKeyRef", "key")
					add("Secret", name, "env:"+envName+"="+key)
				}
			}
			mounts, _, _ := nestedSlice(c, "volumeMounts")
			for _, mount := range mounts {
				m, ok := mount.(map[string]any)
				if !ok {
					continue
				}
				volumeName, _, _ := nestedString(m, "name")
				path, _, _ := nestedString(m, "mountPath")
				if volumeName == "" || path == "" {
					continue
				}
				for _, volume := range volumes {
					v, ok := volume.(map[string]any)
					if !ok {
						continue
					}
					name, _, _ := nestedString(v, "name")
					if name != volumeName {
						continue
					}
					if ref, _, _ := nestedString(v, "configMap", "name"); ref != "" {
						add("ConfigMap", ref, "mount:"+path)
					}
					if ref, _, _ := nestedString(v, "secret", "secretName"); ref != "" {
						add("Secret", ref, "mount:"+path)
					}
					if ref, _, _ := nestedString(v, "persistentVolumeClaim", "claimName"); ref != "" {
						add("PersistentVolumeClaim", ref, "mount:"+path)
					}
				}
			}
		}
	}

	var lines []string
	for ref, usage := range refs {
		line := ref
		if keys := objectKeys(g, pod.Ref.Namespace, ref); keys != "" {
			line += " keys:" + keys
		}
		if len(usage) > 0 {
			line += " use:" + strings.Join(sortedMapKeys(usage), ",")
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

func objectKeys(g *graph.Graph, namespace, label string) string {
	parts := strings.SplitN(label, ": ", 2)
	if len(parts) != 2 {
		return ""
	}
	kind := kindFromSummary(parts[0])
	if kind != "ConfigMap" && kind != "Secret" {
		return ""
	}
	obj, ok := g.ObjectByKey(graph.ObjectRef{Kind: kind, Namespace: namespace, Name: parts[1]}.Key())
	if !ok {
		return ""
	}
	keys := map[string]bool{}
	data, _, _ := unstructuredNestedStringMap(obj, "data")
	for key := range data {
		keys[key] = true
	}
	binaryData, _, _ := unstructuredNestedStringMap(obj, "binaryData")
	for key := range binaryData {
		keys[key] = true
	}
	out := sortedMapKeys(keys)
	if len(out) > 5 {
		out = append(out[:5], "...")
	}
	return strings.Join(out, ",")
}

func kindFromSummary(kind string) string {
	switch kind {
	case "configmap":
		return "ConfigMap"
	case "secret":
		return "Secret"
	case "pvc":
		return "PersistentVolumeClaim"
	case "sa":
		return "ServiceAccount"
	default:
		return kind
	}
}

func sortedMapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func problemLines(g *graph.Graph, obj graph.Object) []string {
	seen := map[string]bool{}
	var lines []string
	for _, problem := range g.ProblemsFor(obj.Ref) {
		line := "problem: " + problem.Message
		seen[line] = true
	}
	for _, pod := range relatedPods(g, obj) {
		for _, problem := range g.ProblemsFor(pod.Ref) {
			line := "problem: " + pod.Ref.Name + " " + problem.Message
			seen[line] = true
		}
	}
	for line := range seen {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

func eventRollupLines(obj graph.Object, pods []graph.Object) []string {
	warnings, normal := eventCounts(obj.Events)
	for _, pod := range pods {
		w, n := eventCounts(pod.Events)
		warnings += w
		normal += n
	}
	if warnings == 0 && normal == 0 {
		return nil
	}
	line := fmt.Sprintf("events: %d warning / %d normal", warnings, normal)
	if warnings > 0 {
		return []string{line}
	}
	return []string{line}
}

func eventCounts(events []string) (warnings, normal int) {
	for _, event := range events {
		if strings.HasPrefix(strings.ToLower(event), "warning ") {
			warnings++
		} else {
			normal++
		}
	}
	return warnings, normal
}

func directRefLines(g *graph.Graph, obj graph.Object) []string {
	refs := map[string]bool{}
	for _, edge := range directImportantEdges(g, obj) {
		refs[summaryKind(edge.To.Kind)+": "+edge.To.Name] = true
	}
	lines := make([]string, 0, len(refs))
	for line := range refs {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

func summaryKind(kind string) string {
	switch kind {
	case "PersistentVolumeClaim":
		return "pvc"
	case "ServiceAccount":
		return "sa"
	case "HorizontalPodAutoscaler":
		return "hpa"
	case "PodDisruptionBudget":
		return "pdb"
	case "NetworkPolicy":
		return "netpol"
	case "EndpointSlice":
		return "endpoint"
	default:
		return strings.ToLower(kind)
	}
}

func opsView(g *graph.Graph, obj graph.Object) string {
	lines := []string{}
	problems := g.ProblemsFor(obj.Ref)
	if len(problems) > 0 {
		lines = append(lines, "Problems")
		for _, problem := range problems {
			lines = append(lines, fmt.Sprintf("[%s] %s", problem.Level, problem.Message))
		}
		lines = append(lines, "")
	}

	important := directImportantEdges(g, obj)
	if len(important) > 0 {
		lines = append(lines, "Direct refs")
		for _, edge := range important {
			lines = append(lines, fmt.Sprintf("%s [%s]", edge.To.Label(), edge.Type))
		}
		lines = append(lines, "")
	}

	if len(obj.Events) > 0 {
		lines = append(lines, "Events")
		lines = append(lines, obj.Events...)
	} else {
		lines = append(lines, "No events loaded for this object.")
	}
	return strings.Join(lines, "\n")
}

func directImportantEdges(g *graph.Graph, obj graph.Object) []graph.Edge {
	var edges []graph.Edge
	for _, pod := range relatedPods(g, obj) {
		for _, edge := range g.EdgesFor(pod.Ref) {
			if edge.From.Key() != obj.Ref.Key() {
				if edge.From.Key() != pod.Ref.Key() {
					continue
				}
			}
			switch edge.To.Kind {
			case "ConfigMap", "Secret", "PersistentVolumeClaim", "ServiceAccount":
				edges = append(edges, edge)
			}
		}
		for _, edge := range g.EdgesFor(pod.Ref) {
			if edge.To.Key() != pod.Ref.Key() {
				continue
			}
			switch edge.From.Kind {
			case "NetworkPolicy":
				edges = append(edges, graph.Edge{From: pod.Ref, To: edge.From, Type: edge.Type, Health: edge.Health, Source: edge.Source, Reason: edge.Reason})
			}
		}
	}
	if obj.Ref.Kind == "Pod" {
		return edges
	}
	for _, edge := range g.EdgesFor(obj.Ref) {
		if edge.From.Key() != obj.Ref.Key() {
			continue
		}
		switch edge.To.Kind {
		case "ConfigMap", "Secret", "PersistentVolumeClaim", "ServiceAccount", "Service", "EndpointSlice", "Endpoints":
			edges = append(edges, edge)
		}
	}
	for _, edge := range g.EdgesFor(obj.Ref) {
		if edge.To.Key() != obj.Ref.Key() {
			continue
		}
		switch edge.From.Kind {
		case "Ingress", "Route", "HorizontalPodAutoscaler", "NetworkPolicy", "Certificate":
			edges = append(edges, graph.Edge{From: obj.Ref, To: edge.From, Type: edge.Type, Health: edge.Health, Source: edge.Source, Reason: edge.Reason})
		}
	}
	return edges
}

func relatedPods(g *graph.Graph, obj graph.Object) []graph.Object {
	if obj.Ref.Kind == "Pod" {
		return []graph.Object{obj}
	}
	seen := map[string]bool{obj.Ref.Key(): true}
	var pods []graph.Object
	var walk func(graph.ObjectRef)
	walk = func(ref graph.ObjectRef) {
		for _, edge := range g.EdgesFor(ref) {
			if edge.Type != "Owns" || edge.From.Key() != ref.Key() || seen[edge.To.Key()] {
				continue
			}
			seen[edge.To.Key()] = true
			child, ok := g.ObjectByKey(edge.To.Key())
			if !ok {
				continue
			}
			if child.Ref.Kind == "Pod" {
				pods = append(pods, child)
			}
			walk(child.Ref)
		}
	}
	walk(obj.Ref)
	sort.Slice(pods, func(i, j int) bool {
		hi := podSortHealth(pods[i])
		hj := podSortHealth(pods[j])
		if hi != hj {
			return hi > hj
		}
		return pods[i].Raw.GetCreationTimestamp().After(pods[j].Raw.GetCreationTimestamp().Time)
	})
	return pods
}

func podSortHealth(pod graph.Object) int {
	_, health := resourceStatus(pod)
	switch health {
	case "bad":
		return 3
	case "warn":
		return 2
	case "good":
		return 1
	default:
		return 0
	}
}

func (m model) logPods(obj graph.Object) map[string][]string {
	pods := relatedPods(m.snapshot.Graph, obj)
	out := map[string][]string{}
	for i, pod := range pods {
		if i >= 5 {
			break
		}
		containers := containerNames(pod)
		if len(containers) == 0 {
			containers = []string{""}
		}
		out[pod.Ref.Name] = containers
	}
	return out
}

func containerNames(obj graph.Object) []string {
	var names []string
	for _, path := range [][]string{{"spec", "containers"}, {"spec", "initContainers"}} {
		containers, _, _ := unstructuredNestedSlice(obj, path...)
		for _, container := range containers {
			c, ok := container.(map[string]any)
			if !ok {
				continue
			}
			name, _, _ := nestedString(c, "name")
			if name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func (m model) logsView(width, maxLines int) string {
	header := "follow"
	if m.logPaused {
		header = "paused"
	}
	if m.logPrevious || m.selectedPrefersPreviousLogs() {
		header += " previous"
	}
	if m.logTimestamps {
		header += " ts"
	}
	if m.logWrap {
		header += " wrap"
	}
	if m.logGrep != "" {
		header += " grep=" + m.logGrep
	}
	if m.logSince > 0 {
		header += " since=" + m.logSince.String()
	}
	lines := wrapLine(header+"  space pause  P previous  t timestamps  w wrap  :grep  :since", width)
	if m.logErr != "" {
		lines = appendPanelLines(lines, []string{"error: " + m.logErr}, width, maxLines)
		return strings.Join(limitTail(lines, maxLines), "\n")
	}
	if len(m.logLines) == 0 {
		lines = appendPanelLines(lines, []string{"Loading logs..."}, width, maxLines)
		return strings.Join(lines, "\n")
	}
	logs := m.logLines
	if len(logs) > maxLines-1 {
		logs = logs[len(logs)-(maxLines-1):]
	}
	for _, line := range logs {
		if m.logWrap {
			lines = append(lines, wrapLine(line, width)...)
		} else {
			lines = append(lines, truncateText(line, width))
		}
		if len(lines) >= maxLines {
			break
		}
	}
	return strings.Join(limitTail(lines, maxLines), "\n")
}

func (m model) selectedPrefersPreviousLogs() bool {
	obj, ok := m.selected()
	if !ok {
		return false
	}
	return shouldPreferPreviousLogs(m.snapshot.Graph, obj)
}

func detailsView(g *graph.Graph, obj graph.Object) string {
	lines := obj.Summary()
	edges := g.EdgesFor(obj.Ref)
	problems := g.ProblemsFor(obj.Ref)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("relations: %d", len(edges)))
	lines = append(lines, fmt.Sprintf("problems: %d", len(problems)))
	lines = append(lines, fmt.Sprintf("events: %d", len(obj.Events)))
	return strings.Join(lines, "\n")
}

func eventsView(obj graph.Object) string {
	if len(obj.Events) == 0 {
		return "No events loaded for this object."
	}
	return strings.Join(obj.Events, "\n")
}

func problemsView(g *graph.Graph, obj graph.Object) string {
	problems := g.ProblemsFor(obj.Ref)
	if len(problems) == 0 {
		return "No graph-derived problems for this object."
	}
	lines := make([]string, 0, len(problems))
	for _, problem := range problems {
		lines = append(lines, fmt.Sprintf("[%s] %s", problem.Level, problem.Message))
	}
	return strings.Join(lines, "\n")
}

func helpView() string {
	return strings.Join([]string{
		"tab  switch pane",
		"/    filter resources",
		":    command mode",
		":q / ctrl-c quit",
		":pod :svc :deploy :sts :ds :job :cj :cm :secret :pvc :all",
		":node :events :hpa :np :pdb :quota :cert",
		":grep <text> / :nogrep / :since 5m",
		":sort age|old|default",
		"space pause/follow logs",
		"A    sort by age newest/oldest/default",
		"w    wrap logs",
		"P    previous logs",
		"t    timestamps",
		":ctx <name> / :ctx",
		":ns / :namespace",
		":ns <namespace>",
		":kubeconfig <path>",
		":refresh",
		"r    relations",
		"d    details",
		"y    YAML",
		"e    events",
		"p    problems",
		"?    help",
	}, "\n")
}

func (m model) footer() string {
	text := m.status
	if m.commandMode {
		text = ":" + m.command
	}
	if text == "" {
		text = ":pod :deploy :sts :ds :job :cj :svc :node :events  A age-sort  :grep :since  space pause  P prev  t ts  w wrap  :q"
	}
	return lipgloss.NewStyle().
		Width(max(1, m.width)).
		Foreground(lipgloss.Color("245")).
		Render(truncateText(text, max(1, m.width-1)))
}

func paneTitle(title, body string, width, height int, active bool) string {
	bodyWidth := max(8, width-4)
	content := titleStyle.Render(truncateText(title, bodyWidth)) + "\n" + fitPanelText(body, bodyWidth, height-2)
	return paneStyle(active).Width(width).Height(height).Render(content)
}

func paneStyle(active bool) lipgloss.Style {
	border := lipgloss.RoundedBorder()
	color := lipgloss.Color("240")
	if active {
		color = lipgloss.Color("39")
	}
	return lipgloss.NewStyle().Border(border).BorderForeground(color).Padding(0, 1)
}

var titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))

func shortStatus(obj graph.Object) string {
	status, _ := resourceStatus(obj)
	return status
}

func resourceStatus(obj graph.Object) (string, string) {
	switch obj.Ref.Kind {
	case "Pod":
		status := podDisplayStatus(obj)
		return status, podHealth(status)
	case "PersistentVolumeClaim":
		phase, _, _ := unstructuredNestedString(obj, "status", "phase")
		return phase, statusHealth(phase)
	case "Service":
		typ, _, _ := unstructuredNestedString(obj, "spec", "type")
		clusterIP, _, _ := unstructuredNestedString(obj, "spec", "clusterIP")
		if clusterIP != "" {
			return typ + " " + clusterIP, "neutral"
		}
		return typ, "neutral"
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet":
		ready, _, _ := unstructuredNestedInt64(obj, "status", "readyReplicas")
		replicas, _, _ := unstructuredNestedInt64(obj, "status", "replicas")
		if obj.Ref.Kind == "DaemonSet" {
			ready, _, _ = unstructuredNestedInt64(obj, "status", "numberReady")
			replicas, _, _ = unstructuredNestedInt64(obj, "status", "desiredNumberScheduled")
		}
		status := fmt.Sprintf("%d/%d ready", ready, replicas)
		if replicas == 0 {
			return status, "neutral"
		}
		if ready < replicas {
			return status, "bad"
		}
		return status, "good"
	case "Job":
		succeeded, _, _ := unstructuredNestedInt64(obj, "status", "succeeded")
		failed, _, _ := unstructuredNestedInt64(obj, "status", "failed")
		active, _, _ := unstructuredNestedInt64(obj, "status", "active")
		status := fmt.Sprintf("%d succeeded  %d active  %d failed", succeeded, active, failed)
		if failed > 0 {
			return status, "bad"
		}
		if active > 0 {
			return status, "warn"
		}
		if succeeded > 0 {
			return status, "good"
		}
		return status, "neutral"
	case "CronJob":
		suspended, _, _ := unstructuredNestedBool(obj, "spec", "suspend")
		active, _, _ := unstructuredNestedSlice(obj, "status", "active")
		status := fmt.Sprintf("%d active", len(active))
		if suspended {
			status += " suspended"
		}
		return status, statusHealth(status)
	case "Node":
		status := nodeStatus(obj)
		return status, statusHealth(status)
	case "Event":
		typ, _, _ := unstructuredNestedString(obj, "type")
		reason, _, _ := unstructuredNestedString(obj, "reason")
		status := strings.TrimSpace(typ + " " + reason)
		return status, statusHealth(status)
	case "ResourceQuota":
		return quotaStatus(obj), "neutral"
	case "HorizontalPodAutoscaler":
		current, _, _ := unstructuredNestedInt64(obj, "status", "currentReplicas")
		desired, _, _ := unstructuredNestedInt64(obj, "status", "desiredReplicas")
		return fmt.Sprintf("%d/%d replicas", current, desired), "neutral"
	case "Certificate":
		status := conditionStatus(obj)
		return status, statusHealth(status)
	}
	return obj.Ref.Namespace, "neutral"
}

func podDisplayStatus(obj graph.Object) string {
	phase, _, _ := unstructuredNestedString(obj, "status", "phase")
	ready, total := podReady(obj)
	restarts := podRestarts(obj)
	reason := podImportantReason(obj)
	if reason != "" {
		return fmt.Sprintf("%s %d/%d restarts:%d", reason, ready, total, restarts)
	}
	return fmt.Sprintf("%s %d/%d restarts:%d", phase, ready, total, restarts)
}

func podImportantReason(obj graph.Object) string {
	for _, path := range [][]string{{"status", "initContainerStatuses"}, {"status", "containerStatuses"}} {
		statuses, _, _ := unstructuredNestedSlice(obj, path...)
		for _, status := range statuses {
			c, ok := status.(map[string]any)
			if !ok {
				continue
			}
			reason, _, _ := nestedString(c, "state", "waiting", "reason")
			if reason != "" {
				return reason
			}
			reason, _, _ = nestedString(c, "state", "terminated", "reason")
			if reason != "" && reason != "Completed" {
				return reason
			}
			reason, _, _ = nestedString(c, "lastState", "terminated", "reason")
			if reason != "" && reason != "Completed" {
				return reason
			}
		}
	}
	return ""
}

func podRestarts(obj graph.Object) int64 {
	var restarts int64
	statuses, _, _ := unstructuredNestedSlice(obj, "status", "containerStatuses")
	for _, status := range statuses {
		c, ok := status.(map[string]any)
		if !ok {
			continue
		}
		count, _, _ := nestedInt64(c, "restartCount")
		restarts += count
	}
	return restarts
}

func podHealth(status string) string {
	normalized := strings.ToLower(status)
	switch {
	case strings.Contains(normalized, "crashloop"),
		strings.Contains(normalized, "imagepull"),
		strings.Contains(normalized, "errimage"),
		strings.Contains(normalized, "error"),
		strings.Contains(normalized, "failed"),
		strings.Contains(normalized, "createcontainer"):
		return "bad"
	case strings.Contains(normalized, "pending"),
		strings.Contains(normalized, "containercreating"),
		strings.Contains(normalized, "podinitializing"),
		strings.Contains(normalized, "unknown"):
		return "warn"
	case strings.Contains(normalized, "running"):
		return "good"
	case strings.Contains(normalized, "succeeded"),
		strings.Contains(normalized, "completed"):
		return "neutral"
	default:
		return statusHealth(status)
	}
}

func nodeStatus(obj graph.Object) string {
	conditions, _, _ := unstructuredNestedSlice(obj, "status", "conditions")
	for _, condition := range conditions {
		c, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := nestedString(c, "type")
		status, _, _ := nestedString(c, "status")
		if typ == "Ready" {
			if status == "True" {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

func quotaStatus(obj graph.Object) string {
	used, _, _ := unstructuredNestedStringMap(obj, "status", "used")
	hard, _, _ := unstructuredNestedStringMap(obj, "status", "hard")
	if len(used) == 0 || len(hard) == 0 {
		return obj.Ref.Namespace
	}
	keys := make([]string, 0, len(hard))
	for key := range hard {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 2 {
		keys = keys[:2]
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%s/%s", key, used[key], hard[key]))
	}
	return strings.Join(parts, " ")
}

func conditionStatus(obj graph.Object) string {
	conditions, _, _ := unstructuredNestedSlice(obj, "status", "conditions")
	if len(conditions) == 0 {
		return obj.Ref.Namespace
	}
	for _, condition := range conditions {
		c, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := nestedString(c, "type")
		status, _, _ := nestedString(c, "status")
		reason, _, _ := nestedString(c, "reason")
		if status != "True" {
			if reason != "" {
				return typ + " " + status + " " + reason
			}
			return typ + " " + status
		}
	}
	return "Ready"
}

func podReady(obj graph.Object) (int, int) {
	conditions, _, _ := unstructuredNestedSlice(obj, "status", "containerStatuses")
	total := len(conditions)
	ready := 0
	for _, condition := range conditions {
		c, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		if value, ok := c["ready"].(bool); ok && value {
			ready++
		}
	}
	return ready, total
}

func resourceAge(obj graph.Object) string {
	created := obj.Raw.GetCreationTimestamp().Time
	if created.IsZero() {
		return ""
	}
	age := time.Since(created)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 48*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

func truncateLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n") + "\n..."
}

func fitPanelText(s string, width, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, wrapLine(line, width)...)
		if len(out) >= maxLines {
			break
		}
	}
	if len(out) > maxLines {
		out = out[:maxLines]
	}
	if len(out) == maxLines && len(strings.Split(s, "\n")) > maxLines {
		out[maxLines-1] = truncateText(out[maxLines-1], max(1, width-3)) + "..."
	}
	return strings.Join(out, "\n")
}

func appendPanelLines(dst, src []string, width, maxLines int) []string {
	for _, line := range src {
		dst = append(dst, wrapLine(line, width)...)
		if len(dst) >= maxLines {
			return dst[:maxLines]
		}
	}
	return dst
}

func wrapLine(line string, width int) []string {
	width = max(1, width)
	if len(line) <= width {
		return []string{line}
	}
	var lines []string
	remaining := line
	for len(remaining) > width {
		cut := width
		if idx := strings.LastIndex(remaining[:width], " "); idx > width/2 {
			cut = idx
		}
		lines = append(lines, strings.TrimSpace(remaining[:cut]))
		remaining = strings.TrimSpace(remaining[cut:])
	}
	if remaining != "" {
		lines = append(lines, remaining)
	}
	return lines
}

func limitTail(lines []string, maxLines int) []string {
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	return lines[len(lines)-maxLines:]
}

func truncateText(s string, maxWidth int) string {
	if len(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return s[:maxWidth]
	}
	return s[:maxWidth-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
