package views

import (
	"fmt"
	"strings"

	"velocetorrent/internal/engine"
	"velocetorrent/internal/tui/components"
)

// RenderFileInspectorView renders the pre-allocated file layout and extent telemetry (`[f] Files`).
func RenderFileInspectorView(snap *engine.EngineTelemetrySnapshot, selectedIdx int, width int) string {
	innerWidth := max(60, width-4)

	if snap == nil || len(snap.Torrents) == 0 {
		return components.StylePanel.Width(innerWidth).Render(
			components.StyleLabel.Render("FILE EXTENT INSPECTOR // NO TORRENT SELECTED"),
		)
	}
	if selectedIdx < 0 || selectedIdx >= len(snap.Torrents) {
		selectedIdx = 0
	}
	t := snap.Torrents[selectedIdx]

	header := fmt.Sprintf("%s  %s  %s %s",
		components.StyleLabel.Render("FILE EXTENT & PRE-ALLOCATION INSPECTOR"),
		components.StyleEmerald.Render(t.Name),
		components.StyleLabel.Render("DIR"),
		components.StyleValue.Render(t.OutputDir),
	)

	var lines []string
	lines = append(lines, header)
	lines = append(lines, "")

	pathColW := max(24, innerWidth-74)
	colHdr := fmt.Sprintf("%s %s %s %s %s %s",
		components.PadRight("RELATIVE PATH", pathColW),
		components.PadRight("PRIORITY", 9),
		components.PadLeft("EXTENT SIZE", 12),
		components.PadLeft("ON DISK", 12),
		components.PadRight("ALLOCATION", 14),
		components.PadRight("COMPLETION", 22),
	)
	lines = append(lines, components.StyleLabel.Render(colHdr))
	lines = append(lines, components.StyleLabel.Render(strings.Repeat("─", min(innerWidth-2, len(colHdr)))))

	if len(t.Files) == 0 {
		lines = append(lines, components.StyleNormalRow.Render("METADATA PENDING // NO FILE EXTENTS ALLOCATED YET."))
	} else {
		for _, f := range t.Files {
			allocBadge := "PENDING"
			if f.Preallocated {
				allocBadge = "SPARSE+EXTENT"
			}
			prioLabel := strings.ToUpper(string(f.Priority))
			if prioLabel == "" {
				prioLabel = "NORMAL"
			}
			prog := components.RenderProgressWithLabel(f.Progress, 12, t.Status)
			row := fmt.Sprintf("%s %s %s %s %s %s",
				components.PadRight(f.Path, pathColW),
				components.PadRight(prioLabel, 9),
				components.PadLeft(components.FormatBytes(f.Length), 12),
				components.PadLeft(components.FormatBytes(f.BytesOnDisk), 12),
				components.PadRight(allocBadge, 14),
				prog,
			)
			lines = append(lines, components.StyleValue.Render(row))
		}
		lines = append(lines, "")
		lines = append(lines, components.StyleLabel.Render("PRESS [C] TO CYCLE SELECTIVE FILE PRIORITY (NORMAL -> HIGH -> SKIP)"))
	}

	return components.StylePanel.Width(innerWidth).Render(strings.Join(lines, "\n"))
}

// RenderDirInputModal renders the inline output directory editor (`[d] Change Output Dir`).
func RenderDirInputModal(inputValue string, savedDirs []string, selectedTorrentName string, width int) string {
	innerWidth := max(50, width-4)
	targetLabel := "GLOBAL DEFAULT"
	if selectedTorrentName != "" {
		targetLabel = selectedTorrentName
	}

	var lines []string
	lines = append(lines, components.StyleAmber.Render("CHANGE OUTPUT DIRECTORY // TARGET: "+targetLabel))
	lines = append(lines, "")
	lines = append(lines, components.StyleLabel.Render("PATH > ")+components.StyleValue.Render(inputValue+"█"))
	lines = append(lines, "")
	if len(savedDirs) > 0 {
		lines = append(lines, components.StyleLabel.Render("SAVED QUICK-SELECT PATHS (PRESS [TAB] TO CYCLE):"))
		for i, d := range savedDirs {
			lines = append(lines, fmt.Sprintf("  [%d] %s", i+1, components.StyleValue.Render(d)))
		}
	}
	lines = append(lines, "")
	lines = append(lines, components.StyleLabel.Render("[ENTER] APPLY & PRE-ALLOCATE   [TAB] CYCLE SAVED DIRS   [ESC] CANCEL"))

	return components.StyleActivePanel.Width(innerWidth).Render(strings.Join(lines, "\n"))
}

// RenderAddInputModal renders the inline torrent / magnet URI addition bar (`[a] Add Torrent`).
func RenderAddInputModal(inputValue string, defaultDir string, width int) string {
	innerWidth := max(50, width-4)
	var lines []string
	lines = append(lines, components.StyleEmerald.Render("ADD TORRENT (.TORRENT FILE PATH OR MAGNET:? URI)"))
	lines = append(lines, "")
	lines = append(lines, components.StyleLabel.Render("SOURCE > ")+components.StyleValue.Render(inputValue+"█"))
	lines = append(lines, components.StyleLabel.Render("OUTPUT > ")+components.StyleValue.Render(defaultDir))
	lines = append(lines, "")
	lines = append(lines, components.StyleLabel.Render("[ENTER] QUEUE TRANSFER   [ESC] CANCEL"))
	return components.StyleActivePanel.Width(innerWidth).Render(strings.Join(lines, "\n"))
}

// RenderBottomControlBar renders the persistent keyboard shortcut and status/error bar.
func RenderBottomControlBar(statusMsg string, errMsg string, width int) string {
	innerWidth := max(50, width-4)
	var out []string

	if errMsg != "" {
		out = append(out, components.StyleErrorBanner.Width(innerWidth).Render("ERROR // "+errMsg))
	} else if statusMsg != "" {
		out = append(out, components.StylePanel.Width(innerWidth).Render(
			components.StyleEmerald.Render("STATUS // ")+components.StyleValue.Render(statusMsg),
		))
	}

	keys := strings.Join([]string{
		components.StyleEmerald.Render("[SPACE]") + components.StyleLabel.Render(" PAUSE"),
		components.StyleEmerald.Render("[D]") + components.StyleLabel.Render(" DIR"),
		components.StyleEmerald.Render("[P]") + components.StyleLabel.Render(" PEERS"),
		components.StyleEmerald.Render("[F]") + components.StyleLabel.Render(" FILES"),
		components.StyleEmerald.Render("[B]") + components.StyleLabel.Render(" THROTTLE"),
		components.StyleEmerald.Render("[W]") + components.StyleLabel.Render(" WEB UI"),
		components.StyleEmerald.Render("[A]") + components.StyleLabel.Render(" ADD"),
		components.StyleEmerald.Render("[S]") + components.StyleLabel.Render(" SWARM"),
		components.StyleEmerald.Render("[Q]") + components.StyleLabel.Render(" DETACH"),
	}, "  ")

	out = append(out, components.StyleHeaderBox.Width(innerWidth).Render(keys))
	return strings.Join(out, "\n")
}
