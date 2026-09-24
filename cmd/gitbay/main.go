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
	"golang.org/x/term"

	"gitbay.org/gitbay/internal/protocol"
)

func main() {
	os.Args, noColor = stripNoColor(os.Args)
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		os.Exit(protocol.ExitUsage)
	}
}

// newRoot builds the command tree. Separate from main so the coverage
// test can walk it.
func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "gitbay",
		Short:         "CLI-first git forge client",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetHelpCommand(helpCmd(root))

	root.AddCommand(
		authCmd(),
		group("label", "issue labels",
			pass("list", passOpts{server: []string{"label", "list"}, needsRepo: true}),
			pass("set", passOpts{server: []string{"label", "set"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"label", "remove"}, needsRepo: true}),
		),
		group("status", "commit statuses (CI)",
			pass("set", passOpts{server: []string{"status", "set"}, needsRepo: true}),
			pass("list", passOpts{server: []string{"status", "list"}, needsRepo: true}),
		),
		group("build", "CI builds",
			pass("list", passOpts{server: []string{"build", "list"}, needsRepo: true}),
			pass("show", passOpts{server: []string{"build", "show"}, needsRepo: true}),
			pass("log", passOpts{server: []string{"build", "log"}, needsRepo: true}),
			pass("jobs", passOpts{server: []string{"build", "jobs"}, needsRepo: true}),
			pass("trigger", passOpts{server: []string{"build", "trigger"}, needsRepo: true}),
			pass("cancel", passOpts{server: []string{"build", "cancel"}, needsRepo: true}),
		),
		withShort(pass("dashboard", passOpts{server: []string{"dashboard"}}), "pinned repos, open MRs, assigned issues, recent builds"),
		pass("feed", passOpts{server: []string{"feed"}}),
		withShort(pass("explore", passOpts{server: []string{"explore"}}), "public repositories on this instance"),
		withShort(pass("search", passOpts{server: []string{"search"}}), "find repositories, issues and merge requests"),
		group("notifications", "your notification inbox",
			pass("list", passOpts{server: []string{"notifications", "list"}}),
			pass("read", passOpts{server: []string{"notifications", "read"}}),
			group("settings", "notification preferences",
				pass("show", passOpts{server: []string{"notifications", "settings", "show"}}),
				pass("mail", passOpts{server: []string{"notifications", "settings", "mail"}}),
				pass("watch", passOpts{server: []string{"notifications", "settings", "watch"}}),
				pass("push", passOpts{server: []string{"notifications", "settings", "push"}}),
			),
			group("device", "Apple devices registered for push",
				pass("add", passOpts{server: []string{"notifications", "device", "add"}, alwaysStdin: true, stdinWhat: "the device token"}),
				pass("list", passOpts{server: []string{"notifications", "device", "list"}}),
				pass("remove", passOpts{server: []string{"notifications", "device", "remove"}}),
			),
		),
		group("wiki", "a repository's wiki pages",
			pass("list", passOpts{server: []string{"wiki", "list"}, needsRepo: true}),
			pass("show", passOpts{server: []string{"wiki", "show"}, needsRepo: true}),
		),
		group("snippet", "shared text files, outside any repository",
			pass("create", passOpts{server: []string{"snippet", "create"}, alwaysStdin: true, stdinWhat: "the file's text"}),
			pass("show", passOpts{server: []string{"snippet", "show"}}),
			pass("list", passOpts{server: []string{"snippet", "list"}}),
			pass("edit", passOpts{server: []string{"snippet", "edit"}}),
			pass("delete", passOpts{server: []string{"snippet", "delete"}}),
			group("file", "the files in a snippet",
				pass("set", passOpts{server: []string{"snippet", "file", "set"}, alwaysStdin: true, stdinWhat: "the file's text"}),
				pass("get", passOpts{server: []string{"snippet", "file", "get"}}),
				pass("remove", passOpts{server: []string{"snippet", "file", "remove"}}),
			),
		),
		repoCmd(),
		issueCmd(),
		milestoneCmd(),
		mrCmd(),
		releaseCmd(),
		migrateCmd(),
		webCmd(),
		orgCmd(),
		group("profile", "user and org profiles",
			pass("show", passOpts{server: []string{"profile", "show"}}),
			pass("set", passOpts{server: []string{"profile", "set"}}),
		),
		webhookCmd(),
		remoteCmd(),
		initCmd(),
		withShort(pass("register", passOpts{server: []string{"register"}}), "create an account on this instance"),
		pass("audit", passOpts{server: []string{"audit"}}),
		group("admin", "instance administration (admins)",
			group("user", "accounts on this instance",
				pass("list", passOpts{server: []string{"admin", "user", "list"}}),
				pass("show", passOpts{server: []string{"admin", "user", "show"}}),
				pass("promote", passOpts{server: []string{"admin", "user", "promote"}}),
				pass("demote", passOpts{server: []string{"admin", "user", "demote"}}),
				pass("create", passOpts{server: []string{"admin", "user", "create"}, stdinOK: true}),
				pass("disable", passOpts{server: []string{"admin", "user", "disable"}}),
				pass("enable", passOpts{server: []string{"admin", "user", "enable"}}),
				pass("delete", passOpts{server: []string{"admin", "user", "delete"}}),
				pass("limits", passOpts{server: []string{"admin", "user", "limits"}}),
			),
			group("email", "addresses on any account",
				pass("verify", passOpts{server: []string{"admin", "email", "verify"}}),
			),
			pass("invite", passOpts{server: []string{"admin", "invite"}}),
			pass("stats", passOpts{server: []string{"admin", "stats"}}),
			withSub(pass("runners", passOpts{server: []string{"admin", "runners"}}),
				pass("remove", passOpts{server: []string{"admin", "runners", "remove"}}),
				pass("forget", passOpts{server: []string{"admin", "runners", "forget"}})),
			group("repo", "any repository, for moderation (audited)",
				pass("list", passOpts{server: []string{"admin", "repo", "list"}}),
				pass("archive", passOpts{server: []string{"admin", "repo", "archive"}}),
				pass("unarchive", passOpts{server: []string{"admin", "repo", "unarchive"}}),
				pass("visibility", passOpts{server: []string{"admin", "repo", "visibility"}}),
				pass("delete", passOpts{server: []string{"admin", "repo", "delete"}}),
			),
			group("mr", "merge requests in any repository (audited)",
				pass("prune", passOpts{server: []string{"admin", "mr", "prune"}}),
			),
		),
		manCmd(root),
	)
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd == root {
			rootHelp(root)
			return
		}
		defaultHelp(cmd, args)
	})
	return root
}

