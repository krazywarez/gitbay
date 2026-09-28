package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

// TestServerPathMismatches pins the commands whose CLI path differs from
// the server path they dispatch, the ones that send --path= so usage and
// help print the CLI's path (#267). A change to this list is deliberate.
func TestServerPathMismatches(t *testing.T) {
	want := []string{
		"auth email add", "auth email list", "auth email primary",
		"auth email remove", "auth email verify",
		"auth export",
		"auth keys add", "auth keys label", "auth keys list", "auth keys remove",
		"auth pgp add", "auth pgp list", "auth pgp remove",
		"auth token create", "auth token list", "auth token revoke",
		"auth whoami",
		"repo topics list",
	}

	var got []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if p := c.Annotations[serverPath]; p != "" {
			if cli := cliPathOf(c); cli != p {
				got = append(got, cli)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	root := newRoot()
	root.InitDefaultHelpCmd()
	walk(root)
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Errorf("mismatched CLI paths = %v\nwant %v", got, want)
	}
}

// TestHelpArgvSendsThePathForAnAliasGroup pins the review-round fix: a
// bare `gitbay auth --help` (cliPath == prefix == "auth") must still send
// --path=auth, since the registry has no "auth" command for withCLIPath's
// equality shortcut to correctly skip (#267 follow-up).
func TestHelpArgvSendsThePathForAnAliasGroup(t *testing.T) {
	got := helpArgv("auth", "auth")
	want := []string{"--path=auth", "help", "auth"}
	if !slices.Equal(got, want) {
		t.Errorf("helpArgv(auth, auth) = %v, want %v", got, want)
	}
}

// TestHelpArgvSendsNoPathWhenCLIMatchesTheRegistry pins the unaffected
// case: an ordinary noun's cliPath and registered prefix are the same
// string, and it is not a CLI-only alias grouping, so no --path= is sent
// and the server resolves it on its own.
func TestHelpArgvSendsNoPathWhenCLIMatchesTheRegistry(t *testing.T) {
	got := helpArgv("issue", "issue")
	want := []string{"help", "issue"}
	if !slices.Equal(got, want) {
		t.Errorf("helpArgv(issue, issue) = %v, want %v", got, want)
	}
}

// TestHelpArgvSendsThePathForAMismatchedGroup pins the ordinary
// Task 2.1 case, unchanged by the alias-group fix: a nested group whose
// cliPath differs from its registered prefix (auth keys, for keys)
// already sends --path= through withCLIPath.
func TestHelpArgvSendsThePathForAMismatchedGroup(t *testing.T) {
	got := helpArgv("keys", "auth keys")
	want := []string{"--path=auth keys", "help", "keys"}
	if !slices.Equal(got, want) {
		t.Errorf("helpArgv(keys, auth keys) = %v, want %v", got, want)
	}
}
