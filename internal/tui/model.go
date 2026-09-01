package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/filipcsupka/krel/internal/graph"
	"github.com/filipcsupka/krel/internal/kube"
)

type pane int

// Order matches the visual/tab-cycle layout: top-left, bottom-left,
// top-right, bottom-right.
const (
	paneResources pane = iota
	paneChain
	paneRelations
	paneStatus
	paneCount
)

type model struct {
	snapshot                kube.Snapshot
	list                    list.Model
	width                   int
	height                  int
	active                  pane
	mode                    string
	commandMode             bool
	namespacePicker         bool
	contextPicker           bool
	command                 string
	commandHistory          []string
	historyCursor           int
	lastResourceCommand     string
	previousResourceCommand string
	status                  string
	loading                 bool
	resourceKind            string
	resourceFilter          string
	resourceFuzzy           bool
	warningsOnly            bool
	sortMode                string
	logLines                []string
	logErr                  string
	logKey                  string
	logPaused               bool
	logWrap                 bool
	logGrep                 string
	logPrevious             bool
	logTimestamps           bool
	logSince                time.Duration
	logScroll               int
	logRequestID            string
	logEvents               <-chan kube.LogEvent
	logCancel               context.CancelFunc
	logSearch               string
	logSearchMode           bool
	statusSearch            string
	statusSearchMode        bool
	summaryCursor           int
	statusScroll            int
	viewScroll              int
	viewSearch              string
	viewSearchMode          bool
	relationSearch          string
	relationSearchMode      bool
	chainCursor             int
	chainSearch             string
	chainSearchMode         bool
	logsFullscreen          bool
	detailFullscreen        bool
	navStack                []navigationState
	autoRefresh             bool
	hideHeader              bool
	hideBreadcrumbs         bool
	wideList                bool
	marks                   map[string]bool
	markAnchor              string
}

func (m model) pickerActive() bool {
	return m.namespacePicker || m.contextPicker
}

type navigationState struct {
	kind           string
	key            string
	warningsOnly   bool
	resourceFilter string
}

type reloadResult struct {
	snapshot   kube.Snapshot
	err        error
	background bool
}

type refreshTickMsg struct{}

type logStreamStarted struct {
	id     string
	events <-chan kube.LogEvent
	cancel context.CancelFunc
}

type logEventMsg struct {
	id    string
	event kube.LogEvent
	open  bool
}

type logReconnect struct {
	id string
}

type item struct {
	obj           graph.Object
	showNamespace bool
	wide          bool
	marked        bool
}

func (i item) Title() string {
	if i.showNamespace && i.obj.Ref.Namespace != "" {
		return i.obj.Ref.Kind + "/" + i.obj.Ref.Namespace + "/" + i.obj.Ref.Name
	}
	return i.obj.Ref.Label()
}
func (i item) Description() string { return shortStatus(i.obj) }
func (i item) FilterValue() string {
	labels := i.obj.Raw.GetLabels()
	annotations := i.obj.Raw.GetAnnotations()
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+"="+labels[key])
	}
	annotationKeys := make([]string, 0, len(annotations))
	for key := range annotations {
		annotationKeys = append(annotationKeys, key)
	}
	sort.Strings(annotationKeys)
	for _, key := range annotationKeys {
		values = append(values, key+"="+annotations[key])
	}
	if len(values) == 0 {
		return i.Title()
	}
	return i.Title() + " " + strings.Join(values, " ")
}

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

type contextItem struct {
	name    string
	current bool
}

func (i contextItem) Title() string { return i.name }
func (i contextItem) Description() string {
	if i.current {
		return "current"
	}
	return "context"
}
func (i contextItem) FilterValue() string { return i.name }

