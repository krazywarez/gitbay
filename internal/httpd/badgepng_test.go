package httpd

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func decodeBadge(t *testing.T, label, state string) image.Image {
	t.Helper()
	out, err := badgePNG(label, state)
	if err != nil {
		t.Fatalf("badgePNG(%q, %q): %v", label, state, err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode %q: %v", state, err)
	}
	return img
}

func TestBadgePNGGeometry(t *testing.T) {
	img := decodeBadge(t, "build", "success")
	if got, want := img.Bounds().Dy(), badgeHeight*badgeScale; got != want {
		t.Errorf("height = %d, want %d", got, want)
	}
	// A longer label makes a wider badge; the pill is sized to its text.
	if decodeBadge(t, "integration", "success").Bounds().Dx() <= img.Bounds().Dx() {
		t.Error("a longer label did not widen the badge")
	}
}

func TestBadgePNGUsesTheStateColour(t *testing.T) {
	// A point inside the right-hand pill, clear of the rounded corners.
	for state, hex := range badgeColors {
		img := decodeBadge(t, "build", state)
		b := img.Bounds()
		got := color.RGBAModel.Convert(img.At(b.Max.X-4, b.Dy()/2)).(color.RGBA)
		if want := mustHex(hex); got != want {
			t.Errorf("%s: pill colour = %v, want %v", state, got, want)
		}
	}
}

// An unknown state takes the grey the SVG gives it rather than a transparent pill.
func TestBadgePNGUnknownStateFallsBack(t *testing.T) {
	img := decodeBadge(t, "build", "no-such-state")
	b := img.Bounds()
	got := color.RGBAModel.Convert(img.At(b.Max.X-4, b.Dy()/2)).(color.RGBA)
	if want := mustHex(badgeColors["unknown"]); got != want {
		t.Errorf("pill colour = %v, want %v", got, want)
	}
}

// The corners are rounded, so the very corner pixel is left transparent.
func TestBadgePNGCornersAreRounded(t *testing.T) {
	img := decodeBadge(t, "build", "success")
	if _, _, _, a := img.At(img.Bounds().Min.X, img.Bounds().Min.Y).RGBA(); a != 0 {
		t.Errorf("top-left alpha = %d, want 0", a)
	}
}

func TestMustHex(t *testing.T) {
	if got, want := mustHex("#2da44e"), (color.RGBA{R: 0x2d, G: 0xa4, B: 0x4e, A: 255}); got != want {
		t.Errorf("mustHex = %v, want %v", got, want)
	}
}
