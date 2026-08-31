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

	root.AddCommand(
		authCmd(),
		group("status", "commit statuses (CI)",
			pass("set", "report a status: <sha> --context <c> --state <s> [--description d] [--url u]", passOpts{server: []string{"status", "set"}, needsRepo: true}),
			pass("list", "statuses on a commit: <sha>", passOpts{server: []string{"status", "list"}, needsRepo: true}),
		),
		group("build", "CI builds",
			pass("list", "recent builds: <owner/name>", passOpts{server: []string{"build", "list"}, needsRepo: true}),
			pass("show", "one build: <owner/name> <n>", passOpts{server: []string{"build", "show"}, needsRepo: true}),
			pass("log", "a build's log: <owner/name> <n>", passOpts{server: []string{"build", "log"}, needsRepo: true}),
			pass("jobs", "list the jobs a trigger can name", passOpts{server: []string{"build", "jobs"}, needsRepo: true}),
			pass("trigger", "queue a job now: <job>", passOpts{server: []string{"build", "trigger"}, needsRepo: true}),
		),
		pass("dashboard", "one read for the account dashboard: pinned repos, open MRs, assigned issues, recent builds",
			passOpts{server: []string{"dashboard"}}),
		pass("feed", "activity on repositories you can reach [--limit n] [--cursor c]",
			passOpts{server: []string{"feed"}}),
		pass("explore", "public repositories on this instance [--limit n] [--cursor c]",
			passOpts{server: []string{"explore"}}),
		group("wiki", "a repository's wiki pages",
			pass("list", "list pages: [<owner/name>]", passOpts{server: []string{"wiki", "list"}, needsRepo: true}),
			pass("show", "print a page: [<owner/name>] [<page>]", passOpts{server: []string{"wiki", "show"}, needsRepo: true}),
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
			pass("show", "show a profile: [name]", passOpts{server: []string{"profile", "show"}}),
			pass("set", "set your profile: [--description d] [--website url] [--about t|--file -] [--about-format md|org] [--link label|url]...",
				passOpts{server: []string{"profile", "set"}, stdinOK: true}),
		),
		webhookCmd(),
		remoteCmd(),
		initCmd(),
		pass("register", "create an account on the default instance: gitbay register --username <n> --email <a> | --invite <code>",
			passOpts{server: []string{"register"}}),
		pass("audit", "instance audit log (admins): [--limit <n>]", passOpts{server: []string{"audit"}}),
		manCmd(root),
	)
	return root
}

// serverPath is the annotation key holding a passthrough command's
// server-side path, so the tree can be checked against the registry.
const serverPath = "gitbay.server_path"
const stdinMode = "gitbay.stdin_mode"

