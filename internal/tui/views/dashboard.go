package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"velocetorrent/internal/engine"
	"velocetorrent/internal/tui/components"
)

// RenderTopTelemetryBar renders the hardware-style global telemetry header bar.
func RenderTopTelemetryBar(snap *engine.EngineTelemetrySnapshot, width int, activeTab string) string {
	if snap == nil {
		return components.StyleHeaderBox.Width(width).Render(
			components.StyleAmber.Render("VELOCETORRENT // CONNECTING TO DAEMON IPC (127.0.0.1:7878)..."),
		)
	}

	title := components.StyleEmerald.Render("VELOCETORRENT") + components.StyleLabel.Render(" // ENGINE v1.0")
	downMetric := components.StyleLabel.Render("DOWN ") + components.StyleEmerald.Render(components.FormatRate(snap.GlobalDownBytesPs))
	upMetric := components.StyleLabel.Render("UP ") + components.StyleValue.Render(components.FormatRate(snap.GlobalUpBytesPs))
	swarmMetric := components.StyleLabel.Render("SWARM ") + components.StyleValue.Render(fmt.Sprintf("%3d/%-3d", snap.GlobalActivePeers, snap.GlobalSwarmPeers))
	iopsMetric := components.StyleLabel.Render("IOPS ") + components.StyleValue.Render(fmt.Sprintf("%4d", snap.GlobalIOPS))
	queueMetric := components.StyleLabel.Render("QUEUE ") + components.StyleValue.Render(fmt.Sprintf("D:%-2d H:%-2d", snap.GlobalDiskQueue, snap.GlobalHashQueue))
	freeMetric := components.StyleLabel.Render("FREE ") + components.StyleValue.Render(components.FormatBytes(int64(snap.DiskFreeBytes)))
	viewBadge := components.StyleAmber.Render("[" + activeTab + "]")

	line1 := strings.Join([]string{title, downMetric, upMetric, swarmMetric, iopsMetric, queueMetric, freeMetric, viewBadge}, "   ")
	line2 := components.StyleLabel.Render("DEFAULT DIR ") + components.StyleValue.Render(components.TruncateMiddle(snap.DefaultOutputDir, max(32, width-18)))

	return components.StyleHeaderBox.Width(max(40, width-2)).Render(line1 + "\n" + line2)
}

// RenderVelocitySection renders side-by-side Braille velocity sparklines for global DOWN and UP speeds.
func RenderVelocitySection(snap *engine.EngineTelemetrySnapshot, width int) string {
	panelWidth := max(24, (width-6)/2)
	graphWidth := max(12, panelWidth-4)

	var downSeries, upSeries []int64
	var peakDown, peakUp int64
	if snap != nil {
		downSeries, upSeries, peakDown, peakUp = components.ExtractVelocitySeries(snap.VelocityHistory)
	}

	downHeader := fmt.Sprintf("%s  %s  %s %s",
		components.StyleLabel.Render("DOWN [60S]"),
		components.StyleEmerald.Render(components.FormatRate(func() int64 {
			if snap != nil {
				return snap.GlobalDownBytesPs
			}
			return 0
		}())),
		components.StyleLabel.Render("PEAK"),
		components.StyleValue.Render(components.FormatRate(peakDown)),
	)
	downGraph := components.RenderBrailleSparkline(downSeries, graphWidth, components.ColorEmerald)
	downBox := components.StylePanel.Width(panelWidth).Render(downHeader + "\n" + downGraph)

	upHeader := fmt.Sprintf("%s    %s  %s %s",
		components.StyleLabel.Render("UP [60S]"),
		components.StyleValue.Render(components.FormatRate(func() int64 {
			if snap != nil {
				return snap.GlobalUpBytesPs
			}
			return 0
		}())),
		components.StyleLabel.Render("PEAK"),
		components.StyleValue.Render(components.FormatRate(peakUp)),
	)
	upGraph := components.RenderBrailleSparkline(upSeries, graphWidth, components.ColorSubtle)
	upBox := components.StylePanel.Width(panelWidth).Render(upHeader + "\n" + upGraph)

	return lipgloss.JoinHorizontal(lipgloss.Top, downBox, "  ", upBox)
}