func New(snapshot kube.Snapshot) tea.Model {
	return model{snapshot: snapshot, list: newResourceList(snapshot, "Pod"), mode: "relations", resourceKind: "Pod", logTimestamps: true, autoRefresh: true}
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
	wide := false
	filterText := ""
	if len(kinds) > 3 {
		for _, option := range kinds[3:] {
			wide = wide || option == "wide"
			if strings.HasPrefix(option, "filter=") {
				filterText = strings.TrimPrefix(option, "filter=")
			}
		}
	}
	inverseFilter := strings.HasPrefix(filterText, "!")
	if inverseFilter {
		filterText = strings.TrimSpace(strings.TrimPrefix(filterText, "!"))
	}
	labelSelector := labels.Everything()
	labelFilter := false
	if strings.HasPrefix(filterText, "-l ") {
		if parsed, err := labels.Parse(strings.TrimSpace(strings.TrimPrefix(filterText, "-l "))); err == nil {
			labelSelector = parsed
			labelFilter = true
		}
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
		if labelFilter && !labelSelector.Matches(labels.Set(obj.Raw.GetLabels())) {
			continue
		}
		if inverseFilter && !labelFilter && matchesResourceFilter(obj, filterText) {
			continue
		}
		items = append(items, item{obj: obj, showNamespace: snapshot.Options.AllNamespaces, wide: wide})
	}
	sortItems(items, sortMode)
	l := list.New(items, resourceDelegate{}, 34, 20)
	title := fmt.Sprintf("config: %s  ctx: %s  ns: %s", configLabel(snapshot), snapshot.Context, snapshot.Namespace)
	if kind != "" {
		title += fmt.Sprintf("  kind: %s [%d]", kind, len(items))
	} else {
		title += fmt.Sprintf("  resources [%d]", len(items))
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

// configLabel is the short name shown in the top crumb for the active
// kubeconfig: the file's basename without extension (e.g. "pft" for
// ~/.kube/pft.yaml), or "default" when no explicit kubeconfig path was
// given (client-go's default loading rules: $KUBECONFIG or ~/.kube/config).
func configLabel(snapshot kube.Snapshot) string {
	path := snapshot.Options.Kubeconfig
	if path == "" {
		return "default"
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func warningFilter(enabled bool) string {
	if enabled {
		return "warnings"
	}
	return ""
}

func podListHeader() string {
	return "NAME                         READY   STATUS             RESTARTS   NODE             IP        AGE"
}

func (m model) resourceList() list.Model {
	selectedKey := m.selectedKey()
	kinds := []string{m.resourceKind, warningFilter(m.warningsOnly), m.sortMode}
	if m.resourceFilter != "" {
		kinds = append(kinds, "filter="+m.resourceFilter)
	}
	if m.wideList {
		kinds = append(kinds, "wide")
	}
	l := newResourceList(m.snapshot, kinds...)
	applyResourceMarks(&l, m.marks)
	if m.podTableLayout() {
		l.Title += "\n" + podListHeader()
	}
	l.SetShowTitle(!m.hideHeader)
	if m.hideBreadcrumbs {
		kind := m.resourceKind
		if kind == "" {
			kind = "all resources"
		}
		title := fmt.Sprintf("kind: %s [%d]", kind, len(l.Items()))
		if m.warningsOnly {
			title += "  warnings"
		}
		if m.sortMode != "" {
			title += "  sort:" + sortModeLabel(m.sortMode)
		}
		if m.podTableLayout() {
			title += "\n" + podListHeader()
		}
		l.Title = title
	}
	if m.resourceFilter != "" && !strings.HasPrefix(m.resourceFilter, "!") && !strings.HasPrefix(m.resourceFilter, "-l ") {
		l.SetFilterText(m.resourceFilter)
		l.SetFilterState(list.FilterApplied)
		setResourceListCount(&l)
	}
	if selectedKey != "" {
		for i, listItem := range l.Items() {
			if it, ok := listItem.(item); ok && it.obj.Ref.Key() == selectedKey {
				l.Select(i)
				break
			}
		}
	}
	return l
}

func (m model) podTableLayout() bool {
	return m.resourceKind == "Pod" && (m.wideList || leftPaneOuterWidth(m.width)-paneBoxOverhead >= 82)
}

func applyResourceMarks(l *list.Model, marks map[string]bool) {
	if len(marks) == 0 {
		return
	}
	items := l.Items()
	for i, listItem := range items {
		it, ok := listItem.(item)
		if !ok {
			continue
		}
		it.marked = marks[it.obj.Ref.Key()]
		items[i] = it
	}
	l.SetItems(items)
}

func setResourceListCount(l *list.Model) {
	start := strings.Index(l.Title, " [")
	if start < 0 {
		return
	}
	end := strings.IndexByte(l.Title[start:], ']')
	if end < 0 {
		return
	}
	end += start
	l.Title = l.Title[:start] + fmt.Sprintf(" [%d]", len(l.VisibleItems())) + l.Title[end+1:]
}

func matchesResourceFilter(obj graph.Object, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return false
	}
	value := obj.Ref.Label() + " " + obj.Ref.Namespace + " " + obj.Ref.Name
	for key, label := range obj.Raw.GetLabels() {
		value += " " + key + "=" + label
	}
	for key, annotation := range obj.Raw.GetAnnotations() {
		value += " " + key + "=" + annotation
	}
	if re, err := regexp.Compile(filter); err == nil {
		return re.MatchString(value)
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(filter))
}

// leftListSize returns the resource list's width/height within the smaller
// top-left quadrant of the 4-pane layout.
// leftPaneOuterWidth is the outer (bordered) width of the whole left
// column; leftListSize's returned width is narrower than this by
// paneBoxOverhead to match the padded content area the box actually
// renders into (border + Padding(0,1) eat 4 columns) — sizing the list to
// the outer width instead causes bubbles list to wrap its own rows and
// silently grow taller than requested.
const paneBoxOverhead = 4
const paneGap = 1

func leftPaneOuterWidth(termWidth int) int {
	return max(34, termWidth*44/100)
}

// leftListSize returns the resource list's content width/height. The left
// column is split 50/50 with the Logs pane below it, matching the 50/50
// width split between the left and right columns.
func (m model) leftListSize() (int, int) {
	if m.compactLayout() {
		return max(20, m.width-paneBoxOverhead), max(4, m.height-3)
	}
	outerWidth := leftPaneOuterWidth(m.width)
	leftTopHeight, _ := m.leftLayoutHeights()
	return max(20, outerWidth-paneBoxOverhead), max(4, leftTopHeight-2)
}

func (m model) compactLayout() bool {
	return m.width < 100 || m.height < 22
}

func (m model) leftLayoutHeights() (int, int) {
	mainHeight := max(8, m.height-1)
	chainLines := 1
	if obj, ok := m.selected(); ok {
		chainLines = len(ownerChain(m.snapshot.Graph, obj))
		if gitopsLine(ownerChain(m.snapshot.Graph, obj)) != "" {
			chainLines++
		}
	}
	// title + borders plus the actual chain, capped so the resource list
	// remains the dominant navigation surface.
	bottom := minInt(max(5, chainLines+3), max(5, mainHeight*35/100))
	top := max(8, mainHeight-bottom)
	return top, max(5, mainHeight-top)
}

func newNamespaceList(snapshot kube.Snapshot) list.Model {
	items := make([]list.Item, 0, len(snapshot.Namespaces))
	for _, namespace := range snapshot.Namespaces {
		items = append(items, namespaceItem{name: namespace, current: namespace == snapshot.Namespace})
	}
	l := list.New(items, list.NewDefaultDelegate(), 34, 20)
	l.Title = fmt.Sprintf("config: %s  ctx: %s  namespaces", configLabel(snapshot), snapshot.Context)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	return l
}

func newContextList(snapshot kube.Snapshot) list.Model {
	items := make([]list.Item, 0, len(snapshot.Contexts))
	for _, contextName := range snapshot.Contexts {
		items = append(items, contextItem{name: contextName, current: contextName == snapshot.Context})
	}
	l := list.New(items, list.NewDefaultDelegate(), 34, 20)
	l.Title = fmt.Sprintf("config: %s  contexts", configLabel(snapshot))
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
		case "status":
			leftStatus, _ := resourceStatus(left)
			rightStatus, _ := resourceStatus(right)
			_, leftHealth := resourceStatus(left)
			_, rightHealth := resourceStatus(right)
			if leftRank, rightRank := healthSortRank(leftHealth), healthSortRank(rightHealth); leftRank != rightRank {
				return leftRank < rightRank
			}
			if leftStatus == rightStatus {
				return left.Ref.Name < right.Ref.Name
			}
			return leftStatus < rightStatus
		case "namespace":
			if left.Ref.Namespace == right.Ref.Namespace {
				return left.Ref.Name < right.Ref.Name
			}
			return left.Ref.Namespace < right.Ref.Namespace
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
	case "status", "state":
		return "status", true
	case "namespace", "ns":
		return "namespace", true
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
	case "status":
		return "status"
	case "namespace":
		return "namespace"
	default:
		return "default"
	}
}

func healthSortRank(health string) int {
	switch health {
	case "bad":
		return 0
	case "warn":
		return 1
	case "good":
		return 2
	default:
		return 3
	}
}

func (m model) Init() tea.Cmd {
	if !m.autoRefresh {
		return nil
	}
	return scheduleRefresh()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		widthChanged := m.width != msg.Width
		m.width = msg.Width
		m.height = msg.Height
		if widthChanged && !m.pickerActive() {
			m.list = m.resourceList()
		}
		lw, lh := m.leftListSize()
		m.list.SetSize(lw, lh)
	case reloadResult:
		selectedKey := m.selectedKey()
		m.loading = false
		if msg.err != nil {
			m.status = "reload failed: " + msg.err.Error()
			return m, scheduleRefreshIfEnabled(m.autoRefresh)
		}
		m.snapshot = msg.snapshot
		m.namespacePicker = false
		m.contextPicker = false
		if msg.snapshot.Options.ResourceKind != "" {
			if resource, ok := msg.snapshot.ResolveResource(msg.snapshot.Options.ResourceKind); ok {
				m.resourceKind = resource.Kind
			} else {
				m.resourceKind = msg.snapshot.Options.ResourceKind
			}
		}
		m.list = m.resourceList()
		lw, lh := m.leftListSize()
		m.list.SetSize(lw, lh)
		m.selectResourceKey(selectedKey)
		if m.logsFullscreen && m.selectedKey() == "" && m.logCancel != nil {
			m.logCancel()
			m.logCancel = nil
			m.logEvents = nil
		}
		if !msg.background {
			m.status = fmt.Sprintf("loaded context %s namespace %s", msg.snapshot.Context, msg.snapshot.Namespace)
		}
		return m, tea.Batch(m.loadSelectedLogs(), scheduleRefreshIfEnabled(m.autoRefresh))
	case refreshTickMsg:
		if !m.autoRefresh {
			return m, nil
		}
		if m.loading || m.commandMode || m.logSearchMode || m.viewSearchMode || m.statusSearchMode || m.relationSearchMode || m.pickerActive() {
			return m, scheduleRefresh()
		}
		m.loading = true
		return m, reloadInBackground(m.snapshot.Options)
	case logStreamStarted:
		if msg.id != m.currentLogRequestID() || !m.logsFullscreen {
			msg.cancel()
			return m, nil
		}
		if m.logCancel != nil {
			m.logCancel()
		}
		if m.logRequestID != msg.id {
			m.logLines = nil
			m.logScroll = 0
		}
		m.logRequestID = msg.id
		m.logEvents = msg.events
		m.logCancel = msg.cancel
		m.logKey = m.selectedKey()
		m.logErr = ""
		return m, waitForLogEvent(msg.id, msg.events)
	case logEventMsg:
		if msg.id != m.logRequestID || !m.logsFullscreen {
			return m, nil
		}
		if !msg.open {
			m.logEvents = nil
			m.logCancel = nil
			if m.logPrevious {
				return m, nil
			}
			return m, scheduleLogReconnect(msg.id)
		}
		if msg.event.Err != nil {
			prefix := ""
			if msg.event.Source != "" {
				prefix = msg.event.Source + ": "
			}
			if len(m.logLines) > 0 {
				m.logLines = append(m.logLines, "error: "+prefix+msg.event.Err.Error())
			} else {
				m.logErr = prefix + msg.event.Err.Error()
			}
			return m, waitForLogEvent(msg.id, m.logEvents)
		}
		line := msg.event.Line
		if msg.event.Source != "" {
			if timestamp, rest, ok := splitLogTimestamp(line); ok {
				line = timestamp + " [" + msg.event.Source + "] " + rest
			} else {
				line = "[" + msg.event.Source + "] " + line
			}
		}
		if line != "" {
			m.logErr = ""
			m.logLines = append(m.logLines, line)
			if m.logPaused || m.logScroll > 0 {
				m.logScroll++
			}
			if len(m.logLines) > 4000 {
				drop := len(m.logLines) - 4000
				m.logLines = m.logLines[drop:]
				m.logScroll = minInt(m.logScroll, len(m.logLines)-1)
			}
			// An offset from the tail represents a deliberately frozen
			// viewport. Grow it as lines arrive so the visible history does
			// not jump; offset zero remains pinned to the live tail.
		}
		return m, waitForLogEvent(msg.id, m.logEvents)
	case logReconnect:
		if msg.id != m.logRequestID || m.logPrevious || !m.logsFullscreen {
			return m, nil
		}
		return m, m.loadSelectedLogs()
	case tea.KeyMsg:
		if m.commandMode {
			return m.updateCommand(msg)
		}
		if m.logSearchMode {
			return m.updateLogSearch(msg)
		}
		if m.viewSearchMode {
			return m.updateViewSearch(msg)
		}
		if m.chainSearchMode {
			return m.updateChainSearch(msg)
		}
		if m.statusSearchMode {
			return m.updateStatusSearch(msg)
		}
		if m.relationSearchMode {
			return m.updateRelationSearch(msg)
		}
		if m.detailFullscreen {
			switch msg.String() {
			case "esc", "f":
				m.detailFullscreen = false
				return m, nil
			}
		}
		if m.logsFullscreen {
			switch msg.String() {
			case "esc", "l":
				if m.logCancel != nil {
					m.logCancel()
					m.logCancel = nil
				}
				m.logsFullscreen = false
				m.status = ""
				return m, nil
			case "/":
				m.logSearchMode = true
				m.command = ""
				return m, nil
			case "n":
				m.jumpToLogMatch(1)
				return m, nil
			case "N":
				m.jumpToLogMatch(-1)
				return m, nil
			case "p":
				m.logPrevious = !m.logPrevious
				m.status = "previous logs " + onOff(m.logPrevious)
				return m, m.loadSelectedLogs()
			// Scroll offset counts back from the tail: k/up moves further
			// into history (increments), j/down moves back toward the live
			// tail (decrements) until it re-reaches 0, matching the
			// "logScroll == 0 means following" convention logsView renders.
			case "k", "up":
				if max := len(m.logLines) - 1; m.logScroll < max {
					m.logScroll++
				}
				return m, nil
			case "j", "down":
				if m.logScroll > 0 {
					m.logScroll--
				}
				return m, nil
			case "pgup", "ctrl+u":
				m.logScroll = minInt(max(0, len(m.logLines)-1), m.logScroll+max(1, m.height/2))
				return m, nil
			case "pgdown", "ctrl+d":
				m.logScroll = max(0, m.logScroll-max(1, m.height/2))
				return m, nil
			case "home":
				m.logScroll = max(0, len(m.logLines)-1)
				return m, nil
			case "end":
				m.logScroll = 0
				return m, nil
			case "G":
				m.logScroll = 0
				m.status = "log tail: live"
				return m, nil
			}
		}
		// While the resource/namespace list is actively capturing filter
		// text ("/" pressed, not yet committed with enter or cancelled
		// with esc), the filter input owns every keystroke exclusively —
		// letters, backspace, enter, and esc all go straight to the list
		// widget, matching k9s's filter-prompt behavior. Without this the
		// global shortcut switch below steals keys (e.g. "y"/"e"/"r" flip
		// the right-pane mode, backspace is a global no-op, esc resets
		// pane mode instead of cancelling the filter) mid-keystroke.
		if m.list.FilterState() == list.Filtering {
			before := m.selectedKey()
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			filter := strings.TrimSpace(m.list.FilterValue())
			// k9s reserves the -f prefix for fuzzy filtering. Bubble List
			// would otherwise search for the literal prefix and temporarily
			// hide every row. Strip it as soon as it is complete, while
			// retaining the filtering state until Enter accepts the query.
			if strings.HasPrefix(filter, "-f") {
				m.resourceFuzzy = true
				filter = strings.TrimSpace(strings.TrimPrefix(filter, "-f"))
				m.list.SetFilterText(filter)
				m.list.SetFilterState(list.Filtering)
			}
			// Bubble List handles ordinary fuzzy/regex filtering well. Keep
			// k9s' two useful filter prefixes as model state so they survive
			// pane redraws, refreshes, and resize events too.
			if m.list.FilterState() == list.FilterApplied {
				filter := strings.TrimSpace(m.list.FilterValue())
				if strings.HasPrefix(filter, "!") || strings.HasPrefix(filter, "-l ") || m.resourceFuzzy {
					m.resourceFilter = filter
					m.resourceFuzzy = false
					m.list = m.resourceList()
					lw, lh := m.leftListSize()
					m.list.SetSize(lw, lh)
					m.selectResourceKey(before)
					m.status = "filter: " + filter
				}
			}
			if m.list.FilterState() == list.Unfiltered {
				m.resourceFuzzy = false
			}
			after := m.selectedKey()
			if before != after {
				m.navStack = nil
				m.logLines = nil
				m.logErr = ""
				m.logScroll = 0
				m.summaryCursor = 0
				m.statusScroll = 0
				m.statusSearch = ""
				m.relationSearch = ""
				m.chainSearch = ""
				m.chainCursor = 0
				m.mode = "relations"
				return m, tea.Batch(cmd, m.loadSelectedLogs())
			}
			return m, cmd
		}
		if !m.pickerActive() && !m.logsFullscreen && m.active == paneChain {
			switch msg.String() {
			case "j", "down":
				if navIndexes := m.chainNavIndexes(); m.chainCursor < len(navIndexes)-1 {
					m.chainCursor++
				}
				return m, nil
			case "k", "up":
				if m.chainCursor > 0 {
					m.chainCursor--
				}
				return m, nil
			case "enter":
				return m.jumpToChainRef()
			case "ctrl+f":
				m.chainSearchMode = true
				m.command = ""
				m.status = "find owner chain: type text, enter filter, esc cancel"
				return m, nil
			}
		}
		if !m.pickerActive() && !m.logsFullscreen && m.active == paneRelations && m.mode == "relations" {
			switch msg.String() {
			case "j", "down":
				if navIndexes := m.summaryNavIndexes(); m.summaryCursor < len(navIndexes)-1 {
					m.summaryCursor++
				}
				return m, nil
			case "k", "up":
				if m.summaryCursor > 0 {
					m.summaryCursor--
				}
				return m, nil
			case "enter":
				return m.jumpToSummaryRef()
			}
		}
		if !m.logsFullscreen && m.active == paneStatus {
			switch msg.String() {
			case "j", "down":
				if obj, ok := m.selected(); ok {
					total := len(statusPanelLines(m.snapshot.Graph, obj))
					if m.statusScroll < total-1 {
						m.statusScroll++
					}
				}
				return m, nil
			case "k", "up":
				if m.statusScroll > 0 {
					m.statusScroll--
				}
				return m, nil
			case "pgup", "ctrl+u":
				m.statusScroll += max(1, m.height/2)
				return m, nil
			case "pgdown", "ctrl+d":
				m.statusScroll = max(0, m.statusScroll-max(1, m.height/2))
				return m, nil
			case "home":
				m.statusScroll = 0
				return m, nil
			case "end":
				if obj, ok := m.selected(); ok {
					m.statusScroll = len(statusPanelLines(m.snapshot.Graph, obj))
				}
				return m, nil
			case "G":
				m.statusScroll = 0
				return m, nil
			case "n":
				if m.statusSearch != "" {
					m.jumpToStatusMatch(1)
				} else if obj, ok := m.selected(); ok {
					if err := clipboard.WriteAll(obj.Ref.Namespace); err != nil {
						m.status = "copy namespace failed: " + err.Error()
					} else {
						m.status = "copied namespace: " + orDash(obj.Ref.Namespace)
					}
				}
				return m, nil
			case "N":
				if m.statusSearch != "" {
					m.jumpToStatusMatch(-1)
					return m, nil
				}
				m.sortMode = ""
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				m.status = "sort: name"
				return m, nil
			}
		}
		if !m.logsFullscreen && m.active == paneRelations && m.mode != "relations" {
			switch msg.String() {
			case "j", "down":
				m.viewScroll++
				return m, nil
			case "k", "up":
				if m.viewScroll > 0 {
					m.viewScroll--
				}
				return m, nil
			case "pgdown", "ctrl+d":
				m.viewScroll += max(1, m.height/2)
				return m, nil
			case "pgup", "ctrl+u":
				m.viewScroll = max(0, m.viewScroll-max(1, m.height/2))
				return m, nil
			case "G":
				m.viewScroll = 0
				return m, nil
			case "n":
				m.jumpToViewMatch(1)
				return m, nil
			case "N":
				m.jumpToViewMatch(-1)
				return m, nil
			case "home":
				m.viewScroll = 0
				return m, nil
			case "end":
				if obj, ok := m.selected(); ok {
					m.viewScroll = len(strings.Split(m.viewText(obj), "\n"))
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "[":
			if !m.pickerActive() && !m.logsFullscreen && len(m.commandHistory) > 0 {
				index := m.historyCursor - 1
				if m.historyCursor >= len(m.commandHistory) {
					index = len(m.commandHistory) - 2
					if index < 0 {
						index = 0
					}
				}
				if index >= 0 {
					return m.runHistoryCommand(index)
				}
			}
		case "]":
			if !m.pickerActive() && !m.logsFullscreen && m.historyCursor >= 0 && m.historyCursor < len(m.commandHistory)-1 {
				return m.runHistoryCommand(m.historyCursor + 1)
			}
		case "-":
			if !m.pickerActive() && m.previousResourceCommand != "" {
				return m.runCommand(m.previousResourceCommand)
			}
		case "ctrl+r":
			if !m.pickerActive() {
				m.loading = true
				m.status = "refreshing..."
				return m, reload(m.snapshot.Options)
			}
		case "ctrl+a":
			if !m.pickerActive() {
				m.mode = "aliases"
				m.active = paneRelations
				m.viewScroll = 0
				m.viewSearch = ""
				m.status = "resource aliases (j/k scroll, Ctrl-F find, esc back)"
			}
		case "ctrl+e":
			if !m.pickerActive() {
				m.hideHeader = !m.hideHeader
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				if m.hideHeader {
					m.status = "resource header hidden"
				} else {
					m.status = "resource header shown"
				}
			}
		case "ctrl+g":
			if !m.pickerActive() {
				m.hideBreadcrumbs = !m.hideBreadcrumbs
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				if m.hideBreadcrumbs {
					m.status = "resource breadcrumbs hidden"
				} else {
					m.status = "resource breadcrumbs shown"
				}
			}
		case "ctrl+z":
			if !m.pickerActive() {
				if m.resourceKind != "Event" {
					m.status = "fault filter is available in the Event view"
					break
				}
				m.warningsOnly = !m.warningsOnly
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				if m.warningsOnly {
					m.status = "showing warning events"
				} else {
					m.status = "showing all events"
				}
			}
		case "ctrl+w":
			if !m.pickerActive() {
				m.wideList = !m.wideList
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				if m.wideList {
					m.status = "wide resource columns on"
				} else {
					m.status = "wide resource columns off"
				}
			}
		case "ctrl+s":
			if !m.pickerActive() && !m.logsFullscreen {
				if obj, ok := m.selected(); ok {
					if err := clipboard.WriteAll(obj.YAML()); err != nil {
						m.status = "copy YAML failed: " + err.Error()
					} else {
						m.status = "copied redacted YAML: " + obj.Ref.Label()
					}
				}
			}
		case "ctrl+@", "ctrl+space":
			if !m.pickerActive() && !m.logsFullscreen && m.active == paneResources {
				m.markSelectedRange()
			}
		case "ctrl+\\":
			if !m.pickerActive() && !m.logsFullscreen {
				m.marks = nil
				m.markAnchor = ""
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				m.status = "marks cleared"
			}
		case "c":
			if !m.pickerActive() {
				if obj, ok := m.selected(); ok {
					if err := clipboard.WriteAll(obj.Ref.Name); err != nil {
						m.status = "copy name failed: " + err.Error()
					} else {
						m.status = "copied name: " + obj.Ref.Name
					}
				}
			}
		case "n":
			if !m.pickerActive() {
				if obj, ok := m.selected(); ok {
					if err := clipboard.WriteAll(obj.Ref.Namespace); err != nil {
						m.status = "copy namespace failed: " + err.Error()
					} else {
						m.status = "copied namespace: " + orDash(obj.Ref.Namespace)
					}
				}
			}
		case "esc":
			if m.viewSearchMode {
				m.viewSearchMode = false
				m.command = ""
				return m, nil
			}
			if m.statusSearchMode {
				m.statusSearchMode = false
				m.command = ""
				return m, nil
			}
			if m.relationSearchMode {
				m.relationSearchMode = false
				m.command = ""
				return m, nil
			}
			if m.chainSearchMode {
				m.chainSearchMode = false
				m.command = ""
				return m, nil
			}
			if m.detailFullscreen {
				m.detailFullscreen = false
				return m, nil
			}
			if m.mode == "relations" && m.active == paneRelations && m.relationSearch != "" {
				m.relationSearch = ""
				m.summaryCursor = 0
				return m, nil
			}
			if m.active == paneResources && (m.resourceFilter != "" || m.list.FilterState() == list.FilterApplied) {
				m.resourceFilter = ""
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				m.status = "resource filter cleared"
				return m, nil
			}
			if m.namespacePicker || m.contextPicker {
				m.namespacePicker = false
				m.contextPicker = false
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				m.status = ""
				return m, nil
			}
			if m.mode != "relations" {
				if len(m.navStack) > 0 {
					m.restoreNavigation()
					m.status = "back"
					return m, m.loadSelectedLogs()
				}
				m.mode = "relations"
				m.summaryCursor = 0
				m.viewScroll = 0
				m.viewSearch = ""
				m.chainSearch = ""
				m.status = ""
				return m, nil
			}
			if m.active != paneResources {
				m.active = paneResources
				m.status = ""
			}
			return m, nil
		case "backspace":
			return m, nil
		case "l":
			if m.namespacePicker || m.contextPicker {
				return m, nil
			}
			m.logsFullscreen = true
			m.logLines = nil
			m.logErr = ""
			m.logRequestID = ""
			m.logPaused = false
			m.logScroll = 0
			m.status = ""
			return m, m.loadSelectedLogs()
		case "f":
			if m.active == paneRelations && m.mode != "relations" && !m.pickerActive() {
				m.detailFullscreen = true
				return m, nil
			}
		case "enter":
			if m.contextPicker {
				selected, ok := m.list.SelectedItem().(contextItem)
				if !ok {
					return m, nil
				}
				opts := m.snapshot.Options
				opts.ContextName = selected.name
				opts.Namespace = ""
				opts.AllNamespaces = false
				m.contextPicker = false
				m.loading = true
				m.status = "loading context " + selected.name + "..."
				return m, reload(opts)
			}
			if m.namespacePicker {
				selected, ok := m.list.SelectedItem().(namespaceItem)
				if !ok {
					return m, nil
				}
				opts := m.snapshot.Options
				opts.Namespace = selected.name
				opts.AllNamespaces = false
				m.loading = true
				m.status = "loading namespace " + selected.name + "..."
				return m, reload(opts)
			}
			if m.active == paneResources {
				m.active = paneRelations
				m.mode = "relations"
				m.status = "relations focused; esc returns to resources"
				return m, nil
			}
		case " ":
			if !m.logsFullscreen {
				if !m.pickerActive() && m.active == paneResources {
					m.toggleSelectedMark()
				}
				return m, nil
			}
			m.logPaused = !m.logPaused
			if m.logPaused {
				m.status = "logs paused"
				return m, nil
			}
			m.logScroll = 0
			m.status = "logs following"
			return m, nil
		case "w":
			if !m.logsFullscreen {
				return m, nil
			}
			m.logWrap = !m.logWrap
			if m.logWrap {
				m.status = "log wrap on"
			} else {
				m.status = "log wrap off"
			}
			return m, nil
		case "P":
			if !m.logsFullscreen {
				if !m.snapshot.Options.AllNamespaces {
					m.status = "namespace sort requires all namespaces (:ns all)"
					return m, nil
				}
				m.sortMode = "namespace"
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				m.status = "sort: namespace"
				m.logLines = nil
				m.logErr = ""
				return m, m.loadSelectedLogs()
			}
			m.logPrevious = !m.logPrevious
			if m.logPrevious {
				m.status = "previous logs on"
			} else {
				m.status = "previous logs off"
			}
			return m, m.loadSelectedLogs()
		case "t":
			if !m.logsFullscreen {
				return m, nil
			}
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
			lw, lh := m.leftListSize()
			m.list.SetSize(lw, lh)
			if m.sortMode == "" {
				m.status = "sort: default"
			} else {
				m.status = "sort: " + sortModeLabel(m.sortMode)
			}
			m.logLines = nil
			m.logErr = ""
			return m, m.loadSelectedLogs()
		case "N":
			m.sortMode = ""
			m.list = m.resourceList()
			lw, lh := m.leftListSize()
			m.list.SetSize(lw, lh)
			m.status = "sort: name"
			m.logLines = nil
			m.logErr = ""
			return m, m.loadSelectedLogs()
		case "S":
			m.sortMode = "status"
			m.list = m.resourceList()
			lw, lh := m.leftListSize()
			m.list.SetSize(lw, lh)
			m.status = "sort: status"
			m.logLines = nil
			m.logErr = ""
			return m, m.loadSelectedLogs()
		case ":":
			m.commandMode = true
			m.command = ""
			m.historyCursor = len(m.commandHistory)
		case "ctrl+f":
			if m.active == paneRelations && m.mode == "relations" {
				m.relationSearchMode = true
				m.command = ""
				m.status = "find relations: type text, enter filter, esc cancel"
			} else if m.active == paneRelations && m.mode != "relations" {
				m.viewSearchMode = true
				m.command = ""
				m.status = "find in view: type text, enter search, esc cancel"
			} else if m.active == paneStatus {
				m.statusSearchMode = true
				m.command = ""
				m.status = "find in status: type text, enter search, esc cancel"
			}
		case "tab":
			m.active = (m.active + 1) % paneCount
		case "shift+tab":
			m.active = (m.active + paneCount - 1) % paneCount
		case "J":
			if !m.pickerActive() {
				m.active = paneChain
				m.status = "owner chain focused"
			}
		case "r":
			m.mode = "relations"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
			m.relationSearch = ""
			m.navStack = nil
		case "d":
			m.mode = "details"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
		case "y":
			m.mode = "yaml"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
		case "e":
			m.mode = "events"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
		case "p":
			m.mode = "problems"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
		case "i":
			m.mode = "impact"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
		case "u":
			m.mode = "usedby"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
		case "z":
			if !m.pickerActive() {
				if obj, ok := m.selected(); ok && obj.Ref.Kind == "Deployment" {
					return m.runCommand("rs")
				}
				m.status = "z is available for Deployment resources"
			}
		case "!":
			m.mode = "loaderrors"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
		case "?":
			m.mode = "help"
			m.active = paneRelations
			m.viewScroll = 0
			m.viewSearch = ""
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
		m.logScroll = 0
		m.summaryCursor = 0
		m.statusScroll = 0
		m.chainCursor = 0
		m.mode = "relations"
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
		m.command = deleteLastRune(m.command)
	case "ctrl+u":
		m.command = ""
	case "ctrl+w":
		m.command = deleteLastWord(m.command)
	case "[":
		if len(m.commandHistory) > 0 && m.historyCursor > 0 {
			m.historyCursor--
			m.command = m.commandHistory[m.historyCursor]
		}
	case "]":
		if m.historyCursor < len(m.commandHistory)-1 {
			m.historyCursor++
			m.command = m.commandHistory[m.historyCursor]
		} else {
			m.historyCursor = len(m.commandHistory)
			m.command = ""
		}
	case "tab":
		matches := m.resourceSuggestions(m.command)
		if len(matches) == 1 {
			m.command = matches[0]
		} else if len(matches) > 1 {
			m.status = "matches: " + strings.Join(matches[:minInt(8, len(matches))], ", ")
		}
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) resourceSuggestions(prefix string) []string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	seen := map[string]bool{}
	var matches []string
	for _, resource := range m.snapshot.Resources {
		candidates := append([]string{strings.ToLower(resource.Kind), resource.GVR.Resource, resource.CommandName()}, resource.ShortNames...)
		for _, candidate := range candidates {
			if strings.HasPrefix(strings.ToLower(candidate), prefix) && !seen[candidate] {
				seen[candidate] = true
				matches = append(matches, candidate)
			}
		}
	}
	sort.Strings(matches)
	return matches
}

func (m model) updateLogSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.logSearchMode = false
		m.command = ""
	case "enter":
		m.logSearchMode = false
		m.logSearch = strings.TrimSpace(m.command)
		m.command = ""
		if m.logSearch == "" {
			m.status = "log search cleared"
			return m, nil
		}
		m.status = "log search: " + m.logSearch
		m.jumpToLogMatch(1)
	case "backspace":
		m.command = deleteLastRune(m.command)
	case "ctrl+u":
		m.command = ""
	case "ctrl+w":
		m.command = deleteLastWord(m.command)
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

// updateViewSearch owns Ctrl-F input for YAML/details/events/problems and
// other resource views. Keeping it separate from the resource list filter
// makes search predictable and lets the same viewer work for Secrets,
// ConfigMaps and CRDs without special cases.
func (m model) updateViewSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.viewSearchMode = false
		m.command = ""
	case "enter":
		m.viewSearchMode = false
		m.viewSearch = strings.TrimSpace(m.command)
		m.command = ""
		m.viewScroll = 0
		if m.viewSearch == "" {
			m.status = "view search cleared"
		} else if m.jumpToViewMatch(1) {
			m.status = "view search: " + m.viewSearch
		} else {
			m.status = "view search: no match for " + m.viewSearch
		}
	case "backspace":
		m.command = deleteLastRune(m.command)
	case "ctrl+u":
		m.command = ""
	case "ctrl+w":
		m.command = deleteLastWord(m.command)
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) updateStatusSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.statusSearchMode = false
		m.command = ""
	case "enter":
		m.statusSearchMode = false
		m.statusSearch = strings.TrimSpace(m.command)
		m.command = ""
		m.statusScroll = 0
		if m.statusSearch == "" {
			m.status = "status search cleared"
		} else if m.jumpToStatusMatch(1) {
			m.status = "status search: " + m.statusSearch
		} else {
			m.status = "status search: no match for " + m.statusSearch
		}
	case "backspace":
		m.command = deleteLastRune(m.command)
	case "ctrl+u":
		m.command = ""
	case "ctrl+w":
		m.command = deleteLastWord(m.command)
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) updateRelationSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.relationSearchMode = false
		m.command = ""
	case "enter":
		m.relationSearchMode = false
		m.relationSearch = strings.TrimSpace(m.command)
		m.command = ""
		m.summaryCursor = 0
		if m.relationSearch == "" {
			m.status = "relations search cleared"
		} else {
			m.status = "relations search: " + m.relationSearch
		}
	case "backspace":
		m.command = deleteLastRune(m.command)
	case "ctrl+u":
		m.command = ""
	case "ctrl+w":
		m.command = deleteLastWord(m.command)
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

func (m model) updateChainSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.chainSearchMode = false
		m.command = ""
	case "enter":
		m.chainSearchMode = false
		m.chainSearch = strings.TrimSpace(m.command)
		m.command = ""
		m.chainCursor = 0
		if m.chainSearch == "" {
			m.status = "owner chain search cleared"
		} else if len(m.filteredOwnerChain()) == 0 {
			m.status = "owner chain search: no match for " + m.chainSearch
		} else {
			m.status = "owner chain search: " + m.chainSearch
		}
	case "backspace":
		m.command = deleteLastRune(m.command)
	case "ctrl+u":
		m.command = ""
	case "ctrl+w":
		m.command = deleteLastWord(m.command)
	default:
		if len(msg.Runes) > 0 {
			m.command += string(msg.Runes)
		}
	}
	return m, nil
}

