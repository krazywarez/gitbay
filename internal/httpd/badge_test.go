package httpd

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestBadgeSVGEscapesLabel(t *testing.T) {
	label := `a<b&c"d`
	out := badgeSVG(label, "success")
	if strings.Contains(out, label) {
		t.Fatalf("label is unescaped:\n%s", out)
	}
	dec := xml.NewDecoder(strings.NewReader(out))
	var texts []string
	var aria string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("badge is not well-formed: %v\n%s", err, out)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if tok.Name.Local == "svg" {
				for _, a := range tok.Attr {
					if a.Name.Local == "aria-label" {
						aria = a.Value
					}
				}
			}
		case xml.CharData:
			if s := strings.TrimSpace(string(tok)); s != "" {
				texts = append(texts, s)
			}
		}
	}
	if want := label + ": success"; aria != want {
		t.Errorf("aria-label = %q, want %q", aria, want)
	}
	if want := []string{label + ": success", label, "success"}; strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Errorf("text = %q, want %q", texts, want)
	}
}
