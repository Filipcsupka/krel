package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderPodTableRowKeepsColumnsInsidePane(t *testing.T) {
	obj := testObject("Pod", "app", "api")
	obj.Raw.Object["spec"] = map[string]any{
		"nodeName": "worker-node-with-a-long-name",
	}
	obj.Raw.Object["status"] = map[string]any{
		"phase": "Running",
		"podIP": "10.0.0.12",
		"containerStatuses": []any{
			map[string]any{"ready": true, "restartCount": int64(2)},
		},
	}

	for _, width := range []int{82, 100, 160, 220} {
		row := renderPodTableRow(item{obj: obj}, width, true)
		if got := lipgloss.Width(row); got > width {
			t.Fatalf("width=%d: row width=%d exceeds pane", width, got)
		}
		if !strings.Contains(row, "1/1") || !strings.Contains(row, "Running") || !strings.Contains(row, "2") {
			t.Fatalf("width=%d: row lost core Pod columns: %q", width, row)
		}
	}
}