func deleteLastWord(value string) string {
	value = strings.TrimRight(value, " ")
	if idx := strings.LastIndexByte(value, ' '); idx >= 0 {
		return value[:idx+1]
	}
	return ""
}

func deleteLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}

func (m *model) jumpToViewMatch(direction int) bool {
	if m.viewSearch == "" {
		return false
	}
	obj, ok := m.selected()
	if !ok {
		return false
	}
	text := m.viewText(obj)
	lines := strings.Split(text, "\n")
	needle := strings.ToLower(m.viewSearch)
	start := m.viewScroll
	if direction < 0 {
		start--
	}
	for i := 0; i < len(lines); i++ {
		idx := (start + direction*i + len(lines)) % len(lines)
		if strings.Contains(strings.ToLower(lines[idx]), needle) {
			m.viewScroll = idx
			return true
		}
	}
	return false
}

func (m *model) jumpToStatusMatch(direction int) bool {
	if m.statusSearch == "" {
		return false
	}
	obj, ok := m.selected()
	if !ok {
		return false
	}
	lines := statusPanelLines(m.snapshot.Graph, obj)
	needle := strings.ToLower(m.statusSearch)
	start := m.statusScroll
	if direction < 0 {
		start--
	}
	for i := 0; i < len(lines); i++ {
		idx := (start + direction*i + len(lines)) % len(lines)
		if strings.Contains(strings.ToLower(lines[idx].text), needle) {
			m.statusScroll = idx
			return true
		}
	}
	return false
}