// rootSection is one heading in gitbay --help: a title and the root
// commands that belong under it.
type rootSection struct {
	title string
	names []string
}

var rootSections = []rootSection{
	{"WORK", []string{"issue", "mr", "build", "release", "milestone", "label", "search"}},
	{"REPOSITORIES", []string{"repo", "wiki", "status", "webhook", "init"}},
	{"YOU", []string{"dashboard", "feed", "notifications", "auth", "profile", "snippet", "web"}},
	{"INSTANCE", []string{"org", "explore", "register", "migrate", "remote", "admin", "audit", "man"}},
}

// rootHelp is gitbay --help: the nouns grouped by what they are for.
func rootHelp(root *cobra.Command) {
	byName := map[string]*cobra.Command{}
	wide := 0
	for _, c := range root.Commands() {
		byName[c.Name()] = c
		if !c.Hidden {
			wide = max(wide, len(c.Name()))
		}
	}
	fmt.Println("gitbay: command-line client for a gitbay forge")
	fmt.Println()
	fmt.Println("USAGE")
	fmt.Println("  gitbay <command> [<owner/name>] [flags]")
	for _, s := range rootSections {
		var rows [][2]string
		for _, n := range s.names {
			if c := byName[n]; c != nil && !c.Hidden {
				rows = append(rows, [2]string{n, c.Short})
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Println()
		fmt.Println(s.title)
		for _, r := range rows {
			fmt.Printf("  %-*s  %s\n", wide, r[0], r[1])
		}
	}
	fmt.Println()
	fmt.Println("gitbay <command> --help for its verbs; gitbay help <prefix> for the server reference.")
}

// serverPath is the annotation key holding a passthrough command's
// server-side path, so the tree can be checked against the registry.
const serverPath = "gitbay.server_path"
const stdinMode = "gitbay.stdin_mode"

// stdinWhat carries the payload's name onto the command tree so the
// coverage test can assert every stdin-payload command has one; without
// it the terminal prompt says the unhelpful word "input" (#150).
const stdinWhat = "gitbay.stdin_what"

// passOpts describes how one CLI command maps onto the server command.
type passOpts struct {
	server      []string // server-side command path
	needsRepo   bool     // prepend inferred owner/name unless given
	stdinOK     bool     // wire local stdin through when --file - asks for it
	alwaysStdin bool     // stdin is the payload, named by no flag: a bare redirect
	// stdinWhat names the payload for the prompt shown when stdin is a
	// terminal; stdinSecret hides the input and takes one line, for a
	// value that should not reach the scrollback.
	stdinWhat   string
	stdinSecret bool
	editor      string // open $EDITOR for a body when none given
	inferSource bool   // --source defaults to the checked-out branch inside a clone
}

// pass builds a passthrough command. Flags are parsed by the server, which
// is the single source of truth for them; the CLI stays thin.
// stdinModeName reports how this command takes stdin, so the coverage test can
// check that a command reading a bare redirect is not left waiting for a
// `--file -` that its callers never type.
func (o passOpts) stdinModeName() string {
	switch {
	case o.alwaysStdin:
		return "always"
	case o.stdinOK:
		return "flag"
	}
	return "none"
}

func pass(use string, o passOpts) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: summaries[strings.Join(o.server, " ")],
		Annotations: map[string]string{
			serverPath: strings.Join(o.server, " "),
			stdinMode:  o.stdinModeName(),
			stdinWhat:  o.stdinWhat,
		},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The registry is the only place flags are written down, so
			// --help asks the server rather than reprinting the one-line
			// summary cobra holds.
			for _, a := range args {
				if a == "--help" || a == "-h" {
					os.Exit(runServerHelp(o))
				}
			}
			os.Exit(runPass(o, args))
			return nil
		},
	}
}

