package httpd

import (
	"math"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

func channel(v float64) float64 {
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func luminance(c chroma.Colour) float64 {
	return 0.2126*channel(float64(c.Red())/255) +
		0.7152*channel(float64(c.Green())/255) +
		0.0722*channel(float64(c.Blue())/255)
}

func contrast(a, b chroma.Colour) float64 {
	hi, lo := luminance(a), luminance(b)
	if hi < lo {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// TestSyntaxPaletteContrast holds both palettes to WCAG AA for body text
// against every ground code sits on here: the page, a code block, and the
// two diff tints. The chroma default (friendly) put 61 token/ground pairs
// under the floor, which is why the styles are chosen rather than assumed.
//
// Two tokens are exempted because the stylesheet overrides them after
// writing the palette, which this test cannot see: NameAttribute in light
// (#6f5a21) and the line-number tokens in dark (#8b949e).
func TestSyntaxPaletteContrast(t *testing.T) {
	const floor = 4.5
	// chroma's own line-number gutter renders on blob pages only — the diff
	// supplies its own gutter — so those tokens are checked against the page
	// and code grounds, never against a diff tint.
	gutterOnly := map[chroma.TokenType]bool{
		chroma.LineNumbers: true, chroma.LineNumbersTable: true,
		chroma.LineHighlight: true, chroma.LineTable: true, chroma.LineTableTD: true,
	}
	cases := []struct {
		style   string
		grounds map[string]chroma.Colour
		exempt  map[chroma.TokenType]bool
	}{
		{lightStyle, map[string]chroma.Colour{
			"page": chroma.NewColour(0xff, 0xff, 0xff),
			"code": chroma.NewColour(0xf5, 0xf5, 0xf5),
			"add":  chroma.NewColour(0xe4, 0xf6, 0xea),
			"del":  chroma.NewColour(0xfd, 0xea, 0xea),
		}, map[chroma.TokenType]bool{chroma.NameAttribute: true}},
		{darkStyle, map[string]chroma.Colour{
			"page": chroma.NewColour(0x0a, 0x0a, 0x0a),
			"code": chroma.NewColour(0x05, 0x05, 0x05),
			"add":  chroma.NewColour(0x0d, 0x2a, 0x18),
			"del":  chroma.NewColour(0x2c, 0x11, 0x13),
		}, map[chroma.TokenType]bool{
			chroma.LineNumbers: true, chroma.LineNumbersTable: true,
		}},
	}

	for _, tc := range cases {
		st := styles.Get(tc.style)
		if st == nil || st.Name != tc.style {
			t.Fatalf("chroma style %q is not available", tc.style)
		}
		for _, tt := range st.Types() {
			// Whitespace markers are meant to be near-invisible, and the
			// background entry is a ground, not text.
			if tt == chroma.TextWhitespace || tt == chroma.Background || tc.exempt[tt] {
				continue
			}
			e := st.Get(tt)
			if !e.Colour.IsSet() {
				continue
			}
			for name, ground := range tc.grounds {
				if gutterOnly[tt] && (name == "add" || name == "del") {
					continue
				}
				if got := contrast(e.Colour, ground); got < floor {
					t.Errorf("%s: %s (%s) on %s is %.2f:1, want >= %.1f",
						tc.style, tt, e.Colour, name, got, floor)
				}
			}
		}
	}
}