func (m model) summaryNavIndexes() []int {
	obj, ok := m.selected()
	if !ok {
		return nil
	}
	lines := filteredRelationLines(m.snapshot.Graph, obj, m.relationSearch)
	refs := summaryNavigableRefs(m.snapshot.Graph, obj.Ref.Namespace, lines)
	indexes := make([]int, 0, len(refs))
	for i := range refs {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	return indexes
}

func (m model) jumpToSummaryRef() (tea.Model, tea.Cmd) {
	obj, ok := m.selected()
	if !ok {
		return m, nil
	}
	lines := filteredRelationLines(m.snapshot.Graph, obj, m.relationSearch)
	refs := summaryNavigableRefs(m.snapshot.Graph, obj.Ref.Namespace, lines)
	navIndexes := m.summaryNavIndexes()
	if len(navIndexes) == 0 {
		return m, nil
	}
	cursor := m.summaryCursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(navIndexes) {
		cursor = len(navIndexes) - 1
	}
	ref, ok := refs[navIndexes[cursor]]
	if !ok {
		return m, nil
	}
	m.pushNavigation(obj)
	m.resourceKind = ref.Kind
	m.warningsOnly = false
	m.resourceFilter = ""
	m.list = m.resourceList()
	lw, lh := m.leftListSize()
	m.list.SetSize(lw, lh)
	for i, listItem := range m.list.Items() {
		if it, ok := listItem.(item); ok && it.obj.Ref.Key() == ref.Key() {
			m.list.Select(i)
			break
		}
	}
	m.mode = "yaml"
	m.relationSearch = ""
	m.summaryCursor = 0
	m.logLines = nil
	m.logErr = ""
	m.logScroll = 0
	m.status = "opened " + ref.Label()
	return m, m.loadSelectedLogs()
}

func (m model) chainNavIndexes() []int {
	_, ok := m.selected()
	if !ok {
		return nil
	}
	chain := m.filteredOwnerChain()
	indexes := make([]int, len(chain))
	for i := range chain {
		indexes[i] = i
	}
	return indexes
}

func (m model) filteredOwnerChain() []graph.Object {
	obj, ok := m.selected()
	if !ok {
		return nil
	}
	chain := ownerChain(m.snapshot.Graph, obj)
	needle := strings.ToLower(strings.TrimSpace(m.chainSearch))
	if needle == "" {
		return chain
	}
	filtered := make([]graph.Object, 0, len(chain))
	for _, entry := range chain {
		if strings.Contains(strings.ToLower(chainDisplayLabel(entry, entry.Ref.Key() == obj.Ref.Key())), needle) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (m model) jumpToChainRef() (tea.Model, tea.Cmd) {
	obj, ok := m.selected()
	if !ok {
		return m, nil
	}
	chain := m.filteredOwnerChain()
	if len(chain) == 0 {
		return m, nil
	}
	cursor := m.chainCursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(chain) {
		cursor = len(chain) - 1
	}
	m.pushNavigation(obj)
	ref := chain[cursor].Ref
	m.resourceKind = ref.Kind
	m.warningsOnly = false
	m.resourceFilter = ""
	m.list = m.resourceList()
	lw, lh := m.leftListSize()
	m.list.SetSize(lw, lh)
	for i, listItem := range m.list.Items() {
		if it, ok := listItem.(item); ok && it.obj.Ref.Key() == ref.Key() {
			m.list.Select(i)
			break
		}
	}
	m.mode = "yaml"
	m.chainCursor = 0
	m.logLines = nil
	m.logErr = ""
	m.logScroll = 0
	m.status = "opened " + ref.Label()
	return m, m.loadSelectedLogs()
}

func (m *model) pushNavigation(obj graph.Object) {
	if obj.Ref.Key() == "" {
		return
	}
	// Avoid growing the stack when a view is reopened for the same object.
	if len(m.navStack) > 0 && m.navStack[len(m.navStack)-1].key == obj.Ref.Key() {
		return
	}
	m.navStack = append(m.navStack, navigationState{
		kind:           m.resourceKind,
		key:            obj.Ref.Key(),
		warningsOnly:   m.warningsOnly,
		resourceFilter: m.resourceFilter,
	})
}

func (m *model) restoreNavigation() {
	last := m.navStack[len(m.navStack)-1]
	m.navStack = m.navStack[:len(m.navStack)-1]
	m.resourceKind = last.kind
	m.warningsOnly = last.warningsOnly
	m.resourceFilter = last.resourceFilter
	m.list = m.resourceList()
	lw, lh := m.leftListSize()
	m.list.SetSize(lw, lh)
	for i, listItem := range m.list.Items() {
		if it, ok := listItem.(item); ok && it.obj.Ref.Key() == last.key {
			m.list.Select(i)
			break
		}
	}
	m.mode = "relations"
	m.active = paneRelations
	m.summaryCursor = 0
	m.chainCursor = 0
	m.viewScroll = 0
	m.viewSearch = ""
	m.logLines = nil
	m.logErr = ""
	m.logScroll = 0
}

// ownerChain walks metadata.ownerReferences generically (via the graph's
// "Owns" edges, built once in graph.Build for every object regardless of
// kind) from obj up to its topmost loaded owner, top-down: [topmost owner,
// ..., obj]. Typical results: Pod -> ReplicaSet -> Deployment, or
// Pod -> Job -> CronJob.
//
// When the topmost owner carries OLM's "olm.owner"/"olm.owner.namespace"
// annotations (set by OLM on the Deployments it manages) and that owner
// (usually a ClusterServiceVersion) happens to be loaded in the graph, the
// walk continues upward through it too — surfacing
// Subscription -> InstallPlan -> CSV -> Deployment -> ... -> Pod. OLM kinds
// aren't fetched by internal/kube/snapshot.go today, so on most clusters
// this simply finds nothing past the workload chain and degrades to the
// plain owner chain.
func ownerChain(g *graph.Graph, obj graph.Object) []graph.Object {
	chain := []graph.Object{obj}
	seen := map[string]bool{obj.Ref.Key(): true}
	current := obj
	for i := 0; i < 12; i++ {
		owner, ok := findOwner(g, current.Ref)
		if !ok || seen[owner.Ref.Key()] {
			break
		}
		seen[owner.Ref.Key()] = true
		chain = append(chain, owner)
		current = owner
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	if csv, ok := olmOwnerFromAnnotations(g, chain[0]); ok && !seen[csv.Ref.Key()] {
		chain = append([]graph.Object{csv}, chain...)
		seen[csv.Ref.Key()] = true
		current = csv
		for i := 0; i < 12; i++ {
			owner, ok := findOwner(g, current.Ref)
			if !ok || seen[owner.Ref.Key()] {
				break
			}
			seen[owner.Ref.Key()] = true
			chain = append([]graph.Object{owner}, chain...)
			current = owner
		}
	}

	// Continue upward through OLM's Subscription/InstallPlan/CSV edges
	// (internal/graph/build.go's appendSubscriptionEdges/
	// appendInstallPlanEdges) when the topmost object reached above is a
	// CSV or InstallPlan — whether it got there via a real
	// metadata.ownerReferences (OLM sets one on the workloads it deploys,
	// pointing at the CSV) or the annotation fallback just above. No-op for
	// every other kind, and for clusters without OLM's CRDs loaded.
	current = chain[0]
	for i := 0; i < 12; i++ {
		owner, ok := findOLMOwner(g, current.Ref)
		if !ok || seen[owner.Ref.Key()] {
			break
		}
		seen[owner.Ref.Key()] = true
		chain = append([]graph.Object{owner}, chain...)
		current = owner
	}

	// Finally, if the topmost object reached above carries ArgoCD's
	// argocd.argoproj.io/instance label (checked via the same "Manages"
	// edge internal/graph/build.go's appendArgoApplicationEdges builds) and
	// that Application is loaded, prepend it too. This is independent of
	// the OLM walk above — a chain can end in either an Application, a
	// Subscription, or nothing further, never both an Application and an
	// OLM ancestor at once in practice, but the two blocks don't assume
	// that; each just no-ops if its edge type isn't present.
	if app, ok := findArgoApplication(g, chain[0].Ref); ok && !seen[app.Ref.Key()] {
		chain = append([]graph.Object{app}, chain...)
	}
	return chain
}

// findArgoApplication looks up the ArgoCD Application managing ref via the
// graph's "Manages" edge (built from the argocd.argoproj.io/instance
// label), when that Application object is loaded in the snapshot.
func findArgoApplication(g *graph.Graph, ref graph.ObjectRef) (graph.Object, bool) {
	for _, edge := range g.EdgesFor(ref) {
		if edge.Type == "Manages" && edge.To.Key() == ref.Key() {
			return g.ObjectByKey(edge.From.Key())
		}
	}
	return graph.Object{}, false
}

func findOwner(g *graph.Graph, ref graph.ObjectRef) (graph.Object, bool) {
	for _, edge := range g.EdgesFor(ref) {
		if edge.Type == "Owns" && edge.To.Key() == ref.Key() {
			return g.ObjectByKey(edge.From.Key())
		}
	}
	return graph.Object{}, false
}

// findOLMOwner walks one hop up the OLM chain: InstallPlan/Subscription ->
// CSV (edge type "Installs") or Subscription -> InstallPlan (edge type
// "Resolves"). When a CSV has both an InstallPlan and a Subscription
// "Installs" edge pointing at it, InstallPlan is the closer hop and wins —
// the walk reaches Subscription on its next iteration, from the InstallPlan.
func findOLMOwner(g *graph.Graph, ref graph.ObjectRef) (graph.Object, bool) {
	var installPlanOwner, subscriptionOwner *graph.Object
	for _, edge := range g.EdgesFor(ref) {
		if edge.Type != "Installs" || edge.To.Key() != ref.Key() {
			continue
		}
		owner, ok := g.ObjectByKey(edge.From.Key())
		if !ok {
			continue
		}
		switch edge.From.Kind {
		case "InstallPlan":
			installPlanOwner = &owner
		case "Subscription":
			subscriptionOwner = &owner
		}
	}
	if installPlanOwner != nil {
		return *installPlanOwner, true
	}
	if subscriptionOwner != nil {
		return *subscriptionOwner, true
	}
	for _, edge := range g.EdgesFor(ref) {
		if edge.Type == "Resolves" && edge.To.Key() == ref.Key() {
			return g.ObjectByKey(edge.From.Key())
		}
	}
	return graph.Object{}, false
}

// olmOwnerFromAnnotations looks up the ClusterServiceVersion (or whatever
// olm.owner.kind names) referenced by OLM's olm.owner annotations, when
// that object happens to be present in the loaded graph.
func olmOwnerFromAnnotations(g *graph.Graph, obj graph.Object) (graph.Object, bool) {
	annotations := obj.Raw.GetAnnotations()
	name := annotations["olm.owner"]
	if name == "" {
		return graph.Object{}, false
	}
	namespace := annotations["olm.owner.namespace"]
	if namespace == "" {
		namespace = obj.Ref.Namespace
	}
	kind := annotations["olm.owner.kind"]
	if kind == "" {
		kind = "ClusterServiceVersion"
	}
	return g.ObjectByKey(graph.ObjectRef{Kind: kind, Namespace: namespace, Name: name}.Key())
}

// gitopsLine reports the first GitOps/CD management marker found walking
// the chain top-down (owners usually carry it, e.g. a Deployment's
// argocd.argoproj.io/instance label — Pods rarely do).
func gitopsLine(chain []graph.Object) string {
	for _, obj := range chain {
		if line, ok := gitopsManagedByLine(obj); ok {
			return line
		}
	}
	return ""
}

func gitopsManagedByLine(obj graph.Object) (string, bool) {
	labels := obj.Raw.GetLabels()
	annotations := obj.Raw.GetAnnotations()
	if app := labels["argocd.argoproj.io/instance"]; app != "" {
		return fmt.Sprintf("managed-by: argocd (app:%s)", app), true
	}
	if trackingID := annotations["argocd.argoproj.io/tracking-id"]; trackingID != "" {
		if app, _, _ := strings.Cut(trackingID, ":"); app != "" {
			return fmt.Sprintf("managed-by: argocd (app:%s)", app), true
		}
	}
	if name := labels["kustomize.toolkit.fluxcd.io/name"]; name != "" {
		if ns := labels["kustomize.toolkit.fluxcd.io/namespace"]; ns != "" {
			return fmt.Sprintf("managed-by: flux (kustomization:%s/%s)", ns, name), true
		}
		return fmt.Sprintf("managed-by: flux (kustomization:%s)", name), true
	}
	if release := labels["app.kubernetes.io/instance"]; release != "" {
		if labels["helm.sh/chart"] != "" || annotations["meta.helm.sh/release-name"] != "" {
			return fmt.Sprintf("managed-by: helm (release:%s)", release), true
		}
	}
	if mb := labels["app.kubernetes.io/managed-by"]; mb != "" {
		return "managed-by: " + strings.ToLower(mb), true
	}
	return "", false
}

// chainPanelBody renders the owner chain top-down with the currently
// navigable cursor highlighted, same interaction pattern as the Relations
// pane (j/k, enter to jump).
func (m model) chainPanelBody(g *graph.Graph, obj graph.Object, width, maxLines int) string {
	chain := ownerChain(g, obj)
	needle := strings.ToLower(strings.TrimSpace(m.chainSearch))
	if needle != "" {
		filtered := make([]graph.Object, 0, len(chain))
		for _, entry := range chain {
			if strings.Contains(strings.ToLower(chainDisplayLabel(entry, entry.Ref.Key() == obj.Ref.Key())), needle) {
				filtered = append(filtered, entry)
			}
		}
		chain = filtered
	}
	if len(chain) == 0 {
		return "no owner chain match: " + m.chainSearch
	}
	maxLines = max(1, maxLines)
	all := make([]string, 0, len(chain)+1)
	selected := make([]bool, 0, len(chain)+1)
	if mb := gitopsLine(chain); mb != "" {
		all = append(all, truncateText(mb, width-2))
		selected = append(selected, false)
	}
	cursorIndex := -1
	if len(chain) > 0 {
		idx := m.chainCursor
		if idx < 0 {
			idx = 0
		}
		if idx >= len(chain) {
			idx = len(chain) - 1
		}
		cursorIndex = idx
	}
	for i, o := range chain {
		text := truncateText(chainDisplayLabel(o, o.Ref.Key() == obj.Ref.Key()), width-2)
		all = append(all, text)
		selected = append(selected, i == cursorIndex)
	}
	if len(all) == 0 {
		return "no owner chain"
	}
	cursorLine := -1
	for i, isSelected := range selected {
		if isSelected {
			cursorLine = i
			break
		}
	}
	start := 0
	if cursorLine >= maxLines {
		start = cursorLine - maxLines + 1
	}
	if start > max(0, len(all)-maxLines) {
		start = max(0, len(all)-maxLines)
	}
	end := minInt(len(all), start+maxLines)
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		if selected[i] {
			out = append(out, "> "+summaryRefSelectedStyle.Render(all[i]))
		} else if i == 0 && gitopsLine(chain) != "" {
			out = append(out, "  "+all[i])
		} else {
			out = append(out, "  "+summaryRefStyle.Render(all[i]))
		}
	}
	return strings.Join(out, "\n")
}

func chainDisplayLabel(obj graph.Object, current bool) string {
	kind := chainKindLabel(obj.Ref.Kind)
	extra := ""
	switch obj.Ref.Kind {
	case "Subscription":
		if channel, _, _ := unstructuredNestedString(obj, "spec", "channel"); channel != "" {
			extra = " (channel:" + channel + ")"
		}
	case "InstallPlan":
		if phase, _, _ := unstructuredNestedString(obj, "status", "phase"); phase != "" {
			extra = " (" + phase + ")"
		}
	case "Application":
		sync, _, _ := unstructuredNestedString(obj, "status", "sync", "status")
		health, _, _ := unstructuredNestedString(obj, "status", "health", "status")
		if sync != "" || health != "" {
			extra = fmt.Sprintf(" (sync:%s health:%s)", orDash(sync), orDash(health))
		}
	}
	line := kind + ": " + obj.Ref.Name + extra
	if current {
		line += " (this)"
	}
	return line
}

func chainKindLabel(kind string) string {
	switch kind {
	case "ClusterServiceVersion":
		return "csv"
	case "Subscription":
		return "subscription"
	case "InstallPlan":
		return "installplan"
	case "Application":
		return "application"
	default:
		return summaryKind(kind)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (m *model) jumpToLogMatch(direction int) {
	if m.logSearch == "" || len(m.logLines) == 0 {
		return
	}
	needle := strings.ToLower(m.logSearch)
	n := len(m.logLines)
	current := n - 1 - m.logScroll
	for step := 1; step <= n; step++ {
		idx := current + direction*step
		if idx < 0 || idx >= n {
			continue
		}
		if strings.Contains(strings.ToLower(m.logLines[idx]), needle) {
			m.logScroll = n - 1 - idx
			m.status = fmt.Sprintf("log search: %s (line %d/%d)", m.logSearch, idx+1, n)
			return
		}
	}
	m.status = "log search: no match for " + m.logSearch
}

func (m model) runCommand(command string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return m, nil
	}
	m.commandHistory = append(m.commandHistory, command)
	if len(m.commandHistory) > 50 {
		m.commandHistory = m.commandHistory[len(m.commandHistory)-50:]
	}
	m.historyCursor = len(m.commandHistory)
	opts := m.snapshot.Options
	switch fields[0] {
	case "q", "quit":
		return m, tea.Quit
	case "ctx", "context":
		m.contextPicker = false
		if len(fields) == 1 {
			if len(m.snapshot.Contexts) == 0 {
				m.status = "no contexts loaded"
				return m, nil
			}
			m.contextPicker = true
			m.namespacePicker = false
			m.active = paneResources
			m.list = newContextList(m.snapshot)
			lw, lh := m.leftListSize()
			m.list.SetSize(lw, lh)
			m.status = "select context with enter, esc returns"
			return m, nil
		}
		opts.ContextName = fields[1]
		opts.Namespace = ""
		m.resourceFilter = ""
	case "ns", "namespace":
		m.contextPicker = false
		m.resourceFilter = ""
		if len(fields) == 1 {
			if len(m.snapshot.Namespaces) == 0 {
				m.status = "no namespaces loaded"
				return m, nil
			}
			m.namespacePicker = true
			m.list = newNamespaceList(m.snapshot)
			lw, lh := m.leftListSize()
			m.list.SetSize(lw, lh)
			m.status = "select namespace with enter, esc returns"
			return m, nil
		}
		if len(fields) != 2 {
			m.status = "usage: :ns [namespace]"
			return m, nil
		}
		if strings.EqualFold(fields[1], "all") || fields[1] == "*" {
			opts.Namespace = ""
			opts.AllNamespaces = true
			opts.ResourceKind = m.resourceKind
		} else {
			opts.Namespace = fields[1]
			opts.AllNamespaces = false
		}
	case "kc", "kubeconfig":
		if len(fields) != 2 {
			m.status = "usage: :kubeconfig <path>"
			return m, nil
		}
		opts.Kubeconfig = fields[1]
		opts.ContextName = ""
		opts.Namespace = ""
	case "refresh", "reload":
	case "watch", "autorefresh":
		m.autoRefresh = true
		m.status = "live refresh on (3s)"
		return m, scheduleRefresh()
	case "nowatch", "pause-refresh", "noautorefresh":
		m.autoRefresh = false
		m.status = "live refresh off"
		return m, nil
	case "help", "?":
		m.mode = "help"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		m.status = "help"
		return m, nil
	case "aliases", "alias":
		m.mode = "aliases"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		m.status = "resource aliases"
		return m, nil
	case "yaml", "y":
		m.mode = "yaml"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
	case "describe", "details", "d":
		m.mode = "details"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
	case "events":
		m.mode = "events"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
	case "problems", "problem":
		m.mode = "problems"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
	case "impact", "blast-radius", "blast":
		m.mode = "impact"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
	case "usedby", "used-by", "consumers":
		m.mode = "usedby"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
	case "xray", "x-ray":
		m.mode = "impact"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
	case "loaderrors", "warnings":
		m.mode = "loaderrors"
		m.active = paneRelations
		m.viewScroll = 0
		m.viewSearch = ""
		return m, nil
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
			m.status = "usage: :sort age|new|old|status|namespace|default"
			return m, nil
		}
		mode, ok := parseSortMode(fields[1])
		if !ok {
			m.status = "usage: :sort age|new|old|status|namespace|default"
			return m, nil
		}
		m.sortMode = mode
		m.list = m.resourceList()
		lw, lh := m.leftListSize()
		m.list.SetSize(lw, lh)
		if m.sortMode == "" {
			m.status = "sort: default"
		} else {
			m.status = "sort: " + sortModeLabel(m.sortMode)
		}
		return m, m.loadSelectedLogs()
	default:
		kind, knownAlias := resourceAlias(fields[0])
		var discovered kube.ResourceType
		discoveredResource := false
		if !knownAlias {
			if resource, ok := m.snapshot.ResolveResource(fields[0]); ok {
				kind = resource.Kind
				knownAlias = true
				discovered = resource
				discoveredResource = true
			}
		}
		if knownAlias {
			m.previousResourceCommand = m.lastResourceCommand
			m.lastResourceCommand = strings.TrimSpace(command)
			if len(fields) > 2 && !(len(fields) == 3 && fields[1] == "-l") {
				m.status = "usage: :" + fields[0] + " [namespace|@context|/filter]"
				return m, nil
			}
			m.namespacePicker = false
			m.contextPicker = false
			m.navStack = nil
			m.resourceKind = kind
			m.warningsOnly = kind == "Event"
			m.resourceFilter = ""
			loaded := m.snapshotHasKind(kind)
			request := kind
			if discoveredResource {
				loaded = m.snapshot.LoadedResources[discovered.Key()]
				request = discovered.CommandName()
			}
			targetNamespace := opts.Namespace
			targetAllNamespaces := opts.AllNamespaces
			targetContext := opts.ContextName
			if len(fields) >= 2 {
				argument := fields[1]
				if len(fields) == 3 && fields[1] == "-l" {
					argument = "-l " + fields[2]
				}
				switch {
				case strings.HasPrefix(argument, "/"):
					m.resourceFilter = strings.ReplaceAll(strings.TrimPrefix(argument, "/"), ",", " ")
				case strings.HasPrefix(argument, "-l "):
					m.resourceFilter = "-l " + strings.TrimSpace(strings.TrimPrefix(argument, "-l "))
				case strings.Contains(argument, "="):
					m.resourceFilter = "-l " + argument
				case strings.EqualFold(argument, "all"), argument == "*":
					targetNamespace = ""
					targetAllNamespaces = true
				case strings.HasPrefix(argument, "@"):
					targetContext = strings.TrimPrefix(argument, "@")
					targetNamespace = ""
					targetAllNamespaces = false
				default:
					targetNamespace = argument
					targetAllNamespaces = false
				}
			}
			opts.Namespace = targetNamespace
			opts.AllNamespaces = targetAllNamespaces
			opts.ContextName = targetContext
			opts.ResourceKind = request
			targetDiffers := opts.Namespace != m.snapshot.Options.Namespace || opts.AllNamespaces != m.snapshot.Options.AllNamespaces || opts.ContextName != m.snapshot.Options.ContextName
			if !loaded || targetDiffers {
				m.loading = true
				m.status = "loading " + kind + "..."
				return m, reload(opts)
			}
			m.list = m.resourceList()
			lw, lh := m.leftListSize()
			m.list.SetSize(lw, lh)
			m.status = fmt.Sprintf("showing %s", kind)
			return m, m.loadSelectedLogs()
		}
		if fields[0] == "all" {
			m.previousResourceCommand = m.lastResourceCommand
			m.lastResourceCommand = strings.TrimSpace(command)
			m.namespacePicker = false
			m.contextPicker = false
			m.navStack = nil
			m.resourceKind = ""
			m.resourceFilter = ""
			m.warningsOnly = false
			m.list = m.resourceList()
			lw, lh := m.leftListSize()
			m.list.SetSize(lw, lh)
			m.status = "showing all loaded resources"
			return m, m.loadSelectedLogs()
		}
		if suggestions := m.resourceSuggestions(fields[0]); len(suggestions) > 0 {
			m.status = "ambiguous resource; use: " + strings.Join(suggestions[:minInt(6, len(suggestions))], ", ")
		} else {
			m.status = "unknown command: " + fields[0]
		}
		return m, nil
	}
	m.loading = true
	m.status = "loading..."
	return m, reload(opts)
}

func (m model) runHistoryCommand(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(m.commandHistory) {
		return m, nil
	}
	command := m.commandHistory[index]
	historyLen := len(m.commandHistory)
	updated, cmd := m.runCommand(command)
	got := updated.(model)
	// Replaying a command must not grow history forever or change the
	// meaning of the next [ / ] press.
	if len(got.commandHistory) > historyLen {
		got.commandHistory = got.commandHistory[:historyLen]
	}
	got.historyCursor = index
	return got, cmd
}

func (m model) snapshotHasKind(kind string) bool {
	return m.snapshot.LoadedKinds[kind]
}

func (m model) selectedKey() string {
	obj, ok := m.selected()
	if !ok {
		return ""
	}
	return obj.Ref.Key()
}

func (m *model) selectResourceKey(key string) {
	if key == "" {
		return
	}
	for i, listItem := range m.list.Items() {
		if it, ok := listItem.(item); ok && it.obj.Ref.Key() == key {
			m.list.Select(i)
			return
		}
	}
}

func (m *model) toggleSelectedMark() {
	key := m.selectedKey()
	if key == "" {
		return
	}
	if m.marks == nil {
		m.marks = map[string]bool{}
	}
	if m.marks[key] {
		delete(m.marks, key)
		m.status = "unmarked: " + key
	} else {
		m.marks[key] = true
		m.status = "marked: " + key
	}
	m.markAnchor = key
	m.list = m.resourceList()
	lw, lh := m.leftListSize()
	m.list.SetSize(lw, lh)
	m.selectResourceKey(key)
}

func (m *model) markSelectedRange() {
	items := m.list.Items()
	if len(items) == 0 {
		return
	}
	current := m.list.Index()
	if current < 0 || current >= len(items) {
		return
	}
	anchor := current
	if m.markAnchor != "" {
		for i, listItem := range items {
			if it, ok := listItem.(item); ok && it.obj.Ref.Key() == m.markAnchor {
				anchor = i
				break
			}
		}
	}
	if m.marks == nil {
		m.marks = map[string]bool{}
	}
	start, end := anchor, current
	if start > end {
		start, end = end, start
	}
	for i := start; i <= end; i++ {
		if it, ok := items[i].(item); ok {
			m.marks[it.obj.Ref.Key()] = true
		}
	}
	m.markAnchor = m.selectedKey()
	m.list = m.resourceList()
	lw, lh := m.leftListSize()
	m.list.SetSize(lw, lh)
	m.selectResourceKey(m.markAnchor)
	m.status = fmt.Sprintf("marked %d resource(s)", len(m.marks))
}

func (m model) loadSelectedLogs() tea.Cmd {
	obj, ok := m.selected()
	if !ok || m.pickerActive() || !m.logsFullscreen {
		return nil
	}
	id := m.currentLogRequestID()
	pods := m.logPods(obj)
	tailLines := int64(160)
	if m.logRequestID == id && len(m.logLines) > 0 {
		tailLines = 0
	}
	return func() tea.Msg {
		stream := kube.StreamLogs(context.Background(), kube.LogRequest{
			Options:    m.snapshot.Options,
			Namespace:  obj.Ref.Namespace,
			Pods:       pods,
			TailLines:  tailLines,
			Grep:       m.logGrep,
			Previous:   m.logPrevious,
			Timestamps: m.logTimestamps,
			Since:      m.logSince,
		})
		return logStreamStarted{id: id, events: stream.Events, cancel: stream.Cancel}
	}
}

func (m model) currentLogRequestID() string {
	return fmt.Sprintf("%s|prev=%t|ts=%t|grep=%s|since=%s", m.selectedKey(), m.logPrevious, m.logTimestamps, m.logGrep, m.logSince)
}

func waitForLogEvent(id string, events <-chan kube.LogEvent) tea.Cmd {
	return func() tea.Msg {
		event, open := <-events
		return logEventMsg{id: id, event: event, open: open}
	}
}

func scheduleLogReconnect(id string) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return logReconnect{id: id}
	})
}

