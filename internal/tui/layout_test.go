package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/filipcsupka/krel/internal/graph"
)

// Regression test for a bug where every bordered pane rendered 2 rows
// taller than requested (lipgloss Height() sets content height, and the
// border adds 2 more on top of that). With 5 stacked panes the overflow
// compounded enough to push the top of the screen (list header, Usage
// panel) off the visible terminal.
func TestLayoutFitsTerminal(t *testing.T) {
	m := testModel()
	for i := 0; i < 40; i++ {
		m.snapshot.Graph.Objects = append(m.snapshot.Graph.Objects, testObject("Pod", "app", "pod-"+string(rune('a'+i%26))))
	}
	m.snapshot.Graph = graph.New(m.snapshot.Graph.Objects, nil, nil)
	m.list = newResourceList(m.snapshot, "Pod")

	for _, size := range []struct{ w, h int }{{20, 8}, {30, 10}, {160, 45}, {200, 60}, {80, 24}, {90, 15}, {220, 90}, {100, 30}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		got := updated.(model)
		lines := strings.Split(got.View(), "\n")
		if len(lines) > size.h {
			t.Errorf("height=%d: view rendered %d lines (overflowed terminal)", size.h, len(lines))
		}
		for lineNumber, line := range lines {
			if width := lipgloss.Width(line); width > size.w {
				t.Errorf("size=%dx%d line=%d: rendered width %d (overflowed terminal)", size.w, size.h, lineNumber+1, width)
			}
		}
	}
}

func TestResizePreservesNavigationState(t *testing.T) {
	m := testModel()
	m.active = paneStatus
	m.mode = "yaml"
	m.viewScroll = 3
	m.statusScroll = 2
	m.viewSearch = "api"

	for _, size := range []struct{ w, h int }{{160, 45}, {80, 24}, {120, 30}, {55, 16}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = updated.(model)
		if m.active != paneStatus || m.mode != "yaml" || m.viewScroll != 3 || m.statusScroll != 2 || m.viewSearch != "api" {
			t.Fatalf("resize %dx%d changed navigation state: active=%d mode=%q viewScroll=%d statusScroll=%d search=%q", size.w, size.h, m.active, m.mode, m.viewScroll, m.statusScroll, m.viewSearch)
		}
		lines := strings.Split(m.View(), "\n")
		if len(lines) > size.h {
			t.Fatalf("resize %dx%d overflowed to %d lines", size.w, size.h, len(lines))
		}
	}
}
