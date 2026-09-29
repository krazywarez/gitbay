package httpd

import "testing"

func TestLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/settings":       true,
		"/a/b?c=d":        true,
		"":                false,
		"settings":        false,
		"//evil.example":  false,
		`/\evil.example`:  false,
		"https://x.test/": false,
	} {
		if got := localPath(p); got != want {
			t.Errorf("localPath(%q) = %v, want %v", p, got, want)
		}
	}
}
