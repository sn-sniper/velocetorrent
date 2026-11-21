package components

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"velocetorrent/internal/engine"
)

var subBlocks = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}

// RenderSubCharProgressBar renders a smooth progress bar using 1/8th sub-character Unicode block precision (`▏▎▍▌▋▊▉█`).
func RenderSubCharProgressBar(percent float64, width int, status engine.TorrentStatus) string {
	if width < 4 {
		width = 4
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	totalEighths := int(math.Round((percent / 100.0) * float64(width*8)))
	fullCells := totalEighths / 8
	remEighth := totalEighths % 8

	var filled strings.Builder
	if fullCells > 0 {
		filled.WriteString(strings.Repeat("█", fullCells))
	}
	if fullCells < width && remEighth > 0 {
		filled.WriteRune(subBlocks[remEighth])
		fullCells++
	}

	emptyCells := width - fullCells
	if emptyCells < 0 {
		emptyCells = 0
	}
	emptyStr := strings.Repeat("░", emptyCells)

	var fgColor lipgloss.Color
	switch status {
	case engine.StatusDownloading, engine.StatusSeeding, engine.StatusCompleted:
		fgColor = ColorEmerald
	case engine.StatusMetadata, engine.StatusVerifying, engine.StatusPrealloc:
		fgColor = ColorAmber
	case engine.StatusError:
		fgColor = ColorRed
	default:
		fgColor = ColorMuted
	}

	filledStyle := lipgloss.NewStyle().Foreground(fgColor)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	return filledStyle.Render(filled.String()) + emptyStyle.Render(emptyStr)
}

// RenderProgressWithLabel renders a sub-character progress bar alongside a tabular percentage readout.
func RenderProgressWithLabel(percent float64, barWidth int, status engine.TorrentStatus) string {
	bar := RenderSubCharProgressBar(percent, barWidth, status)
	pctStr := fmt.Sprintf("%6.1f%%", percent)
	return bar + " " + pctStr
}
