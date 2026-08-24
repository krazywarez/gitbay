// forge is the client CLI. It is ergonomics over a control plane that is
// fully usable from bare OpenSSH: most commands pass through to the server
// over the system ssh binary, adding instance resolution, repo inference
// from the origin remote, and $EDITOR for long text.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"gitbay.org/gitbay/internal/protocol"
)

func main() {
	root := &cobra.Command{
		Use:           "gitbay",
		Short:         "CLI-first git forge client",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		authCmd(),
		repoCmd(),
		issueCmd(),
		mrCmd(),
		webCmd(),
		remoteCmd(),
		initCmd(),
		manCmd(root),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		os.Exit(protocol.ExitUsage)
	}
}

// passOpts describes how one CLI command maps onto the server command.
type passOpts struct {
	server    []string // server-side command path
	needsRepo bool     // prepend inferred owner/name unless given
	stdinOK   bool     // wire local stdin through (keys add, --file -)
	editor    string   // open $EDITOR for a body when none given
}

// pass builds a passthrough command. Flags are parsed by the server, which
// is the single source of truth for them; the CLI stays thin.
func pass(use, short string, o passOpts) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// cobra still owns `forge <cmd> --help`.
			for _, a := range args {
				if a == "--help" || a == "-h" {
					return cmd.Help()
				}
			}
			os.Exit(runPass(o, args))
			return nil
		},
	}
}

func runPass(o passOpts, args []string) int {
	t, err := resolveTarget()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	if o.needsRepo {
		args, err = withRepo(t, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gitbay:", err)
			return protocol.ExitUsage
		}
	}

	var stdin io.Reader = strings.NewReader("")
	if o.editor != "" {
		extended, body, ok, err := maybeEditor(args, o.editor)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gitbay:", err)
			return protocol.ExitFailure
		}
		if !ok {
			return protocol.ExitFailure
		}
		args = extended
		if body != nil {
			stdin = body
		}
	}
	if stdin == nil || isEmptyReader(stdin) {
		if o.stdinOK && usesStdin(args) {
			stdin = os.Stdin
		}
	}
	return runSSH(t, append(o.server, args...), stdin)
}

func isEmptyReader(r io.Reader) bool {
	sr, ok := r.(*strings.Reader)
	return ok && sr.Len() == 0
}

// usesStdin reports whether the arguments request stdin content.
func usesStdin(args []string) bool {
	for i, a := range args {
		if a == "--file" && i+1 < len(args) && args[i+1] == "-" {
			return true
		}
	}
	return false
}

func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short}
	c.AddCommand(subs...)
	return c
}

// local wraps a locally-implemented command (git plumbing, config).
func local(use, short string, fn func(args []string) int) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args {
				if a == "--help" || a == "-h" {
					return cmd.Help()
				}
			}
			os.Exit(fn(args))
			return nil
		},
	}
}

func authCmd() *cobra.Command {
	keysAdd := pass("add", "register an SSH public key (reads the key from stdin or --file -)",
		passOpts{server: []string{"keys", "add"}, stdinOK: true})
	// keys add always reads stdin on the server; wire it through directly.
	keysAdd.RunE = func(cmd *cobra.Command, args []string) error {
		t, err := resolveTarget()
		if err != nil {
			return err
		}
		os.Exit(runSSH(t, append([]string{"keys", "add"}, args...), os.Stdin))
		return nil
	}
	pgpAdd := &cobra.Command{
		Use: "add", Short: "register an OpenPGP public key (armored, on stdin)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget()
			if err != nil {
				return err
			}
			os.Exit(runSSH(t, append([]string{"pgp", "add"}, args...), os.Stdin))
			return nil
		},
	}
	return group("auth", "identity: whoami, SSH and PGP keys",
		pass("whoami", "show the authenticated account", passOpts{server: []string{"whoami"}}),
		group("keys", "manage SSH keys",
			pass("list", "list registered SSH keys", passOpts{server: []string{"keys", "list"}}),
			keysAdd,
			pass("remove", "remove an SSH key by fingerprint", passOpts{server: []string{"keys", "remove"}}),
		),
		group("pgp", "manage OpenPGP keys",
			pass("list", "list registered PGP keys", passOpts{server: []string{"pgp", "list"}}),
			pgpAdd,
			pass("remove", "remove a PGP key by fingerprint", passOpts{server: []string{"pgp", "remove"}}),
		),
	)
}