// RenderDashboardView renders the primary torrent transfer table and real-time piece completion heatmap.
func RenderDashboardView(snap *engine.EngineTelemetrySnapshot, selectedIdx int, width int, height int) string {
	innerWidth := max(60, width-4)

	if snap == nil || len(snap.Torrents) == 0 {
		emptyLines := []string{
			components.StyleLabel.Render("ACTIVE TRANSFERS [0]"),
			"",
			components.StyleValue.Render("NO ACTIVE TORRENTS IN ENGINE SESSION."),
			components.StyleLabel.Render("• PRESS [A] TO ADD A .TORRENT FILE PATH OR MAGNET URI"),
			components.StyleLabel.Render("• PRESS [S] TO SPAWN A LIVE LOCAL TCP SWARM TRANSFER"),
			components.StyleLabel.Render("• OR RUN: vt add <file.torrent|magnet> --dir /path/to/output"),
		}
		return components.StylePanel.Width(innerWidth).Render(strings.Join(emptyLines, "\n"))
	}

	if selectedIdx < 0 {
		selectedIdx = 0
	}
	if selectedIdx >= len(snap.Torrents) {
		selectedIdx = len(snap.Torrents) - 1
	}

	// Calculate responsive column widths
	nameColW := max(18, innerWidth-76)
	hdr := fmt.Sprintf("  %s %s %s %s %s %s %s %s",
		components.PadRight("NAME", nameColW),
		components.PadRight("STATE", 11),
		components.PadRight("PROGRESS", 21),
		components.PadLeft("SIZE", 10),
		components.PadLeft("DOWN", 11),
		components.PadLeft("UP", 11),
		components.PadLeft("ETA", 8),
		components.PadLeft("SWARM", 7),
	)

	var rows []string
	rows = append(rows, components.StyleLabel.Render(hdr))
	rows = append(rows, components.StyleLabel.Render(strings.Repeat("─", min(innerWidth-2, len(hdr)))))

	for idx, t := range snap.Torrents {
		prefix := "  "
		if idx == selectedIdx {
			prefix = "▸ "
		}
		nameStr := components.PadRight(t.Name, nameColW)
		stateStr := components.PadRight(string(t.Status), 11)
		progStr := components.RenderProgressWithLabel(t.Progress, 13, t.Status)
		sizeStr := components.PadLeft(components.FormatBytes(t.TotalSize), 10)
		downStr := components.PadLeft(components.FormatRate(t.DownBytesPs), 11)
		upStr := components.PadLeft(components.FormatRate(t.UpBytesPs), 11)
		etaStr := components.PadLeft(components.FormatETA(t.ETASeconds), 8)
		swarmStr := components.PadLeft(fmt.Sprintf("%d/%d", t.ActivePeers, t.TotalSwarmPeers), 7)

		rowText := fmt.Sprintf("%s%s %s %s %s %s %s %s %s",
			prefix, nameStr, stateStr, progStr, sizeStr, downStr, upStr, etaStr, swarmStr)

		if idx == selectedIdx {
			rows = append(rows, components.StyleSelectedRow.Render(rowText))
		} else {
			rows = append(rows, components.StyleNormalRow.Render(rowText))
		}
	}

	tablePanel := components.StylePanel.Width(innerWidth).Render(strings.Join(rows, "\n"))

	// Selected torrent Piece Map & Metadata Inspector
	sel := snap.Torrents[selectedIdx]
	endgameBadge := ""
	if sel.EndgameActive {
		endgameBadge = "  " + components.StyleAmber.Render("[ENDGAME PIPELINE ACTIVE]")
	}
	seqBadge := ""
	if sel.SequentialMode {
		seqBadge = "  " + components.StyleEmerald.Render("[SEQUENTIAL]")
	}

	pieceHdr := fmt.Sprintf("%s  %s  %s %d/%d (%s)  %s %s%s%s",
		components.StyleLabel.Render("PIECES BITFIELD"),
		components.StyleValue.Render(sel.Name),
		components.StyleLabel.Render("VERIFIED"),
		sel.CompletedPieces,
		sel.PieceCount,
		components.FormatBytes(sel.PieceLength),
		components.StyleLabel.Render("DIR"),
		components.StyleValue.Render(components.TruncateMiddle(sel.OutputDir, 38)),
		endgameBadge,
		seqBadge,
	)

	heatmapCols := max(24, innerWidth-4)
	heatmap := components.RenderPieceHeatmap(sel.PieceStates, sel.PieceAvailability, heatmapCols, 3)
	piecePanel := components.StylePanel.Width(innerWidth).Render(pieceHdr + "\n" + heatmap)

	return tablePanel + "\n" + piecePanel
}
