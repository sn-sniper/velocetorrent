package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"velocetorrent/internal/engine"
)

// RenderPieceHeatmap renders a multi-row bitfield grid of piece completion and swarm availability.
func RenderPieceHeatmap(states []uint8, avail []uint16, cols int, rows int) string {
	if cols < 16 {
		cols = 16
	}
	if rows < 1 {
		rows = 1
	}
	totalCells := cols * rows
	if len(states) == 0 {
		emptyRow := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("░", cols))
		var out []string
		for r := 0; r < rows; r++ {
			out = append(out, emptyRow)
		}
		return strings.Join(out, "\n")
	}

	styleDone := lipgloss.NewStyle().Foreground(ColorEmerald)
	styleVerify := lipgloss.NewStyle().Foreground(ColorAmber)
	styleInFlight := lipgloss.NewStyle().Foreground(ColorAmber)
	styleSwarm := lipgloss.NewStyle().Foreground(ColorSubtle)
	styleMissing := lipgloss.NewStyle().Foreground(ColorBorder)

	var gridRows []string
	numPieces := len(states)

	for r := 0; r < rows; r++ {
		var rowBuilder strings.Builder
		for c := 0; c < cols; c++ {
			cellIdx := r*cols + c
			startP := (cellIdx * numPieces) / totalCells
			endP := ((cellIdx + 1) * numPieces) / totalCells
			if endP <= startP {
				endP = startP + 1
			}
			if startP >= numPieces {
				rowBuilder.WriteString(styleMissing.Render("░"))
				continue
			}
			if endP > numPieces {
				endP = numPieces
			}

			// Aggregate state within this bucket
			var maxStatus engine.PieceStatus
			allComplete := true
			hasSwarm := false
			for p := startP; p < endP; p++ {
				st := engine.PieceStatus(states[p])
				if st != engine.PieceComplete {
					allComplete = false
				}
				if st > maxStatus {
					maxStatus = st
				}
				if p < len(avail) && avail[p] > 0 {
					hasSwarm = true
				}
			}

			switch {
			case allComplete:
				rowBuilder.WriteString(styleDone.Render("█"))
			case maxStatus == engine.PieceComplete:
				rowBuilder.WriteString(styleDone.Render("▓"))
			case maxStatus == engine.PieceVerifying:
				rowBuilder.WriteString(styleVerify.Render("▓"))
			case maxStatus == engine.PieceInFlight:
				rowBuilder.WriteString(styleInFlight.Render("▒"))
			case hasSwarm:
				rowBuilder.WriteString(styleSwarm.Render("▒"))
			default:
				rowBuilder.WriteString(styleMissing.Render("░"))
			}
		}
		gridRows = append(gridRows, rowBuilder.String())
	}

	legend := fmt.Sprintf(
		"%s VERIFIED   %s IN-FLIGHT/HASHING   %s SWARM AVAIL   %s MISSING",
		styleDone.Render("█"),
		styleInFlight.Render("▒"),
		styleSwarm.Render("▒"),
		styleMissing.Render("░"),
	)
	gridRows = append(gridRows, legend)
	return strings.Join(gridRows, "\n")
}
