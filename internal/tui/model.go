package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"k8s.io/apimachinery/pkg/api/resource"

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
	logScroll       int
	logRequestID    string
	logEvents       <-chan kube.LogEvent
	logCancel       context.CancelFunc
	logSearch       string
	logSearchMode   bool
	summaryCursor   int
	statusScroll    int
	chainCursor     int
	logsFullscreen  bool
}

type reloadResult struct {
	snapshot kube.Snapshot
	err      error
}

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
}

func (i item) Title() string {
	if i.showNamespace && i.obj.Ref.Namespace != "" {
		return i.obj.Ref.Kind + "/" + i.obj.Ref.Namespace + "/" + i.obj.Ref.Name
	}
	return i.obj.Ref.Label()
}
func (i item) Description() string { return shortStatus(i.obj) }
func (i item) FilterValue() string { return i.Title() }

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
		items = append(items, item{obj: obj, showNamespace: snapshot.Options.AllNamespaces})
	}
	sortItems(items, sortMode)
	l := list.New(items, resourceDelegate{}, 34, 20)
	title := fmt.Sprintf("config: %s  ctx: %s  ns: %s", configLabel(snapshot), snapshot.Context, snapshot.Namespace)
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

func (m model) resourceList() list.Model {
	return newResourceList(m.snapshot, m.resourceKind, warningFilter(m.warningsOnly), m.sortMode)
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
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		lw, lh := m.leftListSize()
		m.list.SetSize(lw, lh)
	case reloadResult:
		m.loading = false
		if msg.err != nil {
			m.status = "reload failed: " + msg.err.Error()
			return m, nil
		}
		m.snapshot = msg.snapshot
		m.namespacePicker = false
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
		m.status = fmt.Sprintf("loaded context %s namespace %s", msg.snapshot.Context, msg.snapshot.Namespace)
		return m, m.loadSelectedLogs()
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
		if !m.namespacePicker && !m.logsFullscreen && m.active == paneChain {
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
			}
		}
		if !m.namespacePicker && !m.logsFullscreen && m.active == paneRelations && m.mode == "relations" {
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
			case "G":
				m.statusScroll = 0
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.namespacePicker {
				m.namespacePicker = false
				m.list = m.resourceList()
				lw, lh := m.leftListSize()
				m.list.SetSize(lw, lh)
				m.status = ""
				return m, nil
			}
			if m.mode != "relations" {
				m.mode = "relations"
				m.summaryCursor = 0
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
			if m.namespacePicker {
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
		case "enter":
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
				return m, nil
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
		case ":":
			m.commandMode = true
			m.command = ""
		case "tab":
			m.active = (m.active + 1) % paneCount
		case "shift+tab":
			m.active = (m.active + paneCount - 1) % paneCount
		case "r":
			m.mode = "relations"
			m.active = paneRelations
		case "d":
			m.mode = "details"
			m.active = paneRelations
		case "y":
			m.mode = "yaml"
			m.active = paneRelations
		case "e":
			m.mode = "events"
			m.active = paneRelations
		case "p":
			m.mode = "problems"
			m.active = paneRelations
		case "i":
			m.mode = "impact"
			m.active = paneRelations
		case "!":
			m.mode = "loaderrors"
			m.active = paneRelations
		case "?":
			m.mode = "help"
			m.active = paneRelations
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
		if len(m.command) > 0 {
			m.command = m.command[:len(m.command)-1]
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

func (m model) summaryNavIndexes() []int {
	obj, ok := m.selected()
	if !ok {
		return nil
	}
	lines := relationsPanelLines(m.snapshot.Graph, obj)
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
	lines := relationsPanelLines(m.snapshot.Graph, obj)
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
	m.resourceKind = ref.Kind
	m.warningsOnly = false
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
	m.summaryCursor = 0
	m.logLines = nil
	m.logErr = ""
	m.logScroll = 0
	m.status = "opened " + ref.Label()
	return m, m.loadSelectedLogs()
}

func (m model) chainNavIndexes() []int {
	obj, ok := m.selected()
	if !ok {
		return nil
	}
	chain := ownerChain(m.snapshot.Graph, obj)
	indexes := make([]int, len(chain))
	for i := range chain {
		indexes[i] = i
	}
	return indexes
}

func (m model) jumpToChainRef() (tea.Model, tea.Cmd) {
	obj, ok := m.selected()
	if !ok {
		return m, nil
	}
	chain := ownerChain(m.snapshot.Graph, obj)
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
	ref := chain[cursor].Ref
	m.resourceKind = ref.Kind
	m.warningsOnly = false
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
	var out []string
	if mb := gitopsLine(chain); mb != "" {
		out = append(out, "  "+truncateText(mb, width-2))
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
		if len(out) >= maxLines {
			out = append(out, fmt.Sprintf("... %d more", len(chain)-i))
			break
		}
		text := truncateText(chainDisplayLabel(o, o.Ref.Key() == obj.Ref.Key()), width-2)
		if i == cursorIndex {
			out = append(out, "> "+summaryRefSelectedStyle.Render(text))
		} else {
			out = append(out, "  "+summaryRefStyle.Render(text))
		}
	}
	if len(out) == 0 {
		out = []string{"no owner chain"}
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
			m.namespacePicker = false
			m.resourceKind = kind
			m.warningsOnly = kind == "Event"
			loaded := m.snapshotHasKind(kind)
			request := kind
			if discoveredResource {
				loaded = m.snapshot.LoadedResources[discovered.Key()]
				request = discovered.CommandName()
			}
			if !loaded {
				opts.ResourceKind = request
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
			m.namespacePicker = false
			m.resourceKind = ""
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

func (m model) loadSelectedLogs() tea.Cmd {
	obj, ok := m.selected()
	if !ok || m.namespacePicker || !m.logsFullscreen {
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

func (m model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	if m.logsFullscreen {
		return m.logsFullscreenView()
	}
	if m.compactLayout() {
		return m.compactView()
	}
	_, listHeight := m.leftListSize()
	leftWidth := leftPaneOuterWidth(m.width)
	rightWidth := max(40, m.width-leftWidth)
	mainHeight := max(8, m.height-1)
	_, leftBottomHeight := m.leftLayoutHeights()

	// Usage is a fixed small strip (2 gauge lines + title + border).
	usageHeight := 5
	rightRemaining := max(6, mainHeight-usageHeight)
	// Relations gets cut down ~30% versus an even split so Status (the
	// more information-dense pane) gets the extra room.
	relationsLines := 1
	if obj, ok := m.selected(); ok {
		relationsLines = len(relationsPanelLines(m.snapshot.Graph, obj))
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

	full := lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol), footer)

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
	mainHeight := max(6, m.height-1)
	width := max(30, m.width)
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
	return body + "\n" + m.footer()
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

// statusPaneView is the smaller bottom-left quadrant: health, why-it's-failing
// (problems), recent events, and environment values for the selected object.
func (m model) statusPaneView(width, height int) string {
	if m.loading {
		return paneTitle("Status", "Loading cluster snapshot...", width, height, false)
	}
	if m.namespacePicker {
		return paneTitle("Status", "Select a namespace in the list, enter to switch.", width, height, m.active == paneStatus)
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
			title = fmt.Sprintf("Status (! %d warnings; j/k scroll)", len(m.snapshot.LoadErrors))
		} else {
			title = "Status (j/k scroll, G top)"
		}
	}
	body := statusPanelBody(m.snapshot.Graph, obj, width-4, height-3, m.statusScroll)
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
	if m.namespacePicker {
		return m.namespaceHelp(width, height)
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
		body := m.relationsPanelBody(m.snapshot.Graph, obj, width-4, height-3)
		return paneTitleRaw(title, body, width, height, m.active == paneRelations)
	}
	var body, title string
	switch m.mode {
	case "yaml":
		body = truncateLines(obj.YAML(), height-3)
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
	case "loaderrors":
		body = loadErrorsView(m.snapshot.LoadErrors)
		title = "Snapshot Load Warnings"
	case "help":
		body = helpView()
		title = "Help"
	}
	return paneTitle(title, body, width, height, m.active == paneRelations)
}

func (m model) compactStatusPaneView(width, height int) string {
	if m.loading || m.namespacePicker {
		return m.statusPaneView(width, height)
	}
	obj, ok := m.selected()
	if !ok {
		return paneTitleRaw("Status + Usage", "No resources loaded.", width, height, true)
	}
	usage := usageSummary(m.snapshot.PodMetrics, relatedPods(m.snapshot.Graph, obj), width-4, 2)
	statusLines := statusPanelBody(m.snapshot.Graph, obj, width-4, max(1, height-6), m.statusScroll)
	body := usage + "\n" + statusLines
	return paneTitleRaw("Status + Usage (j/k scroll, G top)", body, width, height, true)
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

// chainPaneView is the freed left-column-bottom quadrant: the object's
// owner chain (ReplicaSet -> Deployment, Job -> CronJob, ...), extended
// with the OLM Subscription/InstallPlan/CSV chain and GitOps management
// labels when present.
func (m model) chainPaneView(width, height int) string {
	if m.loading {
		return paneTitleRaw("Owner Chain", "Loading cluster snapshot...", width, height, false)
	}
	if m.namespacePicker {
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
	body := m.chainPanelBody(m.snapshot.Graph, obj, width-4, height-3)
	return paneTitleRaw(title, body, width, height, m.active == paneChain)
}

// usagePaneView is the small strip above Relations: CPU/memory usage vs
// limits (gauges, from metrics-server when available) and requests/limits
// summed across the selected object's related pods.
func (m model) usagePaneView(width, height int) string {
	if m.loading || m.namespacePicker {
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
	for _, pod := range pods {
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

	lines := []string{
		gaugeLine("cpu", useCPU, reqCPU, limCPU, haveUsage, "", width),
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

// relationsPanelBody renders relationsPanelLines with the currently
// navigable (clickable) lines underlined and the cursor line highlighted.
func (m model) relationsPanelBody(g *graph.Graph, obj graph.Object, width, maxLines int) string {
	lines := relationsPanelLines(g, obj)
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
	out := make([]string, 0, len(lines)+1)
	for i, line := range lines {
		if len(out) >= maxLines {
			out = append(out, fmt.Sprintf("... %d more", len(lines)-i))
			break
		}
		text := truncateText(line, width-2)
		if _, navigable := refs[i]; navigable {
			if i == cursorLine {
				out = append(out, "> "+summaryRefSelectedStyle.Render(text))
			} else {
				out = append(out, "  "+summaryRefStyle.Render(text))
			}
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

	pods := relatedPods(g, obj)
	if len(pods) > 1 {
		running, failed, pending, restarts := podRollup(pods)
		out = append(out, styledLine{
			fmt.Sprintf("pods: %d running / %d failed / %d pending  restarts:%d", running, failed, pending, restarts),
			plainLineStyle,
		})
	}
	if checks := operationalCheckLine(g, obj, pods); checks != "" {
		out = append(out, styledLine{checks, plainLineStyle})
	}

	for _, c := range conditionLines(obj) {
		out = append(out, c)
	}
	for _, p := range problemLines(g, obj) {
		out = append(out, styledLine{p, problemLineStyle})
	}
	if cause := rootCauseLine(g, obj); cause != "" {
		out = append(out, styledLine{cause, problemLineStyle})
	}
	for _, e := range recentEventLines(obj, pods, 8) {
		out = append(out, styledLine{e, eventLineStyle})
	}
	for i, e := range envLines(pods, 20) {
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
func statusPanelBody(g *graph.Graph, obj graph.Object, width, maxLines, scroll int) string {
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
		out = append(out, l.style.Render(truncateText(l.text, width)))
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
	if len(events) > limit {
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
			if total >= limit {
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
	end := len(m.logLines) - m.logScroll
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
		"l    fullscreen logs for the selected resource, esc back",
		"/    filter resources, or search logs when logs are fullscreen",
		":    command mode",
		"relations pane: j/k select a ref, enter opens it (cm/secret/sa/pvc/svc/... values in Yaml), esc back",
		"owner chain pane: j/k select an owner (or OLM subscription/installplan/csv), enter opens it, esc back",
		"fullscreen logs: j/k or ↑/↓ scroll, G jump to live tail, n/N next/prev search match",
		"status pane: j/k or ↑/↓ scroll, G back to top",
		"i    blast radius / impact for the selected resource",
		"!    snapshot load warnings (RBAC/API discovery failures)",
		":q / ctrl-c quit",
		":pod :svc :deploy :sts :ds :job :cj :cm :secret :pvc :all (tab completes any discovered kind)",
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
		"i    impact / blast radius",
		"!    load warnings",
		"?    help",
	}, "\n")
}

func (m model) footer() string {
	text := m.status
	if m.commandMode {
		text = ":" + m.command
	}
	if m.logSearchMode {
		text = "/" + m.command
	}
	if text == "" {
		text = "tab panes  l logs  enter open ref  i impact  :<kind> (tab complete)  A age-sort  ? help  :q"
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