// withShort overrides a passthrough command's one-line summary. Used
// where the registry's own Summary for a single-verb noun (dashboard,
// explore, search, register) reads differently from nounSummaries, which
// is what a grouped noun's header and the root help both show; the two
// must agree (TestGroupsSayWhatTheServerSays), so these few take the
// noun's wording instead of the command's.
func withShort(cmd *cobra.Command, short string) *cobra.Command {
	cmd.Short = short
	return cmd
}

// runServerHelp prints the registry's usage for one command.
func runServerHelp(o passOpts) int {
	t, err := resolveTarget()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	return runSSH(t, append([]string{"help"}, o.server...), strings.NewReader(""))
}

func runPass(o passOpts, args []string) int {
	t, err := resolveTarget()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitbay:", err)
		return protocol.ExitFailure
	}
	explicitRepo := len(args) > 0 && !strings.HasPrefix(args[0], "-") && strings.Contains(args[0], "/")
	if o.needsRepo {
		args, err = withRepo(t, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gitbay:", err)
			return protocol.ExitUsage
		}
	}
	// Inside a clone, the branch you are on is the one you mean (#101).
	// Only when the repository was inferred from the clone too: naming
	// another repository and meaning this checkout's branch is unlikely.
	if o.inferSource && !explicitRepo && !hasFlag(args, "--source") {
		if branch := currentBranch(); branch != "" {
			args = append(args, "--source", branch)
		}
	}

	var stdin io.Reader = strings.NewReader("")
	if o.editor != "" {
		// Issue bodies prefill from the repo's .gitbay/issue-template*.md.
		var prefill func() string
		if o.editor == "issue" && len(args) > 0 && strings.Contains(args[0], "/") {
			repoPath := args[0]
			prefill = func() string { return fetchIssueTemplate(t, repoPath) }
		}
		extended, body, ok, err := maybeEditor(args, o.editor, prefill)
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
		if o.alwaysStdin || (o.stdinOK && usesStdin(args)) {
			what := o.stdinWhat
			if what == "" {
				what = "input"
			}
			r, err := stdinPayload(os.Stdin, what, o.stdinSecret)
			if err != nil {
				fmt.Fprintln(os.Stderr, "gitbay:", err)
				return protocol.ExitFailure
			}
			stdin = r
		}
	}
	return runSSHPaged(t, append(o.server, args...), stdin, pages(o.server, args))
}

