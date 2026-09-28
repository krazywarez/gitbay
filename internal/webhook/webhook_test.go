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
