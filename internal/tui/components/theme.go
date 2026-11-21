package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Hardware-instrument color palette adhering strictly to AGENTS.md Section 6.2 (zinc monochrome + emerald/amber semantic accents).
var (
	ColorBg       = lipgloss.Color("#09090B") // zinc-950
	ColorSurface  = lipgloss.Color("#18181B") // zinc-900
	ColorBorder   = lipgloss.Color("#27272A") // zinc-800
	ColorText     = lipgloss.Color("#FAFAFA") // zinc-50
	ColorSubtle   = lipgloss.Color("#A1A1AA") // zinc-400
	ColorMuted    = lipgloss.Color("#71717A") // zinc-500
	ColorEmerald  = lipgloss.Color("#10B981") // active download / verified
	ColorAmber    = lipgloss.Color("#F59E0B") // metadata / verifying / in-flight
	ColorRed      = lipgloss.Color("#EF4444") // error state

	StyleHeaderBox = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(ColorBorder).
			Padding(0, 1)

	StylePanel = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1)

	StyleActivePanel = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(ColorEmerald).
				Padding(0, 1)

	StyleErrorBanner = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(ColorRed).
				Foreground(ColorRed).
				Padding(0, 1)

	StyleLabel = lipgloss.NewStyle().
			Foreground(ColorMuted).
			Bold(true)

	StyleValue = lipgloss.NewStyle().
			Foreground(ColorText)

	StyleEmerald = lipgloss.NewStyle().
			Foreground(ColorEmerald).
			Bold(true)

	StyleAmber = lipgloss.NewStyle().
			Foreground(ColorAmber).
			Bold(true)

	StyleRed = lipgloss.NewStyle().
			Foreground(ColorRed).
			Bold(true)

	StyleSelectedRow = lipgloss.NewStyle().
				Background(lipgloss.Color("#27272A")).
				Foreground(ColorText).
				Bold(true)

	StyleNormalRow = lipgloss.NewStyle().
			Foreground(ColorSubtle)
)

// FormatBytes formats a byte count into a fixed-width tabular string (e.g. "  124.5 MiB").
func FormatBytes(b int64) string {
	if b < 0 {
		b = 0
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%6d   B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	return fmt.Sprintf("%6.1f %s", float64(b)/float64(div), units[exp])
}

// FormatRate formats bytes/sec into a fixed-width tabular velocity string (e.g. "  42.8 MB/s").
func FormatRate(bps int64) string {
	if bps <= 0 {
		return "   0.0 KB/s"
	}
	kb := float64(bps) / 1024.0
	if kb < 1000.0 {
		return fmt.Sprintf("%6.1f KB/s", kb)
	}
	mb := kb / 1024.0
	if mb < 1000.0 {
		return fmt.Sprintf("%6.1f MB/s", mb)
	}
	gb := mb / 1024.0
	return fmt.Sprintf("%6.2f GB/s", gb)
}

// FormatETA formats seconds remaining into a fixed-width tabular string ("00:01:24" or "     INF").
func FormatETA(sec int64) string {
	if sec < 0 {
		return "     INF"
	}
	if sec == 0 {
		return "    DONE"
	}
	if sec >= 360000 {
		return "    >99h"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("   %02d:%02d", m, s)
}

// PadRight pads or truncates a string to an exact rune width.
func PadRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) > width {
		if width <= 3 {
			return string(runes[:width])
		}
		return string(runes[:width-1]) + "…"
	}
	if len(runes) < width {
		return s + strings.Repeat(" ", width-len(runes))
	}
	return s
}

// PadLeft left-pads a string with spaces to an exact rune width for tabular numeric alignment.
func PadLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) >= width {
		return string(runes[:width])
	}
	return strings.Repeat(" ", width-len(runes)) + s
}

// TruncateMiddle shortens a long filesystem path while preserving prefix and suffix.
func TruncateMiddle(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 5 {
		return string(runes[:maxLen])
	}
	half := (maxLen - 1) / 2
	return string(runes[:half]) + "…" + string(runes[len(runes)-(maxLen-1-half):])
}