func isEmptyReader(r io.Reader) bool {
	sr, ok := r.(*strings.Reader)
	return ok && sr.Len() == 0
}

// usesStdin reports whether the arguments request stdin content.
func usesStdin(args []string) bool {
	for i, a := range args {
		if (a == "--file" || a == "--key") && i+1 < len(args) && args[i+1] == "-" {
			return true
		}
		if a == "--token-stdin" {
			return true
		}
	}
	return false
}

// withSub hangs subcommands off a passthrough command, so `admin runners`
// still runs while `admin runners forget` reaches its own command.
func withSub(cmd *cobra.Command, subs ...*cobra.Command) *cobra.Command {
	cmd.AddCommand(subs...)
	return cmd
}

func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short}
	c.AddCommand(subs...)
	// A noun's help is the server's, like a command's: the registry is
	// the only place flags are written down, and cobra's subcommand list
	// carried none (#130). Offline, or for a noun the server does not
	// know by that name, cobra's own tree still prints.
	local := c.HelpFunc()
	c.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !serverHelp(use) {
			local(cmd, args)
		}
	})
	return c
}

// serverHelp prints the registry's usage for a prefix and reports whether
// it did. At a terminal it goes through the terminal-aware path, so it
// gets the same --term=<cols>[,color] treatment (and layout) as any other
// command; piped, it stays a quiet capture, so a network or lookup
// failure falls back to cobra's local help without noise.
func serverHelp(prefix string) bool {
	t, err := resolveTarget()
	if err != nil {
		return false
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return runSSH(t, []string{"help", prefix}, strings.NewReader("")) == 0
	}
	out, code := sshCapture(t, []string{"help", prefix})
	if code != 0 || out == "" {
		return false
	}
	fmt.Print(out)
	return true
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
	keysAdd := pass("add", passOpts{server: []string{"keys", "add"}, alwaysStdin: true, stdinWhat: "an SSH public key"})
	// keys add always reads stdin on the server; wire it through directly.
	keysAdd.RunE = func(cmd *cobra.Command, args []string) error {
		t, err := resolveTarget()
		if err != nil {
			return err
		}
		in, err := stdinPayload(os.Stdin, "an SSH public key", false)
		if err != nil {
			return err
		}
		os.Exit(runSSH(t, append([]string{"keys", "add"}, args...), in))
		return nil
	}
	pgpAdd := &cobra.Command{
		Use: "add", Short: "register an OpenPGP public key (armored, on stdin)",
		Annotations: map[string]string{
			serverPath: "pgp add",
			stdinMode:  "always",
			stdinWhat:  "an armored OpenPGP public key",
		},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget()
			if err != nil {
				return err
			}
			in, err := stdinPayload(os.Stdin, "an armored OpenPGP public key", false)
			if err != nil {
				return err
			}
			os.Exit(runSSH(t, append([]string{"pgp", "add"}, args...), in))
			return nil
		},
	}
	tokens := group("token", "API tokens (minted over SSH, used with the JSON API)",
		pass("create", passOpts{server: []string{"token", "create"}}),
		pass("list", passOpts{server: []string{"token", "list"}}),
		pass("revoke", passOpts{server: []string{"token", "revoke"}}),
	)
	return group("auth", "identity: whoami, SSH and PGP keys",
		pass("export", passOpts{server: []string{"account", "export"}}),
		tokens,
		pass("whoami", passOpts{server: []string{"whoami"}}),
		group("keys", "manage SSH keys",
			pass("list", passOpts{server: []string{"keys", "list"}}),
			keysAdd,
			pass("label", passOpts{server: []string{"keys", "label"}}),
			pass("remove", passOpts{server: []string{"keys", "remove"}}),
		),
		group("email", "manage email addresses",
			pass("add", passOpts{server: []string{"email", "add"}}),
			pass("verify", passOpts{server: []string{"email", "verify"}}),
			pass("list", passOpts{server: []string{"email", "list"}}),
			pass("remove", passOpts{server: []string{"email", "remove"}}),
			pass("primary", passOpts{server: []string{"email", "primary"}}),
		),
		group("pgp", "manage OpenPGP keys",
			pass("list", passOpts{server: []string{"pgp", "list"}}),
			pgpAdd,
			pass("remove", passOpts{server: []string{"pgp", "remove"}}),
		),
	)
}

