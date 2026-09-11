package control

import "testing"

func TestKeyLabel(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"  laptop  ", "laptop", true},
		{"you@machine", "you@machine", true},
		{"two\nlines", "", false},
		{"tab\there", "", false},
		{string(make([]byte, 65)), "", false},
	}
	for _, c := range cases {
		got, err := keyLabel(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("keyLabel(%q) = %q, %v; want %q, ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}