func repoCmd() *cobra.Command {
	return group("repo", "create and manage repositories",
		pass("create", "create a repository: gitbay repo create <owner/name> [--private]",
			passOpts{server: []string{"repo", "create"}}),
		pass("list", "list repositories you own or can access", passOpts{server: []string{"repo", "list"}}),
		pass("show", "show repository details", passOpts{server: []string{"repo", "show"}, needsRepo: true}),
		pass("log", "commit log with signature states", passOpts{server: []string{"repo", "log"}, needsRepo: true}),
		pass("delete", "delete a repository (--yes)", passOpts{server: []string{"repo", "delete"}, needsRepo: true}),
		pass("fork", "fork a repository under your account", passOpts{server: []string{"repo", "fork"}, needsRepo: true}),
		local("clone", "clone via ssh: gitbay repo clone <owner/name> [dir]", cmdRepoClone),
		importCmd(),
		group("access", "manage access grants",
			pass("grant", "grant access: ... <user> read|write|admin", passOpts{server: []string{"repo", "access", "grant"}, needsRepo: true}),
			pass("revoke", "revoke access: ... <user>", passOpts{server: []string{"repo", "access", "revoke"}, needsRepo: true}),
			pass("list", "list access grants", passOpts{server: []string{"repo", "access", "list"}, needsRepo: true}),
		),
		group("settings", "repository settings",
			pass("show", "show settings", passOpts{server: []string{"repo", "settings", "show"}, needsRepo: true}),
			pass("protect", "protect a branch", passOpts{server: []string{"repo", "settings", "protect"}, needsRepo: true}),
			pass("unprotect", "unprotect a branch", passOpts{server: []string{"repo", "settings", "unprotect"}, needsRepo: true}),
			pass("require-signed", "require verified commit signatures: ... on|off", passOpts{server: []string{"repo", "settings", "require-signed"}, needsRepo: true}),
			pass("git-daemon", "expose over git://: ... on|off", passOpts{server: []string{"repo", "settings", "git-daemon"}, needsRepo: true}),
		),
	)
}

func issueCmd() *cobra.Command {
	return group("issue", "issues",
		pass("create", "open an issue: --title <t> [--body|--file -|$EDITOR]",
			passOpts{server: []string{"issue", "create"}, needsRepo: true, stdinOK: true, editor: "issue"}),
		pass("list", "list issues [--state open|closed|all]", passOpts{server: []string{"issue", "list"}, needsRepo: true}),
		pass("show", "show an issue with comments", passOpts{server: []string{"issue", "show"}, needsRepo: true}),
		pass("comment", "comment on an issue [--message|--file -|$EDITOR]",
			passOpts{server: []string{"issue", "comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("close", "close an issue", passOpts{server: []string{"issue", "close"}, needsRepo: true}),
		pass("reopen", "reopen an issue", passOpts{server: []string{"issue", "reopen"}, needsRepo: true}),
		pass("label", "add or remove labels: [--add <l>]... [--remove <l>]...", passOpts{server: []string{"issue", "label"}, needsRepo: true}),
		pass("assign", "assign users: [--add <u>]... [--remove <u>]...", passOpts{server: []string{"issue", "assign"}, needsRepo: true}),
	)
}

func mrCmd() *cobra.Command {
	return group("mr", "merge requests",
		pass("create", "open a merge request: --source <branch> --target <branch> --title <t>",
			passOpts{server: []string{"mr", "create"}, needsRepo: true, stdinOK: true, editor: "merge request"}),
		pass("list", "list merge requests [--state ...]", passOpts{server: []string{"mr", "list"}, needsRepo: true}),
		pass("show", "show a merge request", passOpts{server: []string{"mr", "show"}, needsRepo: true}),
		pass("diff", "show the diff", passOpts{server: []string{"mr", "diff"}, needsRepo: true}),
		local("checkout", "fetch and check out the MR head locally: gitbay mr checkout <n>", cmdMRCheckout),
		pass("comment", "comment on a merge request", passOpts{server: []string{"mr", "comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("review", "review: --approve|--request-changes|--comment", passOpts{server: []string{"mr", "review"}, needsRepo: true}),
		pass("merge", "merge (fast-forward or merge-commit): [--strategy ff|merge]", passOpts{server: []string{"mr", "merge"}, needsRepo: true}),
		pass("close", "close without merging", passOpts{server: []string{"mr", "close"}, needsRepo: true}),
	)
}

// importCmd passes repo import through with stdin wired for --token-stdin.
func importCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "import",
		Short:              "server-side mirror of a foreign repo: gitbay repo import <owner/name> --from <url> [--private] [--token-stdin]",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args {
				if a == "--help" || a == "-h" {
					return cmd.Help()
				}
			}
			t, err := resolveTarget()
			if err != nil {
				return err
			}
			var stdin io.Reader = strings.NewReader("")
			if usesTokenStdin(args) {
				stdin = os.Stdin
			}
			os.Exit(runSSH(t, append([]string{"repo", "import"}, args...), stdin))
			return nil
		},
	}
}

func usesTokenStdin(args []string) bool {
	for _, a := range args {
		if a == "--token-stdin" {
			return true
		}
	}
	return false
}

func webCmd() *cobra.Command {
	return group("web", "browser session",
		pass("login", "mint a one-time browser login URL over ssh", passOpts{server: []string{"web", "login"}}),
	)
}

func remoteCmd() *cobra.Command {
	return group("remote", "local instance profiles (no server contact)",
		local("add", "add a named gitbay instance: gitbay remote add <name> <host> [--port n] [--user u] [--ssh-option o]... [--default]",
			cmdRemoteAdd),
		local("list", "list configured instances", func([]string) int { return cmdRemoteList() }),
	)
}

func initCmd() *cobra.Command {
	return local("init", "git init + repo create + set origin, in one step: gitbay init [name] [--private]", cmdInit)
}

// manCmd generates man pages; a CLI-first tool without man pages is not
// CLI-first.
func manCmd(root *cobra.Command) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:    "man",
		Short:  "generate man pages into a directory",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return doc.GenManTree(root, &doc.GenManHeader{Title: "FORGE", Section: "1"}, dir)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "man", "output directory")
	return cmd
}
