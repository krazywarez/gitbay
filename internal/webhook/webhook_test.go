package webhook

import (
	"net"
	"strings"
	"testing"
)

func TestCheckAddrs(t *testing.T) {
	public := []net.IP{net.ParseIP("203.0.113.5")}
	mixed := []net.IP{net.ParseIP("203.0.113.5"), net.ParseIP("10.1.2.3")}
	if err := CheckAddrs("git.example", public, false); err != nil {
		t.Fatalf("public: %v", err)
	}
	if err := CheckAddrs("git.example", mixed, false); err == nil || !strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("mixed: %v", err)
	}
	if err := CheckAddrs("git.example", mixed, true); err != nil {
		t.Fatalf("allow_local: %v", err)
	}
}

func TestIsForbiddenCGNATAndMulticast(t *testing.T) {
	for _, s := range []string{"100.64.0.1", "100.127.255.254", "224.0.0.251", "239.1.2.3", "ff02::1", "ff0e::1"} {
		if !isForbidden(net.ParseIP(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range []string{"100.63.255.255", "100.128.0.1", "203.0.113.5", "2001:db8::1"} {
		if isForbidden(net.ParseIP(s)) {
			t.Errorf("%s refused", s)
		}
	}
}
