// forge is the client CLI. It speaks to a forge server over the system ssh
// binary; it is ergonomics on top of a control plane that is fully usable
// from bare OpenSSH.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/krazywarez/forge/internal/protocol"
)

func main() {
	root := &cobra.Command{
		Use:           "forge",
		Short:         "CLI-first git forge client",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().Bool("json", false, "machine-readable output")
	root.PersistentFlags().String("repo", "", "owner/name (default: inferred from the origin remote)")

	root.AddCommand(
		authCmd(),
		repoCmd(),
		issueCmd(),
		mrCmd(),
		webCmd(),
		adminCmd(),
		remoteCmd(),
		initCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "forge:", err)
		os.Exit(protocol.ExitFailure)
	}
}

// stub returns a leaf command that fails until its milestone lands.
func stub(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not implemented")
		},
	}
}

func group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short}
	c.AddCommand(subs...)
	return c
}

func authCmd() *cobra.Command {
	return group("auth", "identity: keys, emails, whoami",
		stub("whoami", "show the authenticated account"),
		group("keys", "manage SSH keys",
			stub("list", "list registered SSH keys"),
			stub("add", "register an SSH key"),
			stub("remove", "remove an SSH key"),
		),
		group("pgp", "manage OpenPGP keys",
			stub("list", "list registered PGP keys"),
			stub("add", "register a PGP key"),
			stub("remove", "remove a PGP key"),
		),
		group("email", "manage email addresses",
			stub("add", "add an address"),
			stub("verify", "confirm a verification code"),
		),
	)
}

func repoCmd() *cobra.Command {
	return group("repo", "create and manage repositories",
		stub("create", "create a repository"),
		stub("list", "list repositories"),
		stub("show", "show repository details"),
		stub("clone", "clone via ssh"),
		stub("rename", "rename a repository"),
		stub("delete", "delete a repository"),
		stub("fork", "fork a repository"),
		stub("import", "server-side mirror from a foreign URL"),
		stub("settings", "get or set repository settings"),
	)
}

func issueCmd() *cobra.Command {
	return group("issue", "issues",
		stub("create", "open an issue"),
		stub("list", "list issues"),
		stub("show", "show an issue"),
		stub("comment", "comment on an issue"),
		stub("close", "close an issue"),
		stub("reopen", "reopen an issue"),
		stub("label", "add or remove labels"),
		stub("assign", "assign users"),
	)
}

func mrCmd() *cobra.Command {
	return group("mr", "merge requests",
		stub("create", "open a merge request"),
		stub("list", "list merge requests"),
		stub("show", "show a merge request"),
		stub("diff", "show the diff"),
		stub("checkout", "fetch and check out the MR head locally"),
		stub("comment", "comment on a merge request"),
		stub("review", "approve or request changes"),
		stub("merge", "merge (fast-forward or merge-commit)"),
		stub("close", "close without merging"),
	)
}

func webCmd() *cobra.Command {
	return group("web", "browser session",
		stub("login", "mint a one-time browser login URL over ssh"),
	)
}

func adminCmd() *cobra.Command {
	return group("admin", "instance administration (admin accounts only)",
		stub("user", "manage users"),
		stub("invite", "issue registration invites"),
		stub("stats", "instance statistics"),
	)
}

func remoteCmd() *cobra.Command {
	return group("remote", "local instance profiles (no server contact)",
		stub("add", "add a named forge instance"),
		stub("list", "list configured instances"),
	)
}

func initCmd() *cobra.Command {
	return stub("init", "git init + repo create + set origin, in one step")
}
