package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type resourceDelegate struct{}

func (d resourceDelegate) Height() int  { return 2 }
func (d resourceDelegate) Spacing() int { return 1 }
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
	prefix := "  "
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	if selected {
		prefix = "│ "
		nameStyle = nameStyle.Foreground(lipgloss.Color("39")).Bold(true)
	}
	status, health := resourceStatus(it.obj)
	if age := resourceAge(it.obj); age != "" {
		status += " age:" + age
	}
	statusStyle := statusColor(health)
	name := truncateText(it.obj.Ref.Label(), width)
	statusLine := truncateText(status, width)
	fmt.Fprintf(w, "%s%s\n  %s", prefix, nameStyle.Render(name), statusStyle.Render(statusLine))
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
		strings.Contains(normalized, "0/"):
		return "bad"
	case strings.Contains(normalized, "pending"),
		strings.Contains(normalized, "unknown"),
		strings.Contains(normalized, "not ready"):
		return "warn"
	case strings.Contains(normalized, "running"),
		strings.Contains(normalized, "succeeded"),
		strings.Contains(normalized, "bound"),
		strings.Contains(normalized, "ready"):
		return "good"
	default:
		return "neutral"
	}
}
