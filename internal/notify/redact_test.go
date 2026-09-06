package notify

import "testing"

// The log names the queue row, not the person. A relay's rejection quotes
// the address it rejected, so the error text is redacted too — otherwise
// dropping the recipient field would move the leak rather than close it
// (#173).
func TestRedactAddresses(t *testing.T) {
	cases := []struct{ in, want string }{
		{"550 5.1.1 <alice@example.test>: Recipient address rejected",
			"550 5.1.1 <<address>>: Recipient address rejected"},
		{"dial tcp 10.0.0.1:587: connect: connection refused",
			"dial tcp 10.0.0.1:587: connect: connection refused"},
		{"554 alice@a.test, bob@b.test both unknown",
			"554 <address>, <address> both unknown"},
		{"x509: certificate signed by unknown authority", "x509: certificate signed by unknown authority"},
	}
	for _, c := range cases {
		if got := redactAddresses(c.in); got != c.want {
			t.Errorf("redactAddresses(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Whatever the relay says, no @ survives into the log line.
func TestRedactLeavesNoAddress(t *testing.T) {
	for _, s := range []string{
		"550 <a.b+tag@sub.example.co.uk> over quota",
		`smtp: 553 "weird name"@host.test refused`,
		"relay said: alice@example.test; bob@example.test",
	} {
		if got := redactAddresses(s); containsAddress(got) {
			t.Errorf("address survived redaction: %q -> %q", s, got)
		}
	}
}

func containsAddress(s string) bool { return addressPat.MatchString(s) }