func reload(opts kube.Options) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := kube.LoadSnapshot(context.Background(), opts)
		return reloadResult{snapshot: snapshot, err: err}
	}
}

func reloadInBackground(opts kube.Options) tea.Cmd {
	return func() tea.Msg {
		snapshot, err := kube.LoadSnapshot(context.Background(), opts)
		return reloadResult{snapshot: snapshot, err: err, background: true}
	}
}

func scheduleRefresh() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return refreshTickMsg{}
	})
}

func scheduleRefreshIfEnabled(enabled bool) tea.Cmd {
	if !enabled {
		return nil
	}
	return scheduleRefresh()
}

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	if m.logsFullscreen {
		return m.logsFullscreenView()
	}
	if m.detailFullscreen {
		return m.detailFullscreenView()
	}
	if m.compactLayout() {
		return m.compactView()
	}
	_, listHeight := m.leftListSize()
	leftWidth := leftPaneOuterWidth(m.width)
	rightWidth := max(40, m.width-leftWidth-paneGap)
	mainHeight := max(8, m.height-1)
	_, leftBottomHeight := m.leftLayoutHeights()

	// Usage is a fixed small strip (2 gauge lines + title + border).
	usageHeight := 5
	rightRemaining := max(6, mainHeight-usageHeight)
	// Relations gets cut down ~30% versus an even split so Status (the
	// more information-dense pane) gets the extra room.
	relationsLines := 1
	if obj, ok := m.selected(); ok {
		relationsLines = len(filteredRelationLines(m.snapshot.Graph, obj, m.relationSearch))
	}
	relationsHeight := minInt(max(5, relationsLines+3), max(5, rightRemaining*45/100))
	statusHeight := max(6, rightRemaining-relationsHeight)

	// bubbles list.View() doesn't always respect SetSize exactly (its own
	// pagination/help accounting can be off by a line); clip defensively so
	// it never pushes the rest of the layout below the terminal.
	listLines := strings.Split(m.list.View(), "\n")
	if len(listLines) > listHeight {
		listLines = listLines[:listHeight]
	}
	leftTop := paneStyle(m.active == paneResources).Width(max(1, leftWidth-paneBoxOverhead)).Height(listHeight).Render(strings.Join(listLines, "\n"))
	leftBottom := m.chainPaneView(leftWidth, leftBottomHeight)
	usage := m.usagePaneView(rightWidth, usageHeight)
	rightTop := m.relationsPaneView(rightWidth, relationsHeight)
	rightBottom := m.statusPaneView(rightWidth, statusHeight)

	leftCol := lipgloss.JoinVertical(lipgloss.Left, leftTop, leftBottom)
	rightCol := lipgloss.JoinVertical(lipgloss.Left, usage, rightTop, rightBottom)
	footer := m.footer()

	full := lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, leftCol, " ", rightCol), footer)

	// Belt-and-suspenders: on very small terminals the per-pane minimum
	// heights can add up to more than mainHeight. Never let the composed
	// frame exceed the terminal, or the top of the layout scrolls out of
	// view (list header, Usage panel) with no way back short of resizing.
	if lines := strings.Split(full, "\n"); len(lines) > m.height {
		full = strings.Join(lines[:m.height], "\n")
	}
	return full
}

func (m model) compactView() string {
	mainHeight := max(1, m.height-1)
	width := max(1, m.width)
	var body string
	switch m.active {
	case paneResources:
		_, listHeight := m.leftListSize()
		lines := strings.Split(m.list.View(), "\n")
		if len(lines) > listHeight {
			lines = lines[:listHeight]
		}
		body = paneStyle(true).Width(max(1, width-paneBoxOverhead)).Height(max(1, mainHeight-2)).Render(strings.Join(lines, "\n"))
	case paneChain:
		body = m.chainPaneView(width, mainHeight)
	case paneRelations:
		body = m.relationsPaneView(width, mainHeight)
	case paneStatus:
		body = m.compactStatusPaneView(width, mainHeight)
	}
	full := body + "\n" + m.footer()
	if lines := strings.Split(full, "\n"); len(lines) > m.height {
		full = strings.Join(lines[:max(1, m.height)], "\n")
	}
	return full
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
	case "dp", "deploy", "deployment", "deployments":
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
	case "sec", "secret", "secrets":
		return "Secret", true
	case "pvc", "persistentvolumeclaim", "persistentvolumeclaims":
		return "PersistentVolumeClaim", true
	case "pv", "pvs", "persistentvolume", "persistentvolumes":
		return "PersistentVolume", true
	case "sc", "storageclass", "storageclasses":
		return "StorageClass", true
	case "sa", "serviceaccount", "serviceaccounts":
		return "ServiceAccount", true
	case "role", "roles":
		return "Role", true
	case "rb", "rolebinding", "rolebindings":
		return "RoleBinding", true
	case "cr", "clusterrole", "clusterroles":
		return "ClusterRole", true
	case "crb", "clusterrolebinding", "clusterrolebindings":
		return "ClusterRoleBinding", true
	case "ing", "ingress", "ingresses":
		return "Ingress", true
	case "route", "routes":
		return "Route", true
	case "gw", "gateway", "gateways":
		return "Gateway", true
	case "httproute", "httproutes":
		return "HTTPRoute", true
	case "endpointslice", "endpointslices", "ep", "eps":
		return "EndpointSlice", true
	case "app", "application", "applications":
		return "Application", true
	case "sub", "subscription", "subscriptions":
		return "Subscription", true
	case "csv", "clusterserviceversion", "clusterserviceversions":
		return "ClusterServiceVersion", true
	case "ip", "installplan", "installplans":
		return "InstallPlan", true
	case "es", "externalsecret", "externalsecrets":
		return "ExternalSecret", true
	case "sm", "servicemonitor", "servicemonitors":
		return "ServiceMonitor", true
	case "pm", "podmonitor", "podmonitors":
		return "PodMonitor", true
	case "vs", "volumesnapshot", "volumesnapshots":
		return "VolumeSnapshot", true
	case "so", "scaledobject", "scaledobjects":
		return "ScaledObject", true
	}
	return "", false
}

// statusPaneView is the smaller bottom-left quadrant: health, why-it's-failing
// (problems), recent events, and environment values for the selected object.
func (m model) statusPaneView(width, height int) string {
	if m.loading {
		return paneTitle("Status", "Loading cluster snapshot...", width, height, false)
	}
	if m.pickerActive() {
		prompt := "Select a namespace in the list, enter to switch."
		if m.contextPicker {
			prompt = "Select a context in the list, enter to switch."
		}
		return paneTitle("Status", prompt, width, height, m.active == paneStatus)
	}
	obj, ok := m.selected()
	if !ok {
		return paneTitle("Status", "No resources loaded.", width, height, m.active == paneStatus)
	}
	title := "Status"
	if count := len(m.snapshot.LoadErrors); count > 0 {
		title = fmt.Sprintf("Status (! %d load warnings)", count)
	}
	if m.active == paneStatus {
		if len(m.snapshot.LoadErrors) > 0 {
			title = fmt.Sprintf("Status (! %d warnings; j/k scroll, Ctrl-F find)", len(m.snapshot.LoadErrors))
		} else {
			title = "Status (j/k PgUp/PgDn Ctrl-U/Ctrl-D Home/End, Ctrl-F find, G top)"
		}
	}
	body := statusPanelBody(m.snapshot.Graph, obj, width-4, height-3, m.statusScroll, m.statusSearch)
	return paneTitleRaw(title, body, width, height, m.active == paneStatus)
}