func repoCmd() *cobra.Command {
	return group("repo", "create and manage repositories",
		pass("create", passOpts{server: []string{"repo", "create"}}),
		pass("list", passOpts{server: []string{"repo", "list"}}),
		pass("show", passOpts{server: []string{"repo", "show"}, needsRepo: true}),
		pass("log", passOpts{server: []string{"repo", "log"}, needsRepo: true}),
		pass("transfer", passOpts{server: []string{"repo", "transfer"}, needsRepo: true}),
		pass("rename", passOpts{server: []string{"repo", "rename"}, needsRepo: true}),
		pass("delete", passOpts{server: []string{"repo", "delete"}, needsRepo: true}),
		pass("fork", passOpts{server: []string{"repo", "fork"}, needsRepo: true}),
		pass("search", passOpts{server: []string{"repo", "search"}}),
		pass("grep", passOpts{server: []string{"repo", "grep"}, needsRepo: true}),
		pass("diff", passOpts{server: []string{"repo", "diff"}, needsRepo: true}),
		pass("tree", passOpts{server: []string{"repo", "tree"}, needsRepo: true}),
		pass("cat", passOpts{server: []string{"repo", "cat"}, needsRepo: true}),
		pass("blame", passOpts{server: []string{"repo", "blame"}, needsRepo: true}),
		pass("commit", passOpts{server: []string{"repo", "commit"}, needsRepo: true}),
		pass("commit-file", passOpts{server: []string{"repo", "commit-file"}, needsRepo: true, stdinOK: true}),
		pass("refs", passOpts{server: []string{"repo", "refs"}, needsRepo: true}),
		pass("download", passOpts{server: []string{"repo", "download"}, needsRepo: true}),
		pass("pin", passOpts{server: []string{"repo", "pin"}, needsRepo: true}),
		pass("unpin", passOpts{server: []string{"repo", "unpin"}, needsRepo: true}),
		pass("bookmark", passOpts{server: []string{"repo", "bookmark"}, needsRepo: true}),
		pass("unbookmark", passOpts{server: []string{"repo", "unbookmark"}, needsRepo: true}),
		pass("bookmarks", passOpts{server: []string{"repo", "bookmarks"}}),
		pass("watch", passOpts{server: []string{"repo", "watch"}, needsRepo: true}),
		pass("unwatch", passOpts{server: []string{"repo", "unwatch"}, needsRepo: true}),
		pass("mute", passOpts{server: []string{"repo", "mute"}, needsRepo: true}),
		pass("archive", passOpts{server: []string{"repo", "archive"}, needsRepo: true}),
		pass("unarchive", passOpts{server: []string{"repo", "unarchive"}, needsRepo: true}),
		local("clone", "clone via ssh: gitbay repo clone <owner/name> [dir]", cmdRepoClone),
		importCmd(),
		pass("import-issues", passOpts{server: []string{"repo", "import-issues"}, needsRepo: true, stdinOK: true}),
		group("deploy-key", "repository-bound CI keys",
			pass("add", passOpts{server: []string{"repo", "deploy-key", "add"}, needsRepo: true, alwaysStdin: true, stdinWhat: "an SSH public key"}),
			pass("list", passOpts{server: []string{"repo", "deploy-key", "list"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"repo", "deploy-key", "remove"}, needsRepo: true}),
		),
		group("runner", "runners attached to a repository",
			pass("add", passOpts{server: []string{"repo", "runner", "add"}, needsRepo: true, alwaysStdin: true, stdinWhat: "an SSH public key"}),
			pass("list", passOpts{server: []string{"repo", "runner", "list"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"repo", "runner", "remove"}, needsRepo: true}),
		),
		group("mirror", "sync with a foreign remote",
			pass("add", passOpts{server: []string{"repo", "mirror", "add"}, needsRepo: true, stdinOK: true}),
			pass("list", passOpts{server: []string{"repo", "mirror", "list"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"repo", "mirror", "remove"}, needsRepo: true}),
			pass("sync", passOpts{server: []string{"repo", "mirror", "sync"}, needsRepo: true}),
		),
		group("deps", "check dependencies against upstream registries",
			pass("enable", passOpts{server: []string{"repo", "deps", "enable"}, needsRepo: true}),
			pass("disable", passOpts{server: []string{"repo", "deps", "disable"}, needsRepo: true}),
			pass("status", passOpts{server: []string{"repo", "deps", "status"}, needsRepo: true}),
		),
		group("secret", "build secrets (values on stdin, injected into build env)",
			pass("set", passOpts{server: []string{"repo", "secret", "set"}, needsRepo: true, alwaysStdin: true, stdinWhat: "the secret value", stdinSecret: true}),
			pass("list", passOpts{server: []string{"repo", "secret", "list"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"repo", "secret", "remove"}, needsRepo: true}),
		),
		group("domain", "custom domains for the pages branch",
			pass("add", passOpts{server: []string{"repo", "domain", "add"}, needsRepo: true}),
			pass("verify", passOpts{server: []string{"repo", "domain", "verify"}, needsRepo: true}),
			pass("list", passOpts{server: []string{"repo", "domain", "list"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"repo", "domain", "remove"}, needsRepo: true}),
		),
		group("topics", "free-form repository tags",
			pass("list", passOpts{server: []string{"repo", "topics"}, needsRepo: true}),
			pass("add", passOpts{server: []string{"repo", "topics", "add"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"repo", "topics", "remove"}, needsRepo: true}),
		),
		group("access", "manage access grants",
			pass("grant", passOpts{server: []string{"repo", "access", "grant"}, needsRepo: true}),
			pass("revoke", passOpts{server: []string{"repo", "access", "revoke"}, needsRepo: true}),
			pass("list", passOpts{server: []string{"repo", "access", "list"}, needsRepo: true}),
		),
		group("settings", "repository settings",
			pass("show", passOpts{server: []string{"repo", "settings", "show"}, needsRepo: true}),
			pass("protect", passOpts{server: []string{"repo", "settings", "protect"}, needsRepo: true}),
			pass("unprotect", passOpts{server: []string{"repo", "settings", "unprotect"}, needsRepo: true}),
			pass("protect-tag", passOpts{server: []string{"repo", "settings", "protect-tag"}, needsRepo: true}),
			pass("unprotect-tag", passOpts{server: []string{"repo", "settings", "unprotect-tag"}, needsRepo: true}),
			pass("default-branch", passOpts{server: []string{"repo", "settings", "default-branch"}, needsRepo: true}),
			pass("require-approvals", passOpts{server: []string{"repo", "settings", "require-approvals"}, needsRepo: true}),
			pass("require-resolved", passOpts{server: []string{"repo", "settings", "require-resolved"}, needsRepo: true}),
			pass("require-codeowners", passOpts{server: []string{"repo", "settings", "require-codeowners"}, needsRepo: true}),
			pass("require-checks", passOpts{server: []string{"repo", "settings", "require-checks"}, needsRepo: true}),
			pass("visibility", passOpts{server: []string{"repo", "settings", "visibility"}, needsRepo: true}),
			pass("require-signed", passOpts{server: []string{"repo", "settings", "require-signed"}, needsRepo: true}),
			pass("require-mr", passOpts{server: []string{"repo", "settings", "require-mr"}, needsRepo: true}),
			pass("description", passOpts{server: []string{"repo", "settings", "description"}, needsRepo: true}),
			pass("website", passOpts{server: []string{"repo", "settings", "website"}, needsRepo: true}),
			pass("git-daemon", passOpts{server: []string{"repo", "settings", "git-daemon"}, needsRepo: true}),
		),
	)
}

