package main

import (
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The piped path must stay byte-identical: a script doing
// `printf %s "$TOKEN" | gitbay repo secret set ...` has to send exactly
// the token, with no prompt written and no byte added or removed. This is
// the half that can silently corrupt a credential, so it is the half with
// a test.
func TestStdinPayloadPipedIsUntouched(t *testing.T) {
	defer swapTerminal(func(*os.File) bool { return false })()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	const token = "sqp_0123456789abcdef"
	go func() { io.WriteString(w, token); w.Close() }()

	got, err := stdinPayload(r, "the secret value", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != io.Reader(r) {
		t.Fatal("piped stdin was replaced rather than passed through")
	}
	b, _ := io.ReadAll(got)
	if string(b) != token {
		t.Fatalf("piped payload = %q, want %q", b, token)
	}
}

// A non-terminal stdin must not be prompted at, in either mode. The
// prompt goes to stderr, so a test watching stdout would miss this.
func TestStdinPayloadPipedIsSilent(t *testing.T) {
	defer swapTerminal(func(*os.File) bool { return false })()

	prev := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	for _, secret := range []bool{false, true} {
		if _, err := stdinPayload(os.Stdin, "the secret value", secret); err != nil {
			os.Stderr = prev
			t.Fatalf("secret=%v: %v", secret, err)
		}
	}
	w.Close()
	os.Stderr = prev
	out, _ := io.ReadAll(r)
	if len(out) != 0 {
		t.Fatalf("piped stdin was prompted at: %q", out)
	}
}

// On a terminal the non-secret path says what it wants and how to end it.
// Someone who does not know Ctrl-D is the person this exists for.
func TestStdinPayloadTerminalExplainsItself(t *testing.T) {
	defer swapTerminal(func(*os.File) bool { return true })()

	prev := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	_, err = stdinPayload(os.Stdin, "an SSH public key", false)
	w.Close()
	os.Stderr = prev
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	got := string(out)
	for _, want := range []string{"an SSH public key", "Ctrl-D"} {
		if !strings.Contains(got, want) {
			t.Errorf("terminal prompt %q lacks %q", got, want)
		}
	}
}

// Every command whose payload is stdin names it, or the prompt above says
// the unhelpful word "input".
func TestStdinCommandsNameTheirPayload(t *testing.T) {
	var missing []string
	var walk func(*cobra.Command, string)
	walk = func(c *cobra.Command, path string) {
		if c.Annotations[stdinMode] == "always" && c.Annotations[stdinWhat] == "" {
			missing = append(missing, strings.TrimSpace(path))
		}
		for _, sub := range c.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(newRoot(), "")
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("stdin-payload commands with no stdinWhat: %s", strings.Join(missing, ", "))
	}
}

func swapTerminal(f func(*os.File) bool) func() {
	prev := isTerminal
	isTerminal = f
	return func() { isTerminal = prev }
}
