package httpd

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The PNG badge, for readers that cannot decode SVG. iOS is the case that
// forced it: UIImage decodes SVG only from an asset catalog, so a native
// README view has no way to render the SVG badge. It is drawn at 2x because a
// raster badge has no other way to stay sharp, and a badge carries no channel
// to declare its own scale.

const (
	badgeScale  = 2
	badgeHeight = 20
	badgeRadius = 3
	badgeFontPt = 11
)

// badgeFace is parsed once. Go Regular, not the Verdana the SVG names: the SVG
// asks a browser for a font it already has, while a raster badge has to carry
// one, and the Go fonts are the only TTF in the module graph.
var badgeFace = sync.OnceValues(func() (font.Face, error) {
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{
		Size: badgeFontPt * badgeScale,
		DPI:  72,
		// Hinting rounds stems to the pixel grid, which is what keeps 11px text
		// legible rather than smeared.
		Hinting: font.HintingFull,
	})
})

// badgePNG draws the same two-part pill badgeSVG describes.
//
// Widths are measured against the real face rather than reusing badgeWidth's
// Verdana approximation, which exists only so the SVG need not ship metrics.
// Here the metrics are already loaded, so the pill fits its text exactly.
func badgePNG(label, state string) ([]byte, error) {
	face, err := badgeFace()
	if err != nil {
		return nil, err
	}
	fill := badgeColors[state]
	if fill == "" {
		fill = badgeColors["unknown"]
	}

	pad := 10 * badgeScale
	lw := font.MeasureString(face, label).Ceil() + pad
	sw := font.MeasureString(face, state).Ceil() + pad
	h := badgeHeight * badgeScale
	r := badgeRadius * badgeScale

	img := image.NewRGBA(image.Rect(0, 0, lw+sw, h))
	// Three fills, as in the SVG: the grey pill, the coloured pill over its
	// right half, then a square patch squaring off that pill's left edge.
	fillRoundRect(img, image.Rect(0, 0, lw+sw, h), r, mustHex("#444d56"))
	fillRoundRect(img, image.Rect(lw, 0, lw+sw, h), r, mustHex(fill))
	draw.Draw(img, image.Rect(lw, 0, lw+2*r, h), image.NewUniform(mustHex(fill)), image.Point{}, draw.Src)

	// y=14 of 20 in the SVG is the baseline; it is the same fraction here.
	baseline := 14 * badgeScale
	drawCentered(img, face, label, lw/2, baseline)
	drawCentered(img, face, state, lw+sw/2, baseline)

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func drawCentered(dst *image.RGBA, face font.Face, s string, centerX, baseline int) {
	width := font.MeasureString(face, s)
	drawer := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(color.White),
		Face: face,
		Dot:  fixed.P(centerX, baseline).Sub(fixed.Point26_6{X: width / 2}),
	}
	drawer.DrawString(s)
}

// fillRoundRect fills r with c, rounding its four corners by radius.
func fillRoundRect(dst *image.RGBA, r image.Rectangle, radius int, c color.Color) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if insideRoundRect(x, y, r, radius) {
				dst.Set(x, y, c)
			}
		}
	}
}

func insideRoundRect(x, y int, r image.Rectangle, radius int) bool {
	// Corner centres, inset by the radius. A pixel outside the rectangle the
	// corners inscribe is in a corner, and belongs only if it is within radius
	// of the nearest centre.
	cx, cy := x, y
	switch {
	case x < r.Min.X+radius:
		cx = r.Min.X + radius
	case x >= r.Max.X-radius:
		cx = r.Max.X - radius - 1
	}
	switch {
	case y < r.Min.Y+radius:
		cy = r.Min.Y + radius
	case y >= r.Max.Y-radius:
		cy = r.Max.Y - radius - 1
	}
	if cx == x && cy == y {
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

// mustHex parses the "#rrggbb" literals badgeColors holds. They are constants
// in this package, so a bad one is a programming error, not input.
func mustHex(s string) color.RGBA {
	var r, g, b uint8
	for i, shift := range []uint{0, 1, 2} {
		hi, lo := hexNibble(s[1+i*2]), hexNibble(s[2+i*2])
		v := hi<<4 | lo
		switch shift {
		case 0:
			r = v
		case 1:
			g = v
		case 2:
			b = v
		}
	}
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

func hexNibble(c byte) uint8 {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0
	}
}