func issueCmd() *cobra.Command {
	return group("issue", "issues",
		pass("create", passOpts{server: []string{"issue", "create"}, needsRepo: true, stdinOK: true, editor: "issue"}),
		pass("list", passOpts{server: []string{"issue", "list"}, needsRepo: true}),
		pass("show", passOpts{server: []string{"issue", "show"}, needsRepo: true}),
		pass("comment", passOpts{server: []string{"issue", "comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("close", passOpts{server: []string{"issue", "close"}, needsRepo: true}),
		pass("reopen", passOpts{server: []string{"issue", "reopen"}, needsRepo: true}),
		pass("label", passOpts{server: []string{"issue", "label"}, needsRepo: true}),
		pass("assign", passOpts{server: []string{"issue", "assign"}, needsRepo: true}),
		pass("edit", passOpts{server: []string{"issue", "edit"}, needsRepo: true, stdinOK: true}),
		pass("milestone", passOpts{server: []string{"issue", "milestone"}, needsRepo: true}),
		pass("templates", passOpts{server: []string{"issue", "templates"}, needsRepo: true}),
	)
}

func releaseCmd() *cobra.Command {
	return group("release", "tag-anchored releases with notes and assets",
		pass("create", passOpts{server: []string{"release", "create"}, needsRepo: true, stdinOK: true, editor: "release"}),
		pass("edit", passOpts{server: []string{"release", "edit"}, needsRepo: true, stdinOK: true}),
		pass("list", passOpts{server: []string{"release", "list"}, needsRepo: true}),
		pass("show", passOpts{server: []string{"release", "show"}, needsRepo: true}),
		pass("delete", passOpts{server: []string{"release", "delete"}, needsRepo: true}),
		group("asset", "binary assets on a release",
			pass("add", passOpts{server: []string{"release", "asset", "add"}, needsRepo: true, alwaysStdin: true, stdinWhat: "the asset's bytes"}),
			pass("get", passOpts{server: []string{"release", "asset", "get"}, needsRepo: true}),
			pass("remove", passOpts{server: []string{"release", "asset", "remove"}, needsRepo: true}),
		),
	)
}

func milestoneCmd() *cobra.Command {
	return group("milestone", "group issues and MRs toward a release",
		pass("create", passOpts{server: []string{"milestone", "create"}, needsRepo: true}),
		pass("list", passOpts{server: []string{"milestone", "list"}, needsRepo: true}),
		pass("close", passOpts{server: []string{"milestone", "close"}, needsRepo: true}),
		pass("reopen", passOpts{server: []string{"milestone", "reopen"}, needsRepo: true}),
	)
}

func mrCmd() *cobra.Command {
	review := pass("review", passOpts{server: []string{"mr", "review"}, needsRepo: true})
	review.AddCommand(pass("request", passOpts{server: []string{"mr", "review", "request"}, needsRepo: true}))
	return group("mr", "merge requests",
		pass("create", passOpts{server: []string{"mr", "create"}, needsRepo: true, stdinOK: true, editor: "merge request", inferSource: true}),
		pass("list", passOpts{server: []string{"mr", "list"}, needsRepo: true}),
		pass("show", passOpts{server: []string{"mr", "show"}, needsRepo: true}),
		pass("diff", passOpts{server: []string{"mr", "diff"}, needsRepo: true}),
		local("checkout", "fetch and check out the MR head locally: gitbay mr checkout <n>", cmdMRCheckout),
		local("rebase", "replay the MR's branch onto its target and re-push: gitbay mr rebase <n>", cmdMRRebase),
		pass("comment", passOpts{server: []string{"mr", "comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("diff-comment", passOpts{server: []string{"mr", "diff-comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("threads", passOpts{server: []string{"mr", "threads"}, needsRepo: true}),
		pass("resolve", passOpts{server: []string{"mr", "resolve"}, needsRepo: true}),
		pass("unresolve", passOpts{server: []string{"mr", "unresolve"}, needsRepo: true}),
		review,
		pass("merge", passOpts{server: []string{"mr", "merge"}, needsRepo: true}),
		pass("close", passOpts{server: []string{"mr", "close"}, needsRepo: true}),
		pass("revisions", passOpts{server: []string{"mr", "revisions"}, needsRepo: true}),
		pass("range-diff", passOpts{server: []string{"mr", "range-diff"}, needsRepo: true}),
		pass("draft", passOpts{server: []string{"mr", "draft"}, needsRepo: true}),
		pass("ready", passOpts{server: []string{"mr", "ready"}, needsRepo: true}),
		pass("edit", passOpts{server: []string{"mr", "edit"}, needsRepo: true, stdinOK: true}),
		pass("label", passOpts{server: []string{"mr", "label"}, needsRepo: true}),
		pass("milestone", passOpts{server: []string{"mr", "milestone"}, needsRepo: true}),
		pass("retarget", passOpts{server: []string{"mr", "retarget"}, needsRepo: true}),
	)
}

// importCmd passes repo import through with stdin wired for --token-stdin.
func importCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "import",
		Short:              "server-side mirror of a foreign repo: gitbay repo import <owner/name> --from <url> [--private] [--token-stdin]",
		Annotations:        map[string]string{serverPath: "repo import"},
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
		pass("login", passOpts{server: []string{"web", "login"}}),
		group("sessions", "your browser sessions",
			pass("list", passOpts{server: []string{"web", "sessions", "list"}}),
			pass("revoke", passOpts{server: []string{"web", "sessions", "revoke"}}),
		),
		group("theme", "the colour scheme the web UI uses for you",
			pass("show", passOpts{server: []string{"web", "theme", "show"}}),
			pass("set", passOpts{server: []string{"web", "theme", "set"}}),
		),
	)
}

func webhookCmd() *cobra.Command {
	return group("webhook", "outbound event delivery",
		pass("add", passOpts{server: []string{"webhook", "add"}, needsRepo: true}),
		pass("list", passOpts{server: []string{"webhook", "list"}, needsRepo: true}),
		pass("remove", passOpts{server: []string{"webhook", "remove"}, needsRepo: true}),
		pass("deliveries", passOpts{server: []string{"webhook", "deliveries"}, needsRepo: true}),
		pass("redeliver", passOpts{server: []string{"webhook", "redeliver"}, needsRepo: true}),
	)
}

func orgCmd() *cobra.Command {
	return group("org", "organizations",
		pass("create", passOpts{server: []string{"org", "create"}}),
		pass("list", passOpts{server: []string{"org", "list"}}),
		pass("show", passOpts{server: []string{"org", "show"}}),
		pass("rename", passOpts{server: []string{"org", "rename"}}),
		pass("delete", passOpts{server: []string{"org", "delete"}}),
		pass("profile", passOpts{server: []string{"org", "profile"}}),
		group("members", "manage members",
			pass("add", passOpts{server: []string{"org", "members", "add"}}),
			pass("remove", passOpts{server: []string{"org", "members", "remove"}}),
			pass("list", passOpts{server: []string{"org", "members", "list"}}),
		),
		group("label", "labels every org repository sees",
			pass("set", passOpts{server: []string{"org", "label", "set"}}),
			pass("list", passOpts{server: []string{"org", "label", "list"}}),
			pass("remove", passOpts{server: []string{"org", "label", "remove"}}),
		),
		group("milestone", "milestones spanning an org's repositories",
			pass("create", passOpts{server: []string{"org", "milestone", "create"}}),
			pass("list", passOpts{server: []string{"org", "milestone", "list"}}),
			pass("close", passOpts{server: []string{"org", "milestone", "close"}}),
			pass("reopen", passOpts{server: []string{"org", "milestone", "reopen"}}),
		),
		group("team", "scope repository access with teams",
			pass("create", passOpts{server: []string{"org", "team", "create"}}),
			pass("delete", passOpts{server: []string{"org", "team", "delete"}}),
			pass("list", passOpts{server: []string{"org", "team", "list"}}),
			pass("show", passOpts{server: []string{"org", "team", "show"}}),
			pass("add", passOpts{server: []string{"org", "team", "add"}}),
			pass("remove", passOpts{server: []string{"org", "team", "remove"}}),
			pass("grant", passOpts{server: []string{"org", "team", "grant"}}),
			pass("revoke", passOpts{server: []string{"org", "team", "revoke"}}),
		),
		group("settings", "organization settings",
			pass("members-role", passOpts{server: []string{"org", "settings", "members-role"}}),
		),
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
	return local("init [name] [--private]", "git init + repo create + set origin, in one step", cmdInit)
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

// helpCmd is `gitbay help`. Bare, it is cobra's tree of local commands.
// With anything after it — a prefix such as `mr`, or --json — it is the
// server's help: the registry is the only place flags are written down,
// and its JSON is the contract. Cobra's built-in help used to swallow
// both forms, so `gitbay help --json` failed on an unknown flag.
func helpCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:                "help [<prefix>...] [--json]",
		Short:              "this list, or the server's command reference for a prefix",
		Annotations:        map[string]string{serverPath: "help", stdinMode: "none"},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				rootHelp(root)
				return nil
			}
			t, err := resolveTarget()
			if err != nil {
				fmt.Fprintln(os.Stderr, "gitbay:", err)
				os.Exit(protocol.ExitFailure)
			}
			os.Exit(runSSH(t, append([]string{"help"}, args...), strings.NewReader("")))
			return nil
		},
	}
}
