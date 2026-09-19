package httpd

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/web"
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

// ground mixes a tenth of the chip colour into the canvas the way
// color-mix(in srgb, chip 10%, canvas) does, and ratio is WCAG contrast.
func ground(chip, canvas string) string {
	ch := func(a, b string) string {
		na, _ := strconv.ParseInt(a, 16, 32)
		nb, _ := strconv.ParseInt(b, 16, 32)
		return fmt.Sprintf("%02x", int(math.Round(0.1*float64(na)+0.9*float64(nb))))
	}
	return "#" + ch(chip[1:3], canvas[1:3]) + ch(chip[3:5], canvas[3:5]) + ch(chip[5:7], canvas[5:7])
}

func ratio(a, b string) float64 {
	la, lb := relLum(a), relLum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// A label colour is 12px text on a ground mixed from itself, so it needs
// 4.5:1 there — a ratio no one colour reaches on both the light and the
// dark canvas. Each label carries a tone per scheme (#226).
func TestChipTones(t *testing.T) {
	cases := append([]string{}, labelPalette...)
	cases = append(cases, "#ffff00", "#FFFFFF", "#000000", "#ffcccc", "#00ff00", "#101010", "#0000ff", "#8250df", "#bf5b16")
	for _, in := range cases {
		light, dark := chipTones(in)
		if got := ratio(light, ground(light, chipCanvasLight)); got < 4.5 {
			t.Errorf("chipTones(%s) light = %s: %.2f:1 on its ground", in, light, got)
		}
		if got := ratio(dark, ground(dark, chipCanvasDark)); got < 4.5 {
			t.Errorf("chipTones(%s) dark = %s: %.2f:1 on its ground", in, dark, got)
		}
	}

	// The hue survives the move wherever scaling can reach the ratio: a
	// purple stays a purple, not a grey, in both schemes.
	light, dark := chipTones("#8250df")
	for _, tone := range []string{light, dark} {
		chans := func(s string) (r, g, b int64) {
			for i, p := range []*int64{&r, &g, &b} {
				v, _ := strconv.ParseInt(s[1+2*i:3+2*i], 16, 32)
				*p = v
			}
			return
		}
		r, g, b := chans(tone)
		if !(b > r && r > g) {
			t.Errorf("#8250df became %s: %d red, %d green, %d blue is no longer a purple", tone, r, g, b)
		}
	}

	// A colour that already clears the ratio is left alone.
	if light, _ := chipTones("#3b2178"); light != "#3b2178" {
		t.Errorf("a colour that already passes on the light canvas moved to %s", light)
	}
}

// The canvases the tones are computed against are the stylesheet's, in
// both schemes: a token moves and these constants move with it.
func TestChipCanvasMatchesStylesheet(t *testing.T) {
	s := string(web.StyleCSS)
	dark := strings.Index(s, "@media (prefers-color-scheme: dark)")
	if dark < 0 {
		t.Fatal("no dark media query in style.css")
	}
	re := regexp.MustCompile(`--canvas:\s*(#[0-9a-f]{6});`)
	for _, c := range []struct{ scheme, in, want string }{
		{"light", s[:dark], chipCanvasLight},
		{"dark", s[dark:], chipCanvasDark},
	} {
		m := re.FindStringSubmatch(c.in)
		if m == nil {
			t.Fatalf("%s: no --canvas token", c.scheme)
		}
		if m[1] != c.want {
			t.Errorf("%s: style.css --canvas is %s, the chip tones use %s", c.scheme, m[1], c.want)
		}
	}
}
