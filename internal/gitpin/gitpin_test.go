package gitpin

import (
	"context"
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func answer(ips ...string) Lookup {
	return func(context.Context, string) ([]net.IP, error) {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	r, err := Resolve(ctx, answer("203.0.113.5"), "https://git.example/x.git", false)
	if err != nil || r.URL.Hostname() != "git.example" || len(r.IPs) != 1 {
		t.Fatalf("public: %+v %v", r, err)
	}
	if _, err := Resolve(ctx, answer("203.0.113.5", "10.0.0.7"), "https://git.example/x.git", false); err == nil || !strings.Contains(err.Error(), "10.0.0.7") {
		t.Fatalf("private: %v", err)
	}
	if _, err := Resolve(ctx, answer("10.0.0.7"), "https://git.example/x.git", true); err != nil {
		t.Fatalf("allow_local: %v", err)
	}
	// An empty resolve list would leave curl to resolve the host itself.
	if _, err := Resolve(ctx, answer(), "https://git.example/x.git", true); err == nil || !strings.Contains(err.Error(), "no address") {
		t.Fatalf("empty answer: %v", err)
	}
	for _, raw := range []string{"git://git.example/x.git", "ssh://git.example/x.git", "file:///etc"} {
		_, err := Resolve(ctx, func(context.Context, string) ([]net.IP, error) {
			t.Fatalf("looked up a host for %s", raw)
			return nil, nil
		}, raw, true)
		if err == nil || !strings.Contains(err.Error(), "not http or https") {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestArgs(t *testing.T) {
	u, _ := url.Parse("https://git.example/x.git")
	got := Remote{u, []net.IP{net.ParseIP("203.0.113.5"), net.ParseIP("2001:db8::1")}}.Args()
	want := []string{"-c", "http.followRedirects=false",
		"-c", "http.curloptResolve=git.example:443:203.0.113.5,[2001:db8::1]"}
	if !slices.Equal(got, want) {
		t.Fatalf("https: %q", got)
	}
	u, _ = url.Parse("http://git.example:8080/x.git")
	if got := (Remote{u, []net.IP{net.ParseIP("203.0.113.5")}}).Args(); got[3] != "http.curloptResolve=git.example:8080:203.0.113.5" {
		t.Fatalf("http with port: %q", got)
	}
	// An address literal is its own resolution; there is nothing to pin.
	u, _ = url.Parse("https://203.0.113.5/x.git")
	if got := (Remote{u, []net.IP{net.ParseIP("203.0.113.5")}}).Args(); !slices.Equal(got, []string{"-c", "http.followRedirects=false"}) {
		t.Fatalf("literal: %q", got)
	}
}

func TestEnv(t *testing.T) {
	want := []string{"GIT_TERMINAL_PROMPT=0", "HOME=/srv/gitbay",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if got := Env("/srv/gitbay"); !slices.Equal(got, want) {
		t.Fatalf("Env = %q", got)
	}
}

func TestVersionOK(t *testing.T) {
	for _, s := range []string{"git version 2.37.0", "git version 2.47.3", "git version 2.39.5 (Apple Git-154)",
		"git version 2.45.2.windows.1", "git version 3.0.0\n"} {
		if err := VersionOK(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"git version 2.36.9", "git version 1.99.0", "git version 2", "nonsense", ""} {
		if err := VersionOK(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}
