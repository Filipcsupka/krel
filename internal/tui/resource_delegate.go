package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/filipcsupka/krel/internal/graph"
)

type resourceDelegate struct{}

func (d resourceDelegate) Height() int  { return 1 }
func (d resourceDelegate) Spacing() int { return 0 }
func (d resourceDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd {
	return nil
}

func (d resourceDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it, ok := listItem.(item)
	if !ok {
		return
	}
	width := max(1, m.Width()-4)
	selected := index == m.Index()
	if it.obj.Ref.Kind == "Pod" && (it.wide || width >= 82) {
		fmt.Fprint(w, renderPodTableRow(it, width, selected))
		return
	}
	fmt.Fprint(w, renderCompactResourceRow(it, width, selected))
}

// renderCompactResourceRow is the narrow-terminal fallback for every
// resource, including Pods whose table would not fit comfortably.
func renderCompactResourceRow(it item, width int, selected bool) string {
	prefix := "  "
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	if selected {
		prefix = "│ "
		nameStyle = nameStyle.Foreground(lipgloss.Color("39")).Bold(true)
	}
	if it.marked {
		prefix = "* "
	}
	status, health := resourceRowStatusMode(it.obj, width, it.wide)
	if age := resourceAge(it.obj); age != "" {
		status += " " + age
	}
	statusStyle := statusColor(health)
	statusLimit := width / 3
	if it.obj.Ref.Kind == "Pod" {
		statusLimit = width / 2
		if it.wide {
			statusLimit = width * 2 / 3
		}
	}
	statusText := truncateText(status, max(1, statusLimit))
	nameWidth := max(1, width-lipgloss.Width(prefix)-lipgloss.Width(statusText)-1)
	name := truncateText(it.Title(), nameWidth)
	pad := nameWidth - lipgloss.Width(name)
	if pad < 1 {
		pad = 1
	}
	return fmt.Sprintf("%s%s%s%s", prefix, nameStyle.Render(name), strings.Repeat(" ", pad), statusStyle.Render(statusText))
}

// renderPodTableRow keeps the high-value Pod fields in stable columns on a
// wide terminal. The compact renderer above remains the fallback for narrow
// terminals, where forcing columns would make names and failure reasons less
// readable. Widths are calculated from display cells rather than bytes so
// Unicode names cannot push the row past the pane border.
func renderPodTableRow(it item, width int, selected bool) string {
	ready, total := podReady(it.obj)
	phase, _, _ := unstructuredNestedString(it.obj, "status", "phase")
	if reason := podImportantReason(it.obj); reason != "" {
		phase = reason
	}
	if phase == "" {
		phase = "-"
	}
	restarts := fmt.Sprintf("%d", podRestarts(it.obj))
	node, _, _ := unstructuredNestedString(it.obj, "spec", "nodeName")
	ip, _, _ := unstructuredNestedString(it.obj, "status", "podIP")
	age := resourceAge(it.obj)
	if age == "" {
		age = "-"
	}

	// Keep the identity and health columns useful first. The node/IP columns
	// share whatever width remains and disappear before those fields do.
	nameWidth := minInt(34, max(18, width/3))
	const readyWidth = 7
	const statusWidth = 18
	const restartsWidth = 10
	const ageWidth = 6
	// Leave one safety cell for terminal-width rounding and style padding.
	separators := 7
	remaining := max(0, width-nameWidth-readyWidth-statusWidth-restartsWidth-ageWidth-separators)
	nodeWidth := remaining / 2
	ipWidth := remaining - nodeWidth
	if remaining < 10 {
		return renderCompactResourceRow(it, width, selected)
	}
	marker := "  "
	if it.marked {
		marker = "* "
	}
	nameTextWidth := max(1, nameWidth-lipgloss.Width(marker))

	fields := []string{
		padTable(truncateText(it.Title(), nameTextWidth), nameTextWidth),
		padTable(fmt.Sprintf("%d/%d", ready, total), readyWidth),
		padTable(truncateText(phase, statusWidth), statusWidth),
		padTable(truncateText(restarts, restartsWidth), restartsWidth),
		padTable(truncateText(orDash(node), nodeWidth), nodeWidth),
		padTable(truncateText(orDash(ip), ipWidth), ipWidth),
		padTable(age, ageWidth),
	}
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	if selected {
		nameStyle = nameStyle.Foreground(lipgloss.Color("39")).Bold(true)
	}
	_, health := resourceStatus(it.obj)
	statusStyle := statusColor(health)
	row := nameStyle.Render(marker+fields[0]) + " " +
		fields[1] + " " + statusStyle.Render(fields[2]) + " " +
		fields[3] + " " + fields[4] + " " + fields[5] + " " + fields[6]
	return row
}

func padTable(value string, width int) string {
	value = truncateText(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func resourceRowStatus(obj graph.Object, width int) (string, string) {
	return resourceRowStatusMode(obj, width, false)
}

func resourceRowStatusMode(obj graph.Object, width int, wide bool) (string, string) {
	status, health := resourceStatus(obj)
	if obj.Ref.Kind != "Pod" {
		return status, health
	}
	node, _, _ := unstructuredNestedString(obj, "spec", "nodeName")
	if node != "" {
		status += " node:" + node
	}
	// IP is useful during network debugging, but only include it when the
	// row has enough room to keep the name and readiness readable.
	if width >= 100 || wide {
		ip, _, _ := unstructuredNestedString(obj, "status", "podIP")
		if ip != "" {
			status += " ip:" + ip
		}
	}
	return status, health
}

func statusColor(health string) lipgloss.Style {
	switch health {
	case "bad":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	case "warn":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	case "good":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("40"))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	}
}

func statusHealth(status string) string {
	normalized := strings.ToLower(status)
	switch {
	case strings.Contains(normalized, "failed"),
		strings.Contains(normalized, "error"),
		strings.Contains(normalized, "crash"),
		strings.Contains(normalized, "imagepull"),
		strings.Contains(normalized, " false"),
		strings.Contains(normalized, "degraded"),
		strings.Contains(normalized, "0/"):
		return "bad"
	case strings.Contains(normalized, "pending"),
		strings.Contains(normalized, "unknown"),
		strings.Contains(normalized, "not ready"):
		return "warn"
	case strings.Contains(normalized, "running"),
		strings.Contains(normalized, "succeeded"),
		strings.Contains(normalized, "bound"),
		strings.Contains(normalized, "available"),
		strings.Contains(normalized, "healthy"),
		strings.Contains(normalized, "synced"),
		strings.Contains(normalized, "established"),
		strings.Contains(normalized, "ready"):
		return "good"
	default:
		return "neutral"
	}
}
