package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"velocetorrent/internal/engine"
	"velocetorrent/internal/ipc"
	"velocetorrent/internal/tui/components"
	"velocetorrent/internal/tui/hooks"
)

func TestSubCharProgressBarAndBrailleSparkline(t *testing.T) {
	bar := components.RenderSubCharProgressBar(53.125, 16, engine.StatusDownloading)
	if bar == "" {
		t.Fatalf("expected non-empty sub-character progress bar")
	}

	series := []int64{0, 102400, 524288, 1048576, 4194304, 8388608, 2097152, 512000}
	spark := components.RenderBrailleSparkline(series, 12, components.ColorEmerald)
	lines := strings.Split(spark, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2-row Braille sparkline, got %d rows", len(lines))
	}

	heatmap := components.RenderPieceHeatmap([]uint8{0, 1, 2, 3, 3, 1, 0, 3}, []uint16{1, 2, 3, 4, 2, 1, 0, 5}, 16, 2)
	if !strings.Contains(heatmap, "VERIFIED") {
		t.Fatalf("expected heatmap legend to contain VERIFIED")
	}
}

func TestAppModelKeyBindingsAndViews(t *testing.T) {
	client := ipc.NewClient("127.0.0.1:7878", "")
	model := NewAppModel(client, "127.0.0.1:7878")

	snap := &engine.EngineTelemetrySnapshot{
		DefaultOutputDir: "/tmp/velocetorrent",
		SavedDirs:        []string{"/tmp/velocetorrent"},
		GlobalDownBytesPs: 12582912,
		Torrents: []engine.TorrentTelemetrySnapshot{
			{
				ID:          "0123456789abcdef0123456789abcdef01234567",
				InfoHash:    "0123456789abcdef0123456789abcdef01234567",
				Name:        "archlinux-x86_64.iso",
				OutputDir:   "/tmp/velocetorrent",
				Status:      engine.StatusDownloading,
				TotalSize:   104857600,
				Progress:    42.5,
				DownBytesPs: 12582912,
				ETASeconds:  5,
				PieceCount:  16,
				PieceStates: []uint8{3, 3, 3, 3, 3, 3, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0},
			},
		},
	}

	updated, _ := model.Update(hooks.TelemetryUpdateMsg{Snapshot: snap})
	m := updated.(*AppModel)

	dashOut := m.View()
	if !strings.Contains(dashOut, "archlinux-x86_64.iso") {
		t.Fatalf("expected dashboard view to render torrent name")
	}

	// Press 'p' -> switch to Peer Inspector
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(*AppModel)
	if m.viewMode != ViewPeerInspector {
		t.Fatalf("expected ViewPeerInspector after pressing 'p'")
	}
	if !strings.Contains(m.View(), "SWARM PEER INSPECTOR") {
		t.Fatalf("expected Peer Inspector header in View()")
	}

	// Press 'f' -> switch to File Inspector
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = updated.(*AppModel)
	if m.viewMode != ViewFileInspector {
		t.Fatalf("expected ViewFileInspector after pressing 'f'")
	}

	// Press 'd' -> open Change Output Dir modal
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(*AppModel)
	if m.modalMode != ModalChangeDir {
		t.Fatalf("expected ModalChangeDir after pressing 'd'")
	}
	if !strings.Contains(m.View(), "CHANGE OUTPUT DIRECTORY") {
		t.Fatalf("expected directory modal in View()")
	}
}
