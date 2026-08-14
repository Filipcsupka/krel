package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

	for _, size := range []struct{ w, h int }{{160, 45}, {200, 60}, {80, 24}, {90, 15}, {220, 90}, {100, 30}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		got := updated.(model)
		lines := strings.Split(got.View(), "\n")
		if len(lines) > size.h {
			t.Errorf("height=%d: view rendered %d lines (overflowed terminal)", size.h, len(lines))
		}
	}
}
