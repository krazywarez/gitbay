package httpd

import (
	"net/http/httptest"
	"testing"

	"gitbay.org/gitbay/internal/config"
)

// With no trusted proxies the peer is the client and X-Forwarded-For is
// ignored; behind a trusted proxy the client is the last hop that is not
// itself a proxy, so a spoofed leading hop still cannot pick a bucket.
func TestClientIPBehindProxy(t *testing.T) {
	cases := []struct {
		proxies []string
		remote  string
		xff     string
		want    string
	}{
		{nil, "203.0.113.9:4000", "198.51.100.1", "203.0.113.9"},
		{[]string{"10.0.0.0/8"}, "10.1.2.3:4000", "198.51.100.1", "198.51.100.1"},
		{[]string{"10.0.0.0/8"}, "10.1.2.3:4000", "198.51.100.1, 10.9.9.9", "198.51.100.1"},
		{[]string{"10.0.0.0/8"}, "10.1.2.3:4000", "1.1.1.1, 198.51.100.1", "198.51.100.1"},
		{[]string{"10.0.0.0/8"}, "10.1.2.3:4000", "", "10.1.2.3"},
		{[]string{"10.0.0.0/8"}, "203.0.113.9:4000", "198.51.100.1", "203.0.113.9"},
		{[]string{"127.0.0.1"}, "127.0.0.1:4000", "198.51.100.1", "198.51.100.1"},
	}
	for _, tc := range cases {
		cfg := config.Default()
		cfg.HTTP.TrustedProxies = tc.proxies
		s := New(cfg, nil)
		r := httptest.NewRequest("GET", "/api/v1/read", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := s.clientIP(r); got != tc.want {
			t.Errorf("proxies=%v remote=%s xff=%q: got %s, want %s", tc.proxies, tc.remote, tc.xff, got, tc.want)
		}
	}
	cfg := config.Default()
	cfg.HTTP.TrustedProxies = []string{"not-an-address"}
	if err := cfg.Validate(); err == nil {
		t.Error("bad trusted_proxies entry accepted")
	}
}
