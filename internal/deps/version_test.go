package deps

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v1.2.3", "v1.3.0", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.3.0", "v1.2.3", false},
		{"1.2", "1.2.1", true},
		{"1.2.1", "1.2", false},
		{"0.9", "1.0", true},
		{"1.9.0", "1.10.0", true}, // numeric, not lexical
		{"2.0.0-rc1", "2.0.0", true},
		{"2.0.0", "2.0.0-rc1", false},
		{"1.2.3", "1.2.3.post1", true},
		{"1.2.3.post1", "1.2.3", false},
		{"2.0b1", "2.0", true},
		// A pseudo-version is behind any tagged release.
		{"v0.0.0-20230129092748-24d4a6f8daec", "v1.0.0", true},
		// Unreadable input reports nothing rather than guessing.
		{"", "1.0.0", false},
		{"1.0.0", "", false},
		{"main", "1.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestIsPrerelease(t *testing.T) {
	for _, v := range []string{"2.0.0-rc1", "1.0.0-beta.2", "2.0b1", "1.0.0-alpha"} {
		if !IsPrerelease(v) {
			t.Errorf("IsPrerelease(%q) = false", v)
		}
	}
	for _, v := range []string{"1.2.3", "v1.2.3", "1.2.3.post1", "10.0"} {
		if IsPrerelease(v) {
			t.Errorf("IsPrerelease(%q) = true", v)
		}
	}
}

func TestPin(t *testing.T) {
	cases := map[string]string{
		"1.2.3":        "1.2.3",
		"^1.2.3":       "1.2.3",
		"~1.2":         "1.2",
		">=2.0.0":      "2.0.0",
		"==1.4.2":      "1.4.2",
		"v1.2.3":       "1.2.3",
		"1.2.3-rc1":    "1.2.3-rc1",
		"*":            "",
		">=2,<3":       "",
		"1.x":          "",
		"^1.0 || ^2.0": "",
		"workspace:*":  "",
		"npm:foo@1.0":  "",
		"file:../lib":  "",
		"":             "",
	}
	for spec, want := range cases {
		if got := pin(spec); got != want {
			t.Errorf("pin(%q) = %q, want %q", spec, got, want)
		}
	}
}
