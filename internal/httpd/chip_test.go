package httpd

import (
	"math"
	"strconv"
	"testing"
)

// relLum is WCAG relative luminance of a #rrggbb colour.
func relLum(hex string) float64 {
	lin := func(s string) float64 {
		n, _ := strconv.ParseInt(s, 16, 32)
		v := float64(n) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(hex[1:3]) + 0.7152*lin(hex[3:5]) + 0.0722*lin(hex[5:7])
}

// A label colour is text on the chip. The palette passes through; a
// stored yellow, white, black or pastel comes back with a luminance that
// clears 3:1 against both white and the dark ground (#120).
func TestClampChip(t *testing.T) {
	for _, keep := range labelPalette {
		if got := clampChip(keep); got != keep {
			t.Errorf("palette colour %s changed to %s", keep, got)
		}
	}
	for _, in := range []string{"#ffff00", "#FFFFFF", "#000000", "#ffcccc", "#00ff00", "#101010"} {
		got := clampChip(in)
		y := relLum(got)
		onWhite := 1.05 / (y + 0.05)
		onDark := (y + 0.05) / (relLum("#0a0a0a") + 0.05)
		if onWhite < 3 || onDark < 3 {
			t.Errorf("clampChip(%s) = %s: %.2f:1 on white, %.2f:1 on dark", in, got, onWhite, onDark)
		}
	}
	if got := clampChip("#ffff00"); got == "#ffff00" {
		t.Error("yellow passed through unchanged")
	}
}
