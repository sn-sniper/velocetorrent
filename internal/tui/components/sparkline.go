package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"velocetorrent/internal/engine"
)

// brailleBarLevels maps 0..4 vertical fill levels for left and right sub-columns in a single Unicode Braille cell (U+2800).
// Left column dots (bottom to top): dot 7 (0x40), dot 3 (0x04), dot 2 (0x02), dot 1 (0x01)
// Right column dots (bottom to top): dot 8 (0x80), dot 6 (0x20), dot 5 (0x10), dot 4 (0x08)
var (
	brailleLeftMask  = [5]rune{0x00, 0x40, 0x44, 0x46, 0x47}
	brailleRightMask = [5]rune{0x00, 0x80, 0xA0, 0xB0, 0xB8}
)

// RenderBrailleSparkline renders a 2-row high-resolution Braille sparkline (`⠀..⣿`) from a slice of int64 values.
// Each character column encodes 2 consecutive time samples across 8 vertical levels (2 rows x 4 dots).
func RenderBrailleSparkline(values []int64, width int, color lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	numSamples := width * 2
	samples := make([]int64, numSamples)
	if len(values) > 0 {
		if len(values) >= numSamples {
			copy(samples, values[len(values)-numSamples:])
		} else {
			offset := numSamples - len(values)
			copy(samples[offset:], values)
		}
	}

	var peak int64 = 1024 // minimum 1 KB/s baseline scale
	for _, v := range samples {
		if v > peak {
			peak = v
		}
	}

	var topRow strings.Builder
	var botRow strings.Builder

	for col := 0; col < width; col++ {
		vLeft := samples[col*2]
		vRight := samples[col*2+1]

		lvlLeft := int((vLeft * 8) / peak)
		if vLeft > 0 && lvlLeft == 0 {
			lvlLeft = 1
		}
		if lvlLeft > 8 {
			lvlLeft = 8
		}

		lvlRight := int((vRight * 8) / peak)
		if vRight > 0 && lvlRight == 0 {
			lvlRight = 1
		}
		if lvlRight > 8 {
			lvlRight = 8
		}

		botLeft := minInt(lvlLeft, 4)
		topLeft := maxInt(lvlLeft-4, 0)

		botRight := minInt(lvlRight, 4)
		topRight := maxInt(lvlRight-4, 0)

		topRune := rune(0x2800) | brailleLeftMask[topLeft] | brailleRightMask[topRight]
		botRune := rune(0x2800) | brailleLeftMask[botLeft] | brailleRightMask[botRight]

		topRow.WriteRune(topRune)
		botRow.WriteRune(botRune)
	}

	style := lipgloss.NewStyle().Foreground(color)
	return style.Render(topRow.String()) + "\n" + style.Render(botRow.String())
}

// ExtractVelocitySeries splits a slice of VelocitySample into separate download and upload series.
func ExtractVelocitySeries(history []engine.VelocitySample) (down []int64, up []int64, peakDown int64, peakUp int64) {
	down = make([]int64, len(history))
	up = make([]int64, len(history))
	for i, s := range history {
		down[i] = s.DownBytesPs
		up[i] = s.UpBytesPs
		if s.DownBytesPs > peakDown {
			peakDown = s.DownBytesPs
		}
		if s.UpBytesPs > peakUp {
			peakUp = s.UpBytesPs
		}
	}
	return down, up, peakDown, peakUp
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
