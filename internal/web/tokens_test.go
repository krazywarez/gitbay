package web

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// parseTokens returns the --name: value pairs of the light :root block
// and of the :root block inside the dark media query. A value that is
// itself a var() is resolved one level deep within the same scheme.
func parseTokens(css []byte) (light, dark map[string]string) {
	s := string(css)
	rootRe := regexp.MustCompile(`(?s):root\s*\{(.*?)\}`)
	darkIdx := strings.Index(s, "@media (prefers-color-scheme: dark)")
	if darkIdx < 0 {
		return nil, nil
	}
	parse := func(block string) map[string]string {
		m := map[string]string{}
		lineRe := regexp.MustCompile(`--([a-z0-9-]+)\s*:\s*([^;]+);`)
		for _, mm := range lineRe.FindAllStringSubmatch(block, -1) {
			v := strings.TrimSpace(mm[2])
			if i := strings.Index(v, "/*"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			m[mm[1]] = v
		}
		for k, v := range m {
			if strings.HasPrefix(v, "var(--") {
				ref := strings.TrimSuffix(strings.TrimPrefix(v, "var(--"), ")")
				if rv, ok := m[ref]; ok {
					m[k] = rv
				}
			}
		}
		return m
	}
	lm := rootRe.FindStringSubmatch(s[:darkIdx])
	dm := rootRe.FindStringSubmatch(s[darkIdx:])
	if lm == nil || dm == nil {
		return nil, nil
	}
	light = parse(lm[1])
	dark = parse(dm[1])
	for k, v := range light {
		if _, ok := dark[k]; !ok && strings.HasPrefix(v, "#") {
			dark[k] = v // a light-only colour is a bug; keep it visible below
		}
	}
	return light, dark
}

func hexChannel(h string) float64 {
	n, _ := strconv.ParseUint(h, 16, 8)
	c := float64(n) / 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func luminanceHex(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	return 0.2126*hexChannel(hex[0:2]) + 0.7152*hexChannel(hex[2:4]) + 0.0722*hexChannel(hex[4:6])
}

func contrastHex(a, b string) float64 {
	la, lb := luminanceHex(a), luminanceHex(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestTokenContrast is the contract for the colour tokens in style.css:
// every text colour clears WCAG AA on every ground it lands on, in both
// schemes. The hex values in the stylesheet are free to move as long as
// this passes.
func TestTokenContrast(t *testing.T) {
	light, dark := parseTokens(StyleCSS)
	if light == nil || dark == nil {
		t.Fatal("could not find the light and dark :root blocks in style.css")
	}
	type check struct {
		fg, bg string
		floor  float64
	}
	checks := []check{
		{"fg", "canvas", 7}, {"fg", "surface", 7},
		{"muted", "canvas", 4.5}, {"muted", "surface", 4.5},
		{"link", "canvas", 4.5}, {"link", "surface", 4.5}, {"link", "hover", 4.5},
		{"fill-fg", "fill", 4.5}, {"fill", "surface", 3},
		{"line", "canvas", 3},
		{"mark", "canvas", 3},
		{"warn", "canvas", 4.5}, {"warn", "surface", 4.5},
		{"ok", "canvas", 4.5}, {"ok", "surface", 4.5},
		{"bad", "canvas", 4.5}, {"bad", "surface", 4.5},
		{"done", "canvas", 4.5}, {"done", "surface", 4.5},
		{"focus", "canvas", 3},
		{"shell-fg", "shell-bg", 7}, {"shell-muted", "shell-bg", 4.5}, {"shell-mark", "shell-bg", 3},
	}
	for name, scheme := range map[string]map[string]string{"light": light, "dark": dark} {
		for _, tok := range []string{"canvas", "surface", "inset", "hover", "line", "faint", "fg", "muted", "link", "fill", "fill-fg", "mark", "warn", "ok", "bad", "done", "neutral", "focus", "shell-bg", "shell-fg", "shell-muted", "shell-mark"} {
			v, ok := scheme[tok]
			if !ok || !strings.HasPrefix(v, "#") {
				t.Errorf("%s: --%s is missing or not a hex colour (%q)", name, tok, v)
			}
		}
		if t.Failed() {
			continue
		}
		for _, c := range checks {
			if got := contrastHex(scheme[c.fg], scheme[c.bg]); got < c.floor {
				t.Errorf("%s: --%s (%s) on --%s (%s) is %.2f:1, want >= %.1f", name, c.fg, scheme[c.fg], c.bg, scheme[c.bg], got, c.floor)
			}
		}
		// The surface ladder must be visible: canvas, surface and inset
		// are three grounds, not one.
		lc, ls, _ := luminanceHex(scheme["canvas"]), luminanceHex(scheme["surface"]), luminanceHex(scheme["inset"])
		if r := (math.Max(lc, ls) + 0.05) / (math.Min(lc, ls) + 0.05); r < 1.15 {
			t.Errorf("%s: canvas %s and surface %s are %.2f apart, want >= 1.15", name, scheme["canvas"], scheme["surface"], r)
		}
		if scheme["inset"] == scheme["surface"] {
			t.Errorf("%s: inset and surface are the same colour %s", name, scheme["inset"])
		}
	}
}
