package views

import (
	"fmt"
	"strings"

	"velocetorrent/internal/engine"
	"velocetorrent/internal/tui/components"
)

// RenderPeerInspectorView renders the detailed swarm peer table (`[p] Inspect Peers`).
func RenderPeerInspectorView(snap *engine.EngineTelemetrySnapshot, selectedIdx int, width int) string {
	innerWidth := max(60, width-4)

	if snap == nil || len(snap.Torrents) == 0 {
		return components.StylePanel.Width(innerWidth).Render(
			components.StyleLabel.Render("SWARM PEER INSPECTOR // NO TORRENT SELECTED"),
		)
	}
	if selectedIdx < 0 || selectedIdx >= len(snap.Torrents) {
		selectedIdx = 0
	}
	t := snap.Torrents[selectedIdx]

	header := fmt.Sprintf("%s  %s  %s %d ACTIVE / %d SWARM  %s %d  %s %d",
		components.StyleLabel.Render("SWARM PEER INSPECTOR"),
		components.StyleEmerald.Render(t.Name),
		components.StyleLabel.Render("PEERS"),
		t.ActivePeers,
		t.TotalSwarmPeers,
		components.StyleLabel.Render("SEEDERS"),
		t.Seeders,
		components.StyleLabel.Render("LEECHERS"),
		t.Leechers,
	)

	var lines []string
	lines = append(lines, header)
	lines = append(lines, "")

	colHdr := fmt.Sprintf("%s %s %s %s %s %s %s %s %s",
		components.PadRight("PEER ADDRESS", 22),
		components.PadRight("CLIENT", 18),
		components.PadRight("PROTO", 9),
		components.PadLeft("DOWN", 11),
		components.PadLeft("UP", 11),
		components.PadLeft("RTT", 8),
		components.PadLeft("BDP Q", 7),
		components.PadLeft("FLAGS", 8),
		components.PadRight("HAVE", 18),
	)
	lines = append(lines, components.StyleLabel.Render(colHdr))
	lines = append(lines, components.StyleLabel.Render(strings.Repeat("─", min(innerWidth-2, len(colHdr)))))

	if len(t.Peers) == 0 {
		lines = append(lines, components.StyleNormalRow.Render("NO CONNECTED PEERS IN CURRENT SWARM WINDOW (IDLE OR SEEDING COMPLETE)."))
	} else {
		for _, p := range t.Peers {
			flags := buildPeerFlags(p)
			rttStr := fmt.Sprintf("%5.1fms", p.RTTMs)
			bdpStr := fmt.Sprintf("%2d/%-3d", p.InFlightBlocks, p.BDPQueueDepth)
			haveBar := components.RenderProgressWithLabel(p.Progress, 8, engine.StatusDownloading)

			row := fmt.Sprintf("%s %s %s %s %s %s %s %s %s",
				components.PadRight(p.Addr, 22),
				components.PadRight(p.Client, 18),
				components.PadRight(p.Transport, 9),
				components.PadLeft(components.FormatRate(p.DownBytesPs), 11),
				components.PadLeft(components.FormatRate(p.UpBytesPs), 11),
				components.PadLeft(rttStr, 8),
				components.PadLeft(bdpStr, 7),
				components.PadLeft(flags, 8),
				haveBar,
			)
			lines = append(lines, components.StyleValue.Render(row))
		}
	}

	lines = append(lines, "")
	lines = append(lines, components.StyleLabel.Render("FLAGS LEGEND: D=DOWNLOADING(UNCHOKED)  d=INTERESTED(CHOKED)  U=UPLOADING  O=10S OPTIMISTIC UNCHOKE"))
	return components.StylePanel.Width(innerWidth).Render(strings.Join(lines, "\n"))
}

func buildPeerFlags(p engine.PeerTelemetrySnapshot) string {
	var b strings.Builder
	if !p.PeerChoking && p.AmInterested {
		b.WriteByte('D')
	} else if p.AmInterested {
		b.WriteByte('d')
	} else {
		b.WriteByte('-')
	}
	if !p.AmChoking && p.PeerInterested {
		b.WriteByte('U')
	} else if p.PeerInterested {
		b.WriteByte('u')
	} else {
		b.WriteByte('-')
	}
	if p.IsOptimistic {
		b.WriteByte('O')
	} else {
		b.WriteByte('-')
	}
	return b.String()
}
