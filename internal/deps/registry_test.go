package deps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeRegistry serves one canned body per path and records what was asked
// for, so the test can check the URL each ecosystem builds.
func fakeRegistry(t *testing.T, bodies map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func TestLatestPerEcosystem(t *testing.T) {
	srv, asked := fakeRegistry(t, map[string]string{
		"/github.com/!burnt!sushi/toml/@latest": `{"Version":"v1.6.0"}`,
		"/@types/node":                          `{"dist-tags":{"latest":"20.11.5"}}`,
		"/serde":                                `{"crate":{"max_stable_version":"1.0.197"}}`,
		"/requests/json":                        `{"info":{"version":"2.31.0"}}`,
	})
	c := NewClient("test")
	c.Hosts = map[string]string{EcoGo: srv.URL, EcoNPM: srv.URL, EcoCargo: srv.URL, EcoPyPI: srv.URL}

	cases := []struct{ eco, name, want string }{
		{EcoGo, "github.com/BurntSushi/toml", "v1.6.0"},
		{EcoNPM, "@types/node", "20.11.5"},
		{EcoCargo, "serde", "1.0.197"},
		{EcoPyPI, "requests", "2.31.0"},
	}
	for _, c2 := range cases {
		got, err := c.Latest(context.Background(), c2.eco, c2.name)
		if err != nil {
			t.Errorf("Latest(%s, %s): %v", c2.eco, c2.name, err)
			continue
		}
		if got != c2.want {
			t.Errorf("Latest(%s, %s) = %q, want %q", c2.eco, c2.name, got, c2.want)
		}
	}
	if len(*asked) != 4 {
		t.Errorf("asked for %v", *asked)
	}
}

func TestLatestSkipsPrerelease(t *testing.T) {
	srv, _ := fakeRegistry(t, map[string]string{"/x/json": `{"info":{"version":"2.0.0rc1"}}`})
	c := NewClient("test")
	c.Hosts = map[string]string{EcoPyPI: srv.URL}
	got, err := c.Latest(context.Background(), EcoPyPI, "x")
	if err != nil || got != "" {
		t.Errorf("Latest = %q, %v; want no answer for a prerelease", got, err)
	}
}

func TestLatestRejectsUnusableNames(t *testing.T) {
	srv, asked := fakeRegistry(t, nil)
	c := NewClient("test")
	c.Hosts = map[string]string{EcoGo: srv.URL}
	for _, name := range []string{"../../etc/passwd", "a/../b", "foo?bar", "foo bar", "", "-x"} {
		if _, err := c.Latest(context.Background(), EcoGo, name); err == nil {
			t.Errorf("Latest accepted %q", name)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("unusable names reached the network: %v", *asked)
	}
}

func TestLatestReportsHTTPError(t *testing.T) {
	srv, _ := fakeRegistry(t, nil)
	c := NewClient("test")
	c.Hosts = map[string]string{EcoCargo: srv.URL}
	if _, err := c.Latest(context.Background(), EcoCargo, "missing"); err == nil {
		t.Error("Latest on a 404 returned no error")
	}
}