// relationsPaneView is the top-right quadrant. By default it lists the
// object's relations (Services, ConfigMaps, Secrets, ServiceAccounts, PVCs,
// ...) as a clickable list: j/k to move, enter to open the referenced
// object's values in this same pane. r/d/y/e/p switch it to relations,
// details, YAML, events, or problems.
func (m model) relationsPaneView(width, height int) string {
	if m.loading {
		return paneStyle(false).Width(max(1, width-paneBoxOverhead)).Height(max(1, height-2)).Render("Loading cluster snapshot...")
	}
	if m.pickerActive() {
		return m.namespaceHelp(width, height)
	}
	if m.mode == "aliases" {
		return m.scrollableModeView("Resource Aliases", aliasesView(m.snapshot.Resources), width, height)
	}
	obj, ok := m.selected()
	if !ok {
		return paneStyle(false).Width(max(1, width-paneBoxOverhead)).Height(max(1, height-2)).Render("No resources loaded.")
	}
	if m.mode == "relations" || m.mode == "" {
		title := "Relations"
		if m.active == paneRelations {
			title = "Relations (j/k select, enter open, esc back)"
		}
		if m.relationSearch != "" {
			title += "  /" + m.relationSearch
		}
		body := m.relationsPanelBody(m.snapshot.Graph, obj, width-4, height-3)
		return paneTitleRaw(title, body, width, height, m.active == paneRelations)
	}
	var body, title string
	switch m.mode {
	case "yaml":
		body = obj.YAML()
		title = "Yaml"
	case "events":
		body = eventsView(obj)
		title = "Events"
	case "details":
		body = detailsView(m.snapshot.Graph, obj)
		title = "Details"
	case "problems":
		body = problemsView(m.snapshot.Graph, obj)
		title = "Problems"
	case "impact":
		body = impactView(m.snapshot.Graph, obj)
		title = "Impact / Blast Radius"
	case "usedby":
		body = usedByView(m.snapshot.Graph, obj)
		title = "Used By / Consumers"
	case "loaderrors":
		body = loadErrorsView(m.snapshot.LoadErrors)
		title = "Snapshot Load Warnings"
	case "help":
		body = helpView()
		title = "Help"
	case "aliases":
		body = aliasesView(m.snapshot.Resources)
		title = "Resource Aliases"
	}
	return m.scrollableModeView(title, body, width, height)
}

func (m model) viewText(obj graph.Object) string {
	switch m.mode {
	case "yaml":
		return obj.YAML()
	case "events":
		return eventsView(obj)
	case "details":
		return detailsView(m.snapshot.Graph, obj)
	case "problems":
		return problemsView(m.snapshot.Graph, obj)
	case "impact":
		return impactView(m.snapshot.Graph, obj)
	case "usedby":
		return usedByView(m.snapshot.Graph, obj)
	case "loaderrors":
		return loadErrorsView(m.snapshot.LoadErrors)
	case "help":
		return helpView()
	case "aliases":
		return aliasesView(m.snapshot.Resources)
	default:
		return ""
	}
}

// scrollableModeView is deliberately line based. Kubernetes YAML and event
// output are already structured as lines, so preserving every line gives a
// much more useful terminal viewer than truncating the top of a document.
// Long lines are clipped at the pane edge while the vertical cursor remains
// stable across terminal resizes.
func (m model) scrollableModeView(title, body string, width, height int) string {
	bodyWidth := max(8, width-4)
	maxLines := max(1, height-3)
	all := strings.Split(body, "\n")
	if len(all) == 0 {
		all = []string{""}
	}
	scroll := minInt(max(0, m.viewScroll), max(0, len(all)-1))
	visible := all[scroll:]
	lines := make([]string, 0, minInt(maxLines, len(visible)))
	for _, line := range visible {
		if len(lines) >= maxLines {
			break
		}
		line = truncateText(line, bodyWidth)
		if m.viewSearch != "" {
			line = highlightLogLine(line, m.viewSearch)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	if scroll > 0 || scroll+len(lines) < len(all) {
		title += fmt.Sprintf("  [line %d/%d]", scroll+1, len(all))
	}
	if m.active == paneRelations {
		title += "  (j/k scroll  Ctrl-F find)"
	}
	return paneTitleRaw(title, strings.Join(lines, "\n"), width, height, m.active == paneRelations)
}

func (m model) compactStatusPaneView(width, height int) string {
	if m.loading || m.pickerActive() {
		return m.statusPaneView(width, height)
	}
	obj, ok := m.selected()
	if !ok {
		return paneTitleRaw("Status + Usage", "No resources loaded.", width, height, true)
	}
	usage := usageSummary(m.snapshot.PodMetrics, relatedPods(m.snapshot.Graph, obj), width-4, 2)
	statusLines := statusPanelBody(m.snapshot.Graph, obj, width-4, max(1, height-6), m.statusScroll, m.statusSearch)
	body := usage + "\n" + statusLines
	return paneTitleRaw("Status + Usage (j/k PgUp/PgDn Ctrl-U/Ctrl-D Home/End, G top)", body, width, height, true)
}

// logsFullscreenView takes over the whole terminal (k9s-style `l`) instead
// of fighting for room in a permanent quadrant. esc/l returns to the 4-pane
// layout; all log state (scroll, grep, search, wrap, ...) persists across
// toggles since it lives on the model, not this view.
func (m model) logsFullscreenView() string {
	title := "Logs: no resource selected"
	if obj, ok := m.selected(); ok {
		title = "Logs: " + obj.Ref.Label()
	}
	height := max(4, m.height-1)
	logs := m.logsView(max(8, m.width-4), height-3)
	footer := lipgloss.NewStyle().
		Width(max(1, m.width)).
		Foreground(lipgloss.Color("245")).
		Render(truncateText("esc back  G live  / search  n/N next  space pause  w wrap  P previous  t timestamps", max(1, m.width-1)))
	return paneTitleRaw(title, logs, m.width, height, true) + "\n" + footer
}

func (m model) detailFullscreenView() string {
	title := strings.Title(m.mode)
	if title == "" {
		title = "Resource"
	}
	if obj, ok := m.selected(); ok {
		title += ": " + obj.Ref.Label()
	}
	height := max(4, m.height-1)
	body := "No resource selected."
	if obj, ok := m.selected(); ok {
		body = m.viewText(obj)
	}
	content := m.scrollableModeView(title, body, m.width, height)
	footer := lipgloss.NewStyle().
		Width(max(1, m.width)).
		Foreground(lipgloss.Color("245")).
		Render(truncateText("f/esc split view  j/k scroll  PgUp/PgDn  G top  Ctrl-F find", max(1, m.width-1)))
	return content + "\n" + footer
}

// chainPaneView is the freed left-column-bottom quadrant: the object's
// owner chain (ReplicaSet -> Deployment, Job -> CronJob, ...), extended
// with the OLM Subscription/InstallPlan/CSV chain and GitOps management
// labels when present.
func (m model) chainPaneView(width, height int) string {
	if m.loading {
		return paneTitleRaw("Owner Chain", "Loading cluster snapshot...", width, height, false)
	}
	if m.pickerActive() {
		return paneTitleRaw("Owner Chain", "", width, height, false)
	}
	obj, ok := m.selected()
	if !ok {
		return paneTitleRaw("Owner Chain", "No resources loaded.", width, height, false)
	}
	title := "Owner Chain"
	if m.active == paneChain {
		title = "Owner Chain (j/k select, enter open, esc back)"
	}
	if m.chainSearch != "" {
		title += "  /" + m.chainSearch
	}
	body := m.chainPanelBody(m.snapshot.Graph, obj, width-4, height-3)
	return paneTitleRaw(title, body, width, height, m.active == paneChain)
}

// usagePaneView is the small strip above Relations: CPU/memory usage vs
// limits (gauges, from metrics-server when available) and requests/limits
// summed across the selected object's related pods.
func (m model) usagePaneView(width, height int) string {
	if m.loading || m.pickerActive() {
		return paneTitleRaw("Usage", "", width, height, false)
	}
	obj, ok := m.selected()
	if !ok {
		return paneTitleRaw("Usage", "", width, height, false)
	}
	pods := relatedPods(m.snapshot.Graph, obj)
	body := usageSummary(m.snapshot.PodMetrics, pods, width-4, height-3)
	return paneTitleRaw("Usage", body, width, height, false)
}

func usageSummary(podMetrics map[string]kube.PodMetric, pods []graph.Object, width, maxLines int) string {
	var reqCPU, limCPU, reqMem, limMem, useCPU, useMem int64
	haveUsage := false
	nodes := map[string]bool{}
	for _, pod := range pods {
		if node, _, _ := unstructuredNestedString(pod, "spec", "nodeName"); node != "" {
			nodes[node] = true
		}
		if pm, ok := podMetrics[kube.PodMetricKey(pod.Ref.Namespace, pod.Ref.Name)]; ok && pm.Found {
			haveUsage = true
			useCPU += pm.CPUMilli
			useMem += pm.MemBytes
		}
		for _, path := range [][]string{{"spec", "containers"}, {"spec", "initContainers"}} {
			containers, _, _ := unstructuredNestedSlice(pod, path...)
			for _, container := range containers {
				c, ok := container.(map[string]any)
				if !ok {
					continue
				}
				reqCPU += parseCPUMilliField(c, "requests", "cpu")
				limCPU += parseCPUMilliField(c, "limits", "cpu")
				reqMem += parseMemBytesField(c, "requests", "memory")
				limMem += parseMemBytesField(c, "limits", "memory")
			}
		}
	}
	if len(pods) == 0 {
		return "no pods"
	}
	extra := fmt.Sprintf("pods:%d", len(pods))
	if len(nodes) > 0 {
		nodeNames := sortedMapKeys(nodes)
		extra += " nodes:" + strings.Join(nodeNames, ",")
	}

	lines := []string{
		gaugeLine("cpu", useCPU, reqCPU, limCPU, haveUsage, extra, width),
		gaugeLine("mem", useMem, reqMem, limMem, haveUsage, "", width),
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func parseCPUMilliField(container map[string]any, tier, key string) int64 {
	s, _, _ := nestedString(container, "resources", tier, key)
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.MilliValue()
}

func parseMemBytesField(container map[string]any, tier, key string) int64 {
	s, _, _ := nestedString(container, "resources", tier, key)
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.Value()
}

var gaugeGoodStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("40"))
var gaugeWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
var gaugeBadStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
var gaugeMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

// gaugeLine renders one usage row: a bar (when metrics-server usage data is
// available and a limit is set), req/limit, and an extra info field after
// a "|" separator — keeping the whole panel to 2 lines (cpu, mem) instead
// of spreading req/limit onto their own rows.
func gaugeLine(label string, used, req, limit int64, haveUsage bool, extra string, width int) string {
	var base string
	var style lipgloss.Style
	switch {
	case haveUsage && limit > 0:
		pct := float64(used) / float64(limit)
		if pct < 0 {
			pct = 0
		}
		if pct > 1 {
			pct = 1
		}
		barWidth := 12
		filled := int(pct * float64(barWidth))
		bar := strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled)
		style = gaugeGoodStyle
		if pct >= 0.9 {
			style = gaugeBadStyle
		} else if pct >= 0.7 {
			style = gaugeWarnStyle
		}
		base = fmt.Sprintf("%s [%s] %3.0f%% req:%s lim:%s", label, bar, pct*100, formatQuantity(label, req), formatQuantity(label, limit))
	case haveUsage:
		style = gaugeMutedStyle
		base = fmt.Sprintf("%s %s used  req:%s lim:none", label, formatQuantity(label, used), formatQuantity(label, req))
	default:
		style = gaugeMutedStyle
		base = fmt.Sprintf("%s req:%s lim:%s (metrics-server unavailable)", label, formatQuantity(label, req), formatQuantity(label, limit))
	}
	if extra != "" {
		base += "  | " + extra
	}
	return style.Render(truncateText(base, width))
}

func formatQuantity(label string, v int64) string {
	if label == "mem" {
		return formatMemBytes(v)
	}
	return formatMilli(v)
}

func formatMilli(m int64) string {
	if m <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dm", m)
}

func formatMemBytes(b int64) string {
	if b <= 0 {
		return "-"
	}
	const mi = 1024 * 1024
	if b >= mi {
		return fmt.Sprintf("%dMi", b/mi)
	}
	return fmt.Sprintf("%dKi", b/1024)
}

func (m model) namespaceHelp(width, height int) string {
	if m.contextPicker {
		body := strings.Join([]string{
			"Select a context with enter.",
			"Use / to filter contexts.",
			"Esc returns to the current resource view.",
			"",
			"K9s-compatible command:",
			":ctx opens this picker.",
			":ctx <name> switches directly.",
		}, "\n")
		return paneStyle(false).Width(max(1, width-paneBoxOverhead)).Height(max(1, height-2)).Render(body)
	}
	body := strings.Join([]string{
		"Select a namespace with enter.",
		"Use / to filter namespaces.",
		"Esc returns to the current resource view.",
		"",
		"K9s-compatible command:",
		":ns / :namespace opens this picker.",
		":ns <name> switches directly.",
	}, "\n")
	return paneStyle(false).Width(max(1, width-paneBoxOverhead)).Height(max(1, height-2)).Render(body)
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

// relationsPanelLines lists the object's relations only (Services,
// ConfigMaps, Secrets, ServiceAccounts, PVCs, Ingresses, ...) for the
// top-right Relations pane. Health/pods/problems/events live in the
// status pane instead.
func relationsPanelLines(g *graph.Graph, obj graph.Object) []string {
	records := map[string]string{}
	for _, line := range configUsageLines(g, obj) {
		if ref, ok := navigableRefForLine(g, obj.Ref.Namespace, line); ok {
			records[ref.Key()] = line
		} else {
			// Keep unresolved references visible. A missing Secret/ConfigMap/PVC
			// is often the useful diagnostic result (RBAC, typo, or an object
			// not returned by discovery), and silently dropping it makes the
			// split screen look healthier than the workload really is.
			records["missing:"+line] = "! missing " + line
		}
	}
	addEdges := func(focal graph.ObjectRef) {
		for _, edge := range g.EdgesFor(focal) {
			if edge.Type == "Owns" {
				continue // ownership has its own pane
			}
			other := edge.To
			direction := "→"
			if edge.To.Key() == focal.Key() {
				other = edge.From
				direction = "←"
			}
			if other.Key() == obj.Ref.Key() || other.Kind == "Event" {
				continue
			}
			if _, detailed := records[other.Key()]; detailed {
				continue
			}
			name := other.Name
			if other.Namespace != "" && other.Namespace != obj.Ref.Namespace {
				name = other.Namespace + "/" + other.Name
			}
			alias := summaryKind(other.Kind)
			if ambiguousRefIdentity(g, other) && other.Group != "" {
				alias += "." + other.Group
			}
			records[other.Key()] = fmt.Sprintf("%s: %s  %s %s", alias, name, direction, edge.Type)
		}
	}
	addEdges(obj.Ref)
	for _, pod := range relatedPods(g, obj) {
		if pod.Ref.Key() != obj.Ref.Key() {
			addEdges(pod.Ref)
		}
	}
	lines := make([]string, 0, len(records))
	for _, line := range records {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return []string{"no direct refs"}
	}
	return lines
}

func filteredRelationLines(g *graph.Graph, obj graph.Object, search string) []string {
	lines := relationsPanelLines(g, obj)
	search = strings.ToLower(strings.TrimSpace(search))
	if search == "" {
		return lines
	}
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), search) {
			filtered = append(filtered, line)
		}
	}
	if len(filtered) == 0 {
		return []string{"no relations match: " + search}
	}
	return filtered
}

func ambiguousRefIdentity(g *graph.Graph, ref graph.ObjectRef) bool {
	for _, obj := range g.Objects {
		if obj.Ref.Kind == ref.Kind && obj.Ref.Namespace == ref.Namespace && obj.Ref.Name == ref.Name && obj.Ref.Group != ref.Group {
			return true
		}
	}
	return false
}

var summaryRefStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
var summaryRefSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39")).Bold(true)
var summaryMissingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
var relationSecretStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
var relationConfigStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
var relationServiceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))
var relationStorageStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
var relationGitOpsStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))

func relationRefStyle(line string) lipgloss.Style {
	kind, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(line)), ":")
	switch {
	case strings.HasPrefix(kind, "secret"):
		return relationSecretStyle
	case strings.HasPrefix(kind, "configmap"):
		return relationConfigStyle
	case strings.HasPrefix(kind, "service"):
		return relationServiceStyle
	case strings.HasPrefix(kind, "pvc"), strings.HasPrefix(kind, "persistentvolum"):
		return relationStorageStyle
	case strings.HasPrefix(kind, "application"):
		return relationGitOpsStyle
	default:
		return summaryRefStyle
	}
}