// passOpts describes how one CLI command maps onto the server command.
type passOpts struct {
	server      []string // server-side command path
	needsRepo   bool     // prepend inferred owner/name unless given
	stdinOK     bool     // wire local stdin through when --file - asks for it
	alwaysStdin bool     // stdin is the payload, named by no flag: a bare redirect
	editor      string   // open $EDITOR for a body when none given
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

func pass(use, short string, o passOpts) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Annotations: map[string]string{
			serverPath: strings.Join(o.server, " "),
			stdinMode:  o.stdinModeName(),
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
	if o.needsRepo {
		args, err = withRepo(t, args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gitbay:", err)
			return protocol.ExitUsage
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
		if a == "--token-stdin" {
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
		passOpts{server: []string{"keys", "add"}, alwaysStdin: true})
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
		Annotations:        map[string]string{serverPath: "pgp add"},
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
	tokens := group("token", "API tokens (minted over SSH, used with the JSON API)",
		pass("create", "mint a token: --name <n> [--scope full|read] [--ttl 30d]", passOpts{server: []string{"token", "create"}}),
		pass("list", "list API tokens", passOpts{server: []string{"token", "list"}}),
		pass("revoke", "revoke a token by name", passOpts{server: []string{"token", "revoke"}}),
	)
	return group("auth", "identity: whoami, SSH and PGP keys",
		pass("export", "write your account bundle (a user-level backup) to stdout",
			passOpts{server: []string{"account", "export"}}),
		tokens,
		pass("whoami", "show the authenticated account", passOpts{server: []string{"whoami"}}),
		group("keys", "manage SSH keys",
			pass("list", "list registered SSH keys", passOpts{server: []string{"keys", "list"}}),
			keysAdd,
			pass("remove", "remove an SSH key by fingerprint", passOpts{server: []string{"keys", "remove"}}),
		),
		group("email", "manage email addresses",
			pass("add", "add an address and get a verification code by mail", passOpts{server: []string{"email", "add"}}),
			pass("verify", "confirm a verification code", passOpts{server: []string{"email", "verify"}}),
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
		pass("list", "list repositories you own or can access [--limit n] [--cursor c]", passOpts{server: []string{"repo", "list"}}),
		pass("show", "show repository details", passOpts{server: []string{"repo", "show"}, needsRepo: true}),
		pass("log", "commit log with signature states", passOpts{server: []string{"repo", "log"}, needsRepo: true}),
		pass("transfer", "move a repository to another owner: <new-owner>", passOpts{server: []string{"repo", "transfer"}, needsRepo: true}),
		pass("delete", "delete a repository (--yes)", passOpts{server: []string{"repo", "delete"}, needsRepo: true}),
		pass("fork", "fork a repository under your account", passOpts{server: []string{"repo", "fork"}, needsRepo: true}),
		pass("search", "find repositories by name, description, or topic: <query>", passOpts{server: []string{"repo", "search"}}),
		pass("grep", "search file contents: <query> [--ref <ref>]", passOpts{server: []string{"repo", "grep"}, needsRepo: true}),
		pass("tree", "list a directory: [<path>] [--ref <ref>]", passOpts{server: []string{"repo", "tree"}, needsRepo: true}),
		pass("cat", "read a file: <path> [--ref <ref>]", passOpts{server: []string{"repo", "cat"}, needsRepo: true}),
		pass("blame", "attribute lines to commits: <path> [--ref <ref>] [--from <n>] [--to <n>]",
			passOpts{server: []string{"repo", "blame"}, needsRepo: true}),
		pass("commit", "show one commit with its patch: <sha>",
			passOpts{server: []string{"repo", "commit"}, needsRepo: true}),
		pass("commit-file", "write a file and commit it: <path> [--ref <ref>] [--message <m>] --file -",
			passOpts{server: []string{"repo", "commit-file"}, needsRepo: true, stdinOK: true}),
		pass("refs", "list branches and tags", passOpts{server: []string{"repo", "refs"}, needsRepo: true}),
		pass("download", "write a tar.gz of a ref to stdout: [--ref <r>] > repo.tar.gz",
			passOpts{server: []string{"repo", "download"}, needsRepo: true}),
		pass("pin", "pin a repository to your dashboard", passOpts{server: []string{"repo", "pin"}, needsRepo: true}),
		pass("unpin", "unpin a repository", passOpts{server: []string{"repo", "unpin"}, needsRepo: true}),
		pass("archive", "archive a repository (read-only)", passOpts{server: []string{"repo", "archive"}, needsRepo: true}),
		pass("unarchive", "unarchive a repository", passOpts{server: []string{"repo", "unarchive"}, needsRepo: true}),
		local("clone", "clone via ssh: gitbay repo clone <owner/name> [dir]", cmdRepoClone),
		importCmd(),
		pass("import-issues", "import GitHub issue/PR history: --from <ghowner/ghrepo> [--token-stdin]",
			passOpts{server: []string{"repo", "import-issues"}, needsRepo: true, stdinOK: true}),
		group("deploy-key", "repository-bound CI keys",
			pass("add", "bind a key: [--rw] < key.pub", passOpts{server: []string{"repo", "deploy-key", "add"}, needsRepo: true, alwaysStdin: true}),
			pass("list", "list deploy keys", passOpts{server: []string{"repo", "deploy-key", "list"}, needsRepo: true}),
			pass("remove", "remove a deploy key: <fingerprint>", passOpts{server: []string{"repo", "deploy-key", "remove"}, needsRepo: true}),
		),
		group("mirror", "sync with a foreign remote",
			pass("add", "add a mirror: <https-url> --direction push|pull [--username <u>] [--token-stdin]",
				passOpts{server: []string{"repo", "mirror", "add"}, needsRepo: true, stdinOK: true}),
			pass("list", "list mirrors with sync status", passOpts{server: []string{"repo", "mirror", "list"}, needsRepo: true}),
			pass("remove", "remove a mirror: <id>", passOpts{server: []string{"repo", "mirror", "remove"}, needsRepo: true}),
			pass("sync", "schedule an immediate sync", passOpts{server: []string{"repo", "mirror", "sync"}, needsRepo: true}),
		),
		group("deps", "check dependencies against upstream registries",
			pass("enable", "check this repo's dependencies for updates", passOpts{server: []string{"repo", "deps", "enable"}, needsRepo: true}),
			pass("disable", "stop checking dependencies", passOpts{server: []string{"repo", "deps", "disable"}, needsRepo: true}),
			pass("status", "show check state and what is behind", passOpts{server: []string{"repo", "deps", "status"}, needsRepo: true}),
		),
		group("secret", "build secrets (values on stdin, injected into build env)",
			pass("set", "set a secret: <NAME> (value on stdin)", passOpts{server: []string{"repo", "secret", "set"}, needsRepo: true, alwaysStdin: true}),
			pass("list", "list secret names", passOpts{server: []string{"repo", "secret", "list"}, needsRepo: true}),
			pass("remove", "remove a secret: <NAME>", passOpts{server: []string{"repo", "secret", "remove"}, needsRepo: true}),
		),
		group("domain", "custom domains for the pages branch",
			pass("add", "claim a domain (verify with a DNS TXT record): <domain>", passOpts{server: []string{"repo", "domain", "add"}, needsRepo: true}),
			pass("verify", "check the DNS challenge and activate a claim: <domain>", passOpts{server: []string{"repo", "domain", "verify"}, needsRepo: true}),
			pass("list", "list custom pages domains", passOpts{server: []string{"repo", "domain", "list"}, needsRepo: true}),
			pass("remove", "remove a custom pages domain: <domain>", passOpts{server: []string{"repo", "domain", "remove"}, needsRepo: true}),
		),
		group("topics", "free-form repository tags",
			pass("list", "list topics", passOpts{server: []string{"repo", "topics"}, needsRepo: true}),
			pass("add", "add topics: <topic>...", passOpts{server: []string{"repo", "topics", "add"}, needsRepo: true}),
			pass("remove", "remove topics: <topic>...", passOpts{server: []string{"repo", "topics", "remove"}, needsRepo: true}),
		),
		group("access", "manage access grants",
			pass("grant", "grant access: ... <user> read|write|admin", passOpts{server: []string{"repo", "access", "grant"}, needsRepo: true}),
			pass("revoke", "revoke access: ... <user>", passOpts{server: []string{"repo", "access", "revoke"}, needsRepo: true}),
			pass("list", "list access grants", passOpts{server: []string{"repo", "access", "list"}, needsRepo: true}),
		),
		group("settings", "repository settings",
			pass("show", "show settings", passOpts{server: []string{"repo", "settings", "show"}, needsRepo: true}),
			pass("protect", "protect a branch", passOpts{server: []string{"repo", "settings", "protect"}, needsRepo: true}),
			pass("unprotect", "unprotect a branch", passOpts{server: []string{"repo", "settings", "unprotect"}, needsRepo: true}),
			pass("require-approvals", "require N fresh approvals to merge: <n>", passOpts{server: []string{"repo", "settings", "require-approvals"}, needsRepo: true}),
			pass("require-resolved", "require threads resolved to merge: on|off", passOpts{server: []string{"repo", "settings", "require-resolved"}, needsRepo: true}),
			pass("require-checks", "gate merges on green statuses: ... on|off", passOpts{server: []string{"repo", "settings", "require-checks"}, needsRepo: true}),
			pass("visibility", "set repository visibility: public|private", passOpts{server: []string{"repo", "settings", "visibility"}, needsRepo: true}),
			pass("require-signed", "require verified commit signatures: ... on|off", passOpts{server: []string{"repo", "settings", "require-signed"}, needsRepo: true}),
			pass("description", "set the repository description: <text>", passOpts{server: []string{"repo", "settings", "description"}, needsRepo: true}),
			pass("website", "set the repository website: <url> ('' clears)", passOpts{server: []string{"repo", "settings", "website"}, needsRepo: true}),
			pass("git-daemon", "expose over git://: ... on|off", passOpts{server: []string{"repo", "settings", "git-daemon"}, needsRepo: true}),
		),
	)
}

func issueCmd() *cobra.Command {
	return group("issue", "issues",
		pass("create", "open an issue: --title <t> [--body|--file -|$EDITOR]",
			passOpts{server: []string{"issue", "create"}, needsRepo: true, stdinOK: true, editor: "issue"}),
		pass("list", "list issues [--state open|closed|all] [--limit n] [--cursor c]", passOpts{server: []string{"issue", "list"}, needsRepo: true}),
		pass("show", "show an issue with comments", passOpts{server: []string{"issue", "show"}, needsRepo: true}),
		pass("comment", "comment on an issue [--message|--file -|$EDITOR]",
			passOpts{server: []string{"issue", "comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("close", "close an issue", passOpts{server: []string{"issue", "close"}, needsRepo: true}),
		pass("reopen", "reopen an issue", passOpts{server: []string{"issue", "reopen"}, needsRepo: true}),
		pass("label", "add or remove labels: [--add <l>]... [--remove <l>]...", passOpts{server: []string{"issue", "label"}, needsRepo: true}),
		pass("assign", "assign users: [--add <u>]... [--remove <u>]...", passOpts{server: []string{"issue", "assign"}, needsRepo: true}),
		pass("edit", "edit title or body: <n> [--title <t>] [--body <b>|--file -]", passOpts{server: []string{"issue", "edit"}, needsRepo: true, stdinOK: true}),
		pass("milestone", "set or clear the milestone: <n> <title|none>", passOpts{server: []string{"issue", "milestone"}, needsRepo: true}),
		pass("templates", "list issue templates (.gitbay/issue-template*.md)", passOpts{server: []string{"issue", "templates"}, needsRepo: true}),
	)
}

func releaseCmd() *cobra.Command {
	return group("release", "tag-anchored releases with notes and assets",
		pass("create", "create a release on a pushed tag: <tag> [--title <t>] [--notes|--file -|$EDITOR]",
			passOpts{server: []string{"release", "create"}, needsRepo: true, stdinOK: true, editor: "release"}),
		pass("edit", "update title and notes: <tag> [--title <t>] [--notes|--file -]",
			passOpts{server: []string{"release", "edit"}, needsRepo: true, stdinOK: true}),
		pass("list", "list releases", passOpts{server: []string{"release", "list"}, needsRepo: true}),
		pass("show", "show a release with assets: <tag>", passOpts{server: []string{"release", "show"}, needsRepo: true}),
		pass("delete", "delete a release and its assets: <tag> --yes", passOpts{server: []string{"release", "delete"}, needsRepo: true}),
		group("asset", "binary assets on a release",
			pass("add", "upload from stdin: <tag> <filename> < file", passOpts{server: []string{"release", "asset", "add"}, needsRepo: true, alwaysStdin: true}),
			pass("get", "download to stdout: <tag> <filename> > file", passOpts{server: []string{"release", "asset", "get"}, needsRepo: true}),
			pass("remove", "remove an asset: <tag> <filename>", passOpts{server: []string{"release", "asset", "remove"}, needsRepo: true}),
		),
	)
}

func milestoneCmd() *cobra.Command {
	return group("milestone", "group issues and MRs toward a release",
		pass("create", "create a milestone: <title> [--description <d>] [--due YYYY-MM-DD]",
			passOpts{server: []string{"milestone", "create"}, needsRepo: true}),
		pass("list", "list milestones with progress [--state open|closed|all]",
			passOpts{server: []string{"milestone", "list"}, needsRepo: true}),
		pass("close", "close a milestone: <title>", passOpts{server: []string{"milestone", "close"}, needsRepo: true}),
		pass("reopen", "reopen a milestone: <title>", passOpts{server: []string{"milestone", "reopen"}, needsRepo: true}),
	)
}

func mrCmd() *cobra.Command {
	return group("mr", "merge requests",
		pass("create", "open a merge request: --source <branch> --target <branch> --title <t>",
			passOpts{server: []string{"mr", "create"}, needsRepo: true, stdinOK: true, editor: "merge request"}),
		pass("list", "list merge requests [--state ...] [--limit n] [--cursor c]", passOpts{server: []string{"mr", "list"}, needsRepo: true}),
		pass("show", "show a merge request", passOpts{server: []string{"mr", "show"}, needsRepo: true}),
		pass("diff", "show the diff", passOpts{server: []string{"mr", "diff"}, needsRepo: true}),
		local("checkout", "fetch and check out the MR head locally: gitbay mr checkout <n>", cmdMRCheckout),
		pass("comment", "comment on a merge request", passOpts{server: []string{"mr", "comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("diff-comment", "comment on a diff line: --path <f> --line <l> [--old] [--reply <id>]", passOpts{server: []string{"mr", "diff-comment"}, needsRepo: true, stdinOK: true, editor: "comment"}),
		pass("threads", "review threads on an MR", passOpts{server: []string{"mr", "threads"}, needsRepo: true}),
		pass("resolve", "resolve a review thread: <n> <thread-id>", passOpts{server: []string{"mr", "resolve"}, needsRepo: true}),
		pass("unresolve", "reopen a review thread: <n> <thread-id>", passOpts{server: []string{"mr", "unresolve"}, needsRepo: true}),
		pass("review", "review: --approve|--request-changes|--comment", passOpts{server: []string{"mr", "review"}, needsRepo: true}),
		pass("merge", "merge: [--strategy ff|merge|squash|rebase]", passOpts{server: []string{"mr", "merge"}, needsRepo: true}),
		pass("close", "close without merging", passOpts{server: []string{"mr", "close"}, needsRepo: true}),
		pass("edit", "edit title or body: <n> [--title <t>] [--body <b>|--file -]", passOpts{server: []string{"mr", "edit"}, needsRepo: true, stdinOK: true}),
		pass("milestone", "set or clear the milestone: <n> <title|none>", passOpts{server: []string{"mr", "milestone"}, needsRepo: true}),
		pass("retarget", "retarget onto another branch: <n> <branch>", passOpts{server: []string{"mr", "retarget"}, needsRepo: true}),
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
		pass("login", "mint a one-time browser login URL over ssh", passOpts{server: []string{"web", "login"}}),
	)
}

func webhookCmd() *cobra.Command {
	return group("webhook", "outbound event delivery",
		pass("add", "add a webhook: <url> [--secret s] [--events k1,k2|*]", passOpts{server: []string{"webhook", "add"}, needsRepo: true}),
		pass("list", "list webhooks", passOpts{server: []string{"webhook", "list"}, needsRepo: true}),
		pass("remove", "remove a webhook: <id>", passOpts{server: []string{"webhook", "remove"}, needsRepo: true}),
		pass("deliveries", "recent deliveries [--limit n]", passOpts{server: []string{"webhook", "deliveries"}, needsRepo: true}),
		pass("redeliver", "requeue a delivery: <delivery-id>", passOpts{server: []string{"webhook", "redeliver"}, needsRepo: true}),
	)
}

func orgCmd() *cobra.Command {
	return group("org", "organizations",
		pass("create", "create an organization", passOpts{server: []string{"org", "create"}}),
		pass("list", "list organizations you belong to", passOpts{server: []string{"org", "list"}}),
		pass("show", "show an organization and its members", passOpts{server: []string{"org", "show"}}),
		pass("rename", "rename an organization: <old> <new>", passOpts{server: []string{"org", "rename"}}),
		pass("delete", "delete an empty organization (--yes)", passOpts{server: []string{"org", "delete"}}),
		pass("profile", "show or set an org profile: <org> [--description d] [--website url] [--about t|--file -] [--about-format md|org] [--link label|url]...",
			passOpts{server: []string{"org", "profile"}, stdinOK: true}),
		group("members", "manage members",
			pass("add", "add or update a member: <org> <user> [--role member|admin]", passOpts{server: []string{"org", "members", "add"}}),
			pass("remove", "remove a member: <org> <user>", passOpts{server: []string{"org", "members", "remove"}}),
			pass("list", "list members: <org>", passOpts{server: []string{"org", "members", "list"}}),
		),
		group("team", "scope repository access with teams",
			pass("create", "create a team: <org> <team>", passOpts{server: []string{"org", "team", "create"}}),
			pass("delete", "delete a team: <org> <team>", passOpts{server: []string{"org", "team", "delete"}}),
			pass("list", "list teams: <org>", passOpts{server: []string{"org", "team", "list"}}),
			pass("show", "show members and grants: <org> <team>", passOpts{server: []string{"org", "team", "show"}}),
			pass("add", "add org members: <org> <team> <user>...", passOpts{server: []string{"org", "team", "add"}}),
			pass("remove", "remove members: <org> <team> <user>...", passOpts{server: []string{"org", "team", "remove"}}),
			pass("grant", "grant a repo role: <org> <team> <owner/name> read|write|admin", passOpts{server: []string{"org", "team", "grant"}}),
			pass("revoke", "revoke a repo grant: <org> <team> <owner/name>", passOpts{server: []string{"org", "team", "revoke"}}),
		),
		group("settings", "organization settings",
			pass("members-role", "role plain membership implies: <org> write|read|none", passOpts{server: []string{"org", "settings", "members-role"}}),
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
