package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// A command whose payload is stdin blocks on a terminal with nothing
// printed, which is indistinguishable from a hung connection — and
// pressing Enter does not end it, because the server reads to EOF. So it
// looks the same before and after you have done the right thing (#150).
//
// The fix belongs here rather than in the daemon: whether stdin is a
// terminal is a fact about the client, and the server cannot see it.

// isTerminal is a variable so tests can drive both paths; a test process
// has no terminal, which is the case that must stay byte-identical.
var isTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// stdinPayload returns the reader for a command that takes its payload on
// stdin. Piped or redirected input is passed through untouched — no
// prompt, no added or removed bytes — because a script's `printf %s
// "$TOKEN" | gitbay ...` must send exactly the token.
//
// what names the thing being read, for the prompt. secret hides the input
// and takes a single line, so a credential never lands in the terminal's
// scrollback and Enter is enough to finish.
func stdinPayload(in *os.File, what string, secret bool) (io.Reader, error) {
	if !isTerminal(in) {
		return in, nil
	}
	if secret {
		fmt.Fprintf(os.Stderr, "%s (input hidden, Enter when done): ", what)
		raw, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", what, err)
		}
		return bytes.NewReader(raw), nil
	}
	fmt.Fprintf(os.Stderr, "reading %s from stdin — paste it, then press Ctrl-D. (Or pipe it in.)\n", what)
	return in, nil
}