// relationsPanelBody renders relationsPanelLines with the currently
// navigable (clickable) lines underlined and the cursor line highlighted.
func (m model) relationsPanelBody(g *graph.Graph, obj graph.Object, width, maxLines int) string {
	lines := filteredRelationLines(g, obj, m.relationSearch)
	refs := summaryNavigableRefs(g, obj.Ref.Namespace, lines)
	navIndexes := make([]int, 0, len(refs))
	for i := range refs {
		navIndexes = append(navIndexes, i)
	}
	sort.Ints(navIndexes)
	cursorLine := -1
	if len(navIndexes) > 0 {
		idx := m.summaryCursor
		if idx < 0 {
			idx = 0
		}
		if idx >= len(navIndexes) {
			idx = len(navIndexes) - 1
		}
		cursorLine = navIndexes[idx]
	}
	maxLines = max(1, maxLines)
	start := 0
	if cursorLine >= maxLines {
		start = cursorLine - maxLines + 1
	}
	if start > max(0, len(lines)-maxLines) {
		start = max(0, len(lines)-maxLines)
	}
	end := minInt(len(lines), start+maxLines)
	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		line := lines[i]
		text := truncateText(line, width-2)
		if _, navigable := refs[i]; navigable {
			if i == cursorLine {
				out = append(out, "> "+summaryRefSelectedStyle.Render(text))
			} else {
				out = append(out, "  "+relationRefStyle(line).Render(text))
			}
		} else if strings.HasPrefix(line, "! missing ") {
			out = append(out, "  "+summaryMissingStyle.Render(text))
		} else {
			out = append(out, "  "+text)
		}
	}
	if len(out) == 0 {
		out = []string{"no direct refs"}
	}
	return strings.Join(out, "\n")
}

// statusPanelText is the bottom-left quadrant body: health, pod rollup,
// why-it's-failing (problems), recent events, and environment values.
// styledLine pairs plain text with the lipgloss style it should render
// with; kept separate so width-truncation (raw text) always happens before
// coloring (ANSI codes), never after.
type styledLine struct {
	text  string
	style lipgloss.Style
}

var plainLineStyle = lipgloss.NewStyle()
var problemLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
var eventLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var envHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("108")).Bold(true)
var envItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("108"))
var statusSectionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)

// statusPanelLines avoids repeating what the resource list row and the top
// status line already show (running/restarts counts, per-pod rows) — this
// pane is for what isn't visible elsewhere: why something is unhealthy
// (problems, non-Ready status.conditions — covers Node, cert-manager
// Certificates, HPAs, and most CRDs that follow the conditions convention),
// real event messages, then environment values grouped by container.
func statusPanelLines(g *graph.Graph, obj graph.Object) []styledLine {
	var out []styledLine
	status, health := resourceStatus(obj)
	out = append(out, styledLine{"status: " + status, statusColor(health)})
	if managed := gitopsLine(ownerChain(g, obj)); managed != "" {
		out = append(out, styledLine{managed, plainLineStyle})
	}

	pods := relatedPods(g, obj)
	if len(pods) > 1 {
		running, failed, pending, restarts := podRollup(pods)
		out = append(out, styledLine{
			fmt.Sprintf("pods: %d running / %d failed / %d pending  restarts:%d", running, failed, pending, restarts),
			plainLineStyle,
		})
	}
	// Keep the complete container inventory in the model. The pane viewport
	// limits what is painted, while scrolling and Ctrl-F must still reach
	// every container in a large workload.
	if containers := containerStatusLines(pods, 0); len(containers) > 0 {
		out = append(out, styledLine{"containers:", statusSectionStyle})
		out = append(out, containers...)
	}
	if checks := operationalCheckLine(g, obj, pods); checks != "" {
		out = append(out, styledLine{checks, plainLineStyle})
	}

	if conditions := conditionLines(obj); len(conditions) > 0 {
		out = append(out, styledLine{"conditions:", statusSectionStyle})
		out = append(out, conditions...)
	}
	problems := problemLines(g, obj)
	if len(problems) > 0 {
		out = append(out, styledLine{"problems:", statusSectionStyle})
		for _, p := range problems {
			out = append(out, styledLine{p, problemLineStyle})
		}
	}
	if cause := rootCauseLine(g, obj); cause != "" {
		out = append(out, styledLine{cause, problemLineStyle})
	}
	if events := recentEventLines(obj, pods, 0); len(events) > 0 {
		out = append(out, styledLine{"events:", statusSectionStyle})
		for _, e := range events {
			out = append(out, styledLine{e, eventLineStyle})
		}
	}
	for i, e := range envLines(pods, 0) {
		style := envItemStyle
		if i == 0 {
			style = envHeaderStyle
		}
		out = append(out, styledLine{e, style})
	}
	return out
}

func operationalCheckLine(g *graph.Graph, obj graph.Object, pods []graph.Object) string {
	if len(pods) == 0 {
		return ""
	}
	services := map[string]bool{}
	pdb := false
	netpol := false
	nodes := map[string]bool{}
	containers, requests, limits := 0, 0, 0
	for _, pod := range pods {
		if node, _, _ := unstructuredNestedString(pod, "spec", "nodeName"); node != "" {
			nodes[node] = true
		}
		for _, edge := range g.EdgesFor(pod.Ref) {
			if edge.To.Key() != pod.Ref.Key() {
				continue
			}
			switch edge.From.Kind {
			case "Service":
				services[edge.From.Key()] = true
			case "PodDisruptionBudget":
				pdb = true
			case "NetworkPolicy":
				netpol = true
			}
		}
		items, _, _ := unstructuredNestedSlice(pod, "spec", "containers")
		for _, item := range items {
			container, ok := item.(map[string]any)
			if !ok {
				continue
			}
			containers++
			if cpu, _, _ := nestedString(container, "resources", "requests", "cpu"); cpu != "" {
				if memory, _, _ := nestedString(container, "resources", "requests", "memory"); memory != "" {
					requests++
				}
			}
			if cpu, _, _ := nestedString(container, "resources", "limits", "cpu"); cpu != "" {
				if memory, _, _ := nestedString(container, "resources", "limits", "memory"); memory != "" {
					limits++
				}
			}
		}
	}
	return fmt.Sprintf("checks: svc:%d pdb:%s netpol:%s req:%d/%d lim:%d/%d nodes:%d", len(services), yesNo(pdb), yesNo(netpol), requests, containers, limits, containers, len(nodes))
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

// badWhenTrueConditions are Node condition types where True means trouble
// (the opposite polarity of most conditions, where True means healthy).
var badWhenTrueConditions = map[string]bool{
	"MemoryPressure":     true,
	"DiskPressure":       true,
	"PIDPressure":        true,
	"NetworkUnavailable": true,
	"Degraded":           true,
	"Failing":            true,
	"Failure":            true,
	"Error":              true,
	"Stalled":            true,
}

var positiveConditions = map[string]bool{
	"Ready":       true,
	"Available":   true,
	"Healthy":     true,
	"Synced":      true,
	"Established": true,
}

// conditionLines reads status.conditions generically — the convention most
// Kubernetes resources and CRDs follow (Node, Certificate, HPA, SealedSecret,
// custom operators, ...) — and surfaces each with a reason/message, colored
// by whether that condition indicates a problem.
func conditionLines(obj graph.Object) []styledLine {
	conditions, _, _ := unstructuredNestedSlice(obj, "status", "conditions")
	var out []styledLine
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := nestedString(cm, "type")
		status, _, _ := nestedString(cm, "status")
		reason, _, _ := nestedString(cm, "reason")
		message, _, _ := nestedString(cm, "message")
		if typ == "" {
			continue
		}
		line := typ + ": " + status
		if reason != "" {
			line += " (" + reason + ")"
		}
		if message != "" {
			line += " - " + message
		}
		bad := (positiveConditions[typ] && status != "True") || (badWhenTrueConditions[typ] && status == "True")
		style := plainLineStyle
		if bad {
			style = problemLineStyle
		}
		out = append(out, styledLine{line, style})
	}
	return out
}

// statusPanelBody truncates each line to width before coloring (so ANSI
// bytes never get counted as display width), applies the scroll offset,
// and caps the panel at maxLines.
func statusPanelBody(g *graph.Graph, obj graph.Object, width, maxLines, scroll int, search string) string {
	lines := statusPanelLines(g, obj)
	if scroll < 0 {
		scroll = 0
	}
	if maxLines > 0 && scroll > max(0, len(lines)-maxLines) {
		scroll = max(0, len(lines)-maxLines)
	}
	if scroll > len(lines) {
		scroll = len(lines)
	}
	visible := lines[scroll:]
	out := make([]string, 0, len(visible))
	for i, l := range visible {
		if len(out) >= maxLines {
			out = append(out, fmt.Sprintf("... %d more", len(visible)-i))
			break
		}
		line := truncateText(l.text, width)
		if search != "" {
			line = highlightLogLine(line, search)
		}
		out = append(out, l.style.Render(line))
	}
	if len(out) == 0 {
		out = []string{"no status"}
	}
	return strings.Join(out, "\n")
}

