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