func recentEventLines(obj graph.Object, pods []graph.Object, limit int) []string {
	events := append([]string{}, obj.Events...)
	for _, pod := range pods {
		events = append(events, pod.Events...)
	}
	if len(events) == 0 {
		return nil
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events
}

// envLines renders one "env:" header followed by each container's
// variables grouped underneath it, instead of repeating "env: <container>/"
// as a prefix on every line.
func envLines(pods []graph.Object, limit int) []string {
	grouped := map[string][]string{}
	var order []string
	seen := map[string]bool{}
	for _, pod := range pods {
		for _, path := range [][]string{{"spec", "containers"}, {"spec", "initContainers"}} {
			containers, _, _ := unstructuredNestedSlice(pod, path...)
			for _, container := range containers {
				c, ok := container.(map[string]any)
				if !ok {
					continue
				}
				cname, _, _ := nestedString(c, "name")
				envFrom, _, _ := nestedSlice(c, "envFrom")
				for _, envSource := range envFrom {
					source, ok := envSource.(map[string]any)
					if !ok {
						continue
					}
					prefix, _, _ := nestedString(source, "prefix")
					optional, _, _ := nestedBool(source, "configMapRef", "optional")
					if !optional {
						optional, _, _ = nestedBool(source, "secretRef", "optional")
					}
					optionalSuffix := ""
					if optional {
						optionalSuffix = " (optional)"
					}
					line := ""
					if name, _, _ := nestedString(source, "configMapRef", "name"); name != "" {
						line = "envFrom=" + prefix + "<- cm:" + name + "/*" + optionalSuffix
					}
					if name, _, _ := nestedString(source, "secretRef", "name"); name != "" {
						line = "envFrom=" + prefix + "<- secret:" + name + "/*" + optionalSuffix
					}
					if line != "" {
						key := cname + "|" + line
						if !seen[key] {
							seen[key] = true
							if _, ok := grouped[cname]; !ok {
								order = append(order, cname)
							}
							grouped[cname] = append(grouped[cname], line)
						}
					}
				}
				env, _, _ := nestedSlice(c, "env")
				for _, envVar := range env {
					e, ok := envVar.(map[string]any)
					if !ok {
						continue
					}
					name, _, _ := nestedString(e, "name")
					if name == "" {
						continue
					}
					var value string
					if v, found, _ := nestedString(e, "value"); found && v != "" {
						value = v
					} else if cmName, _, _ := nestedString(e, "valueFrom", "configMapKeyRef", "name"); cmName != "" {
						key, _, _ := nestedString(e, "valueFrom", "configMapKeyRef", "key")
						value = fmt.Sprintf("<- cm:%s/%s", cmName, key)
					} else if secretName, _, _ := nestedString(e, "valueFrom", "secretKeyRef", "name"); secretName != "" {
						key, _, _ := nestedString(e, "valueFrom", "secretKeyRef", "key")
						value = fmt.Sprintf("<- secret:%s/%s", secretName, key)
					} else if fieldPath, _, _ := nestedString(e, "valueFrom", "fieldRef", "fieldPath"); fieldPath != "" {
						value = "<- field:" + fieldPath
					} else if resourceName, _, _ := nestedString(e, "valueFrom", "resourceFieldRef", "resource"); resourceName != "" {
						value = "<- resource:" + resourceName
						if divisor, _, _ := nestedString(e, "valueFrom", "resourceFieldRef", "divisor"); divisor != "" {
							value += " divisor:" + divisor
						}
					}
					line := name + "=" + value
					key := cname + "|" + line
					if seen[key] {
						continue
					}
					seen[key] = true
					if _, ok := grouped[cname]; !ok {
						order = append(order, cname)
					}
					grouped[cname] = append(grouped[cname], line)
				}
			}
		}
	}
	if len(grouped) == 0 {
		return nil
	}
	sort.Strings(order)
	lines := []string{"env:"}
	total := 0
	for _, cname := range order {
		vars := grouped[cname]
		sort.Strings(vars)
		header := "  " + cname + ":"
		if cname == "" {
			header = "  (container):"
		}
		lines = append(lines, header)
		for _, v := range vars {
			if limit > 0 && total >= limit {
				lines = append(lines, "    ...more")
				return lines
			}
			lines = append(lines, "    "+v)
			total++
		}
	}
	return lines
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

// containerStatusLines keeps the useful part of a Pod's describe output in
// the always-visible split view: container identity, current state, ready
// bit, restart count, and the actionable waiting/termination reason. For a
// workload this is emitted per related Pod, while a directly selected Pod
// stays compact by omitting its redundant Pod prefix.
func containerStatusLines(pods []graph.Object, limit int) []styledLine {
	var out []styledLine
	for _, pod := range pods {
		for _, path := range [][]string{{"status", "initContainerStatuses"}, {"status", "containerStatuses"}} {
			statuses, _, _ := unstructuredNestedSlice(pod, path...)
			for _, raw := range statuses {
				status, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				name, _, _ := nestedString(status, "name")
				if name == "" {
					name = "?"
				}
				state, health := containerState(status)
				ready, _, _ := nestedBool(status, "ready")
				restarts, _, _ := nestedInt64(status, "restartCount")
				prefix := "container/"
				if strings.HasPrefix(path[len(path)-1], "init") {
					prefix = "init/"
				}
				line := fmt.Sprintf("%s%s: %s %s restarts:%d", prefix, name, state, readyWord(ready), restarts)
				if len(pods) > 1 {
					line = pod.Ref.Name + " " + line
				}
				out = append(out, styledLine{line, statusColor(health)})
				if limit > 0 && len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}

func readyWord(ready bool) string {
	if ready {
		return "ready"
	}
	return "not-ready"
}

func containerState(status map[string]any) (string, string) {
	if reason, _, _ := nestedString(status, "state", "waiting", "reason"); reason != "" {
		return "waiting:" + reason, "bad"
	}
	if reason, _, _ := nestedString(status, "state", "terminated", "reason"); reason != "" {
		if reason == "Completed" {
			return "terminated:" + reason, "good"
		}
		return "terminated:" + reason, "bad"
	}
	if _, found, _ := nestedString(status, "state", "running", "startedAt"); found {
		return "running", "good"
	}
	if _, found, _ := nestedMap(status, "state", "running"); found {
		return "running", "good"
	}
	return "unknown", "warn"
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

	imagePullSecrets, _, _ := unstructuredNestedSlice(pod, "spec", "imagePullSecrets")
	for _, rawRef := range imagePullSecrets {
		ref, ok := rawRef.(map[string]any)
		if !ok {
			continue
		}
		if name, _, _ := nestedString(ref, "name"); name != "" {
			add("Secret", name, "imagePullSecret")
		}
	}

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
		projected, _, _ := nestedSlice(v, "projected", "sources")
		for _, rawSource := range projected {
			source, ok := rawSource.(map[string]any)
			if !ok {
				continue
			}
			if name, _, _ := nestedString(source, "configMap", "name"); name != "" {
				add("ConfigMap", name, "projected:"+volumeName)
			}
			if name, _, _ := nestedString(source, "secret", "name"); name != "" {
				add("Secret", name, "projected:"+volumeName)
			}
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
	case "hpa":
		return "HorizontalPodAutoscaler"
	case "pdb":
		return "PodDisruptionBudget"
	case "netpol":
		return "NetworkPolicy"
	case "endpoint":
		return "EndpointSlice"
	case "service":
		return "Service"
	case "ingress":
		return "Ingress"
	case "route":
		return "Route"
	case "cert":
		return "Certificate"
	case "pod":
		return "Pod"
	default:
		return kind
	}
}

// navigableRefForLine extracts a jumpable object reference from a summary
// line like "secret: mailhog-auth keys:auth.txt use:mount:/authdir". Only
// lines whose leading alias maps to a known kind and whose object actually
// exists in the loaded snapshot are considered navigable.
func navigableRefForLine(g *graph.Graph, namespace, line string) (graph.ObjectRef, bool) {
	alias, rest, found := strings.Cut(line, ": ")
	if !found {
		return graph.ObjectRef{}, false
	}
	kind := kindFromSummary(alias)
	name, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return graph.ObjectRef{}, false
	}
	targetNamespace := namespace
	qualifiedName := false
	if ns, objectName, qualified := strings.Cut(name, "/"); qualified {
		targetNamespace = ns
		name = objectName
		qualifiedName = true
	}
	for _, obj := range g.Objects {
		groupAlias := summaryKind(obj.Ref.Kind)
		if obj.Ref.Group != "" {
			groupAlias += "." + obj.Ref.Group
		}
		if obj.Ref.Name != name || !strings.EqualFold(obj.Ref.Kind, kind) && !strings.EqualFold(summaryKind(obj.Ref.Kind), alias) && !strings.EqualFold(groupAlias, alias) {
			continue
		}
		if obj.Ref.Namespace == targetNamespace {
			return obj.Ref, true
		}
	}
	if qualifiedName {
		return graph.ObjectRef{}, false
	}
	for _, obj := range g.Objects {
		groupAlias := summaryKind(obj.Ref.Kind)
		if obj.Ref.Group != "" {
			groupAlias += "." + obj.Ref.Group
		}
		if obj.Ref.Name == name && (strings.EqualFold(obj.Ref.Kind, kind) || strings.EqualFold(summaryKind(obj.Ref.Kind), alias) || strings.EqualFold(groupAlias, alias)) {
			return obj.Ref, true
		}
	}
	return graph.ObjectRef{}, false
}

func summaryNavigableRefs(g *graph.Graph, namespace string, lines []string) map[int]graph.ObjectRef {
	refs := map[int]graph.ObjectRef{}
	for i, line := range lines {
		if ref, ok := navigableRefForLine(g, namespace, line); ok {
			refs[i] = ref
		}
	}
	return refs
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
	conditionTypes := map[string]bool{}
	conditions, _, _ := unstructuredNestedSlice(obj, "status", "conditions")
	for _, condition := range conditions {
		if m, ok := condition.(map[string]any); ok {
			if typ, _, _ := nestedString(m, "type"); typ != "" {
				conditionTypes[typ] = true
			}
		}
	}
	for _, problem := range g.ProblemsFor(obj.Ref) {
		duplicatedCondition := false
		for typ := range conditionTypes {
			if strings.HasPrefix(problem.Message, typ+" ") {
				duplicatedCondition = true
				break
			}
		}
		if duplicatedCondition {
			continue
		}
		line := "problem: " + problem.Message
		seen[line] = true
	}
	for line := range seen {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

func rootCauseLine(g *graph.Graph, obj graph.Object) string {
	path, problem, ok := g.ProblemPath(obj.Ref, 5)
	if !ok {
		return ""
	}
	labels := make([]string, 0, len(path))
	for _, ref := range path {
		labels = append(labels, summaryKind(ref.Kind)+"/"+ref.Name)
	}
	return "cause: " + strings.Join(labels, " → ") + " — " + problem.Message
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
	if m.logPrevious {
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
	if m.logSearch != "" {
		header += " /" + m.logSearch
	}
	if m.logScroll > 0 {
		header += fmt.Sprintf(" scrolled:-%d", m.logScroll)
	}
	lines := wrapLine(header+"  j/k scroll  G live  / search  n/N next  space pause", width)
	if m.logErr != "" {
		lines = appendPanelLines(lines, []string{"error: " + m.logErr}, width, maxLines)
		return strings.Join(limitTail(lines, maxLines), "\n")
	}
	if len(m.logLines) == 0 {
		lines = appendPanelLines(lines, []string{"Loading logs..."}, width, maxLines)
		return strings.Join(lines, "\n")
	}
	logScroll := max(0, m.logScroll)
	if logScroll > max(0, len(m.logLines)-1) {
		logScroll = max(0, len(m.logLines)-1)
	}
	end := len(m.logLines) - logScroll
	if end < 1 {
		end = 1
	}
	logs := m.logLines[:end]
	if len(logs) > maxLines-1 {
		logs = logs[len(logs)-(maxLines-1):]
	}
outer:
	for _, line := range logs {
		var pieces []string
		if m.logWrap {
			pieces = wrapLine(line, width)
		} else {
			pieces = []string{truncateText(line, width)}
		}
		for _, piece := range pieces {
			lines = append(lines, renderLogLine(piece, m.logSearch))
			if len(lines) >= maxLines {
				break outer
			}
		}
	}
	return strings.Join(limitTail(lines, maxLines), "\n")
}

var logSearchHighlight = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("214")).Bold(true)
var logTimestampStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("103"))

func renderLogLine(line, search string) string {
	ts, rest, ok := splitLogTimestamp(line)
	if !ok {
		return highlightLogLine(line, search)
	}
	return logTimestampStyle.Render(ts) + " " + highlightLogLine(rest, search)
}

func splitLogTimestamp(line string) (string, string, bool) {
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return "", line, false
	}
	token := line[:idx]
	if _, err := time.Parse(time.RFC3339Nano, token); err != nil {
		return "", line, false
	}
	return token, line[idx+1:], true
}

func highlightLogLine(line, needle string) string {
	if needle == "" {
		return line
	}
	lower := strings.ToLower(line)
	target := strings.ToLower(needle)
	idx := strings.Index(lower, target)
	if idx < 0 {
		return line
	}
	var b strings.Builder
	rest := line
	for {
		lowerRest := strings.ToLower(rest)
		i := strings.Index(lowerRest, target)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		b.WriteString(logSearchHighlight.Render(rest[i : i+len(needle)]))
		rest = rest[i+len(needle):]
	}
	return b.String()
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

func impactView(g *graph.Graph, obj graph.Object) string {
	refs := g.ImpactFor(obj.Ref, 5)
	if len(refs) == 0 {
		return "No loaded objects depend on this resource."
	}
	lines := []string{fmt.Sprintf("%d potentially affected objects", len(refs))}
	for _, ref := range refs {
		line := summaryKind(ref.Kind) + ": " + ref.Name
		if problems := g.ProblemsFor(ref); len(problems) > 0 {
			line += "  ! " + problems[0].Message
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func usedByView(g *graph.Graph, obj graph.Object) string {
	lines := make([]string, 0)
	for _, edge := range g.EdgesFor(obj.Ref) {
		if edge.To.Key() != obj.Ref.Key() || edge.From.Key() == obj.Ref.Key() {
			continue
		}
		// Ownership and management describe controllers, not consumers of a
		// configuration/storage object. UsedBy is intentionally direct and
		// reference-oriented; Impact remains available for the wider graph.
		if edge.Type == "Owns" || edge.Type == "Manages" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  [%s]", edge.From.Label(), edge.Type))
	}
	if len(lines) == 0 {
		return "No direct consumers found for this resource."
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func loadErrorsView(errors []string) string {
	if len(errors) == 0 {
		return "No snapshot load warnings."
	}
	lines := []string{fmt.Sprintf("%d resources could not be loaded:", len(errors))}
	lines = append(lines, errors...)
	return strings.Join(lines, "\n")
}

func helpView() string {
	return strings.Join([]string{
		"tab  switch pane (resources/chain/relations/status)",
		"-    toggle between the last two resource views",
		"[ / ]    replay previous/next executed resource command",
		"N    sort by name; S    status; P    namespace (-A); A    cycle age sort",
		"ctrl-a  show all discovered resource aliases",
		"ctrl-e  toggle the resource header",
		"ctrl-g  toggle context/namespace breadcrumbs",
		"ctrl-z  show only Warning events (Event view)",
		"ctrl-w  toggle wide resource columns (in command mode: delete previous word)",
		"c    copy selected resource name; n    copy selected namespace",
		"ctrl-s  copy selected resource YAML (Secret payloads redacted)",
		"space  mark selected resource; ctrl-space mark range; ctrl-\\ clear marks",
		"l    fullscreen logs for the selected resource, esc back",
		"/    filter resources, or search logs when logs are fullscreen",
		"resource filters: /regex; /-f fuzzy; /!regex inverse; /-l key=value label selector",
		":    command mode",
		"relations pane: j/k select a ref, enter opens it (cm/secret/sa/pvc/svc/... values in Yaml), esc back",
		"owner chain pane: j/k select an owner, Ctrl-F filter chain, enter opens it, esc back",
		"Shift-J  focus owner chain",
		"fullscreen logs: j/k or ↑/↓ scroll, PgUp/PgDn or Ctrl-U/Ctrl-D, Home/End, G live tail, n/N search, p/P previous",
		"status pane: j/k, PgUp/PgDn or Ctrl-U/Ctrl-D, Home/End, Ctrl-F search, G top",
		"resource views: j/k, PgUp/PgDn or Ctrl-U/Ctrl-D, Home/End, Ctrl-F search, n/N, G top",
		"f    toggle fullscreen for YAML/details/events/problems/aliases",
		"i    blast radius / impact for the selected resource",
		"u    direct consumers / Used By for the selected resource",
		"z    view ReplicaSets for a selected Deployment",
		"!    snapshot load warnings (RBAC/API discovery failures)",
		":q / ctrl-c quit",
		":pod :svc :deploy :sts :ds :job :cj :cm :secret :pvc :all (tab completes any discovered kind)",
		":pod -l app=backend  label selector; :pod /!worker  inverse filter",
		":node :events :hpa :np :pdb :quota :cert",
		":grep <text> / :nogrep / :since 5m",
		":sort age|old|status|namespace|default",
		"space pause/follow logs",
		"A    sort by age newest/oldest/default",
		"w    wrap logs",
		"p/P  previous logs",
		"t    timestamps",
		":ctx <name> / :ctx",
		":ns / :namespace",
		":ns <namespace>",
		":kubeconfig <path>",
		":refresh",
		":watch / :nowatch  toggle 3s live snapshot refresh",
		":help / :aliases / :yaml / :describe / :events / :problems / :impact / :warnings",
		"r    relations",
		"d    details",
		"y    YAML",
		"u    Used By / consumers",
		"z    Deployment -> ReplicaSets",
		"e    events",
		"p    problems",
		"i    impact / blast radius",
		"!    load warnings",
		"?    help",
	}, "\n")
}

func aliasesView(resources []kube.ResourceType) string {
	if len(resources) == 0 {
		return "No discovered resource aliases."
	}
	sorted := append([]kube.ResourceType(nil), resources...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := strings.ToLower(sorted[i].Kind)
		right := strings.ToLower(sorted[j].Kind)
		if left == right {
			return sorted[i].GVR.Resource < sorted[j].GVR.Resource
		}
		return left < right
	})
	lines := []string{"kind                         resource                         short names"}
	for _, resource := range sorted {
		short := "-"
		if len(resource.ShortNames) > 0 {
			short = strings.Join(resource.ShortNames, ",")
		}
		gvr := resource.GVR.Resource
		if resource.GVR.Group != "" {
			gvr = resource.GVR.Group + "/" + gvr
		}
		lines = append(lines, fmt.Sprintf("%-28s %-32s %s", resource.Kind, gvr, short))
	}
	return strings.Join(lines, "\n")
}

func (m model) footer() string {
	text := m.status
	if m.commandMode {
		text = ":" + m.command
	}
	if m.logSearchMode {
		text = "/" + m.command
	}
	if m.viewSearchMode {
		text = "find: " + m.command
	}
	if m.statusSearchMode {
		text = "find status: " + m.command
	}
	if text == "" {
		text = "tab panes  l logs  enter open ref  i impact  :<kind> (tab complete)  Ctrl-R refresh  ? help  :q"
	}
	return lipgloss.NewStyle().
		Width(max(1, m.width)).
		Foreground(lipgloss.Color("245")).
		Render(truncateText(text, max(1, m.width-1)))
}

// Both pane renderers take `height` as the box's TOTAL rendered height
// (borders included). lipgloss Style.Height() sets content height only —
// the rounded border in paneStyle adds 2 more rows on top of that — so we
// must ask lipgloss for height-2 to end up with `height` on screen.

func paneTitle(title, body string, width, height int, active bool) string {
	bodyWidth := max(8, width-4)
	contentHeight := max(1, height-2)
	content := titleStyle.Render(truncateText(title, bodyWidth)) + "\n" + fitPanelText(body, bodyWidth, contentHeight-1)
	return paneStyle(active).Width(max(1, width-paneBoxOverhead)).Height(contentHeight).Render(content)
}

// paneTitleRaw renders a body that the caller has already truncated/wrapped
// and styled (ANSI colors included), so it must not be re-wrapped by
// fitPanelText — re-wrapping would count escape-code bytes as width. The
// caller must size its body to height-3 lines (border top+bottom + title).
func paneTitleRaw(title, body string, width, height int, active bool) string {
	bodyWidth := max(8, width-4)
	contentHeight := max(1, height-2)
	content := titleStyle.Render(truncateText(title, bodyWidth)) + "\n" + body
	return paneStyle(active).Width(max(1, width-paneBoxOverhead)).Height(contentHeight).Render(content)
}

func paneStyle(active bool) lipgloss.Style {
	// k9s uses a crisp single-line frame. NormalBorder also avoids the
	// rounded-corner glyphs becoming visually uneven when two panes meet or
	// when the terminal switches to a narrow/legacy character set.
	border := lipgloss.NormalBorder()
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
	if conditions, _, _ := unstructuredNestedSlice(obj, "status", "conditions"); len(conditions) > 0 {
		status := conditionStatus(obj)
		return status, statusHealth(status)
	}
	if phase, _, _ := unstructuredNestedString(obj, "status", "phase"); phase != "" {
		return phase, statusHealth(phase)
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
	healthy := ""
	for _, condition := range conditions {
		c, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := nestedString(c, "type")
		status, _, _ := nestedString(c, "status")
		reason, _, _ := nestedString(c, "reason")
		if badWhenTrueConditions[typ] && status == "True" {
			if reason != "" {
				return typ + " " + status + " " + reason
			}
			return typ + " " + status
		}
		if positiveConditions[typ] {
			if status != "True" {
				if reason != "" {
					return typ + " " + status + " " + reason
				}
				return typ + " " + status
			}
			healthy = typ
		}
	}
	if healthy != "" {
		return healthy
	}
	return obj.Ref.Namespace
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
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return truncateDisplayWidth(s, maxWidth)
	}
	return truncateDisplayWidth(s, maxWidth-3) + "..."
}

func truncateDisplayWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	var out strings.Builder
	width := 0
	for _, r := range s {
		part := string(r)
		partWidth := lipgloss.Width(part)
		if width+partWidth > maxWidth {
			break
		}
		out.WriteRune(r)
		width += partWidth
	}
	return out.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
