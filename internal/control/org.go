package control

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"org", "create"},
		Summary: "create an organization (you become its first admin)",
		Usage:   "org create <name>", Run: runOrgCreate})
	register(Command{Path: []string{"org", "list"},
		Summary: "list organizations you belong to",
		Usage:   "org list", ReadOnly: true, Run: runOrgList})
	register(Command{Path: []string{"org", "show"},
		Summary: "show an organization and its members",
		Usage:   "org show <name>", ReadOnly: true, Run: runOrgShow})
	register(Command{Path: []string{"org", "rename"},
		Summary: "rename an organization",
		Usage:   "org rename <old> <new> (clone URLs change)", Run: runOrgRename})
	register(Command{Path: []string{"org", "delete"},
		Summary: "delete an empty organization",
		Usage:   "org delete <name> --yes", Run: runOrgDelete})
	register(Command{Path: []string{"org", "members", "add"},
		Summary: "add or update a member",
		Usage:   "org members add <org> <user> [--role member|admin]", Run: runOrgMembersAdd})
	register(Command{Path: []string{"org", "members", "remove"},
		Summary: "remove a member",
		Usage:   "org members remove <org> <user>", Run: runOrgMembersRemove})
	register(Command{Path: []string{"org", "members", "list"},
		Summary: "list members",
		Usage:   "org members list <org>", ReadOnly: true, Run: runOrgMembersList})
}

// orgAdmin loads an org and requires the caller to be one of its admins.
func orgAdmin(c *Ctx, name string) (store.Org, int) {
	org, err := c.Store.OrgByName(name)
	if errors.Is(err, store.ErrNotFound) {
		return org, c.fail(protocol.ExitNotFound, "no organization %q", name)
	}
	if err != nil {
		return org, c.fail(protocol.ExitFailure, "%v", err)
	}
	role, err := c.Store.OrgRole(org.ID, c.User.ID)
	if err != nil {
		return org, c.fail(protocol.ExitFailure, "%v", err)
	}
	if role != "admin" {
		return org, c.fail(protocol.ExitDenied, "only admins of %s can do that", name)
	}
	return org, -1
}

func runOrgCreate(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org create <name>")
	}
	if err := policy.ValidateOwnerName(args[0]); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if _, err := c.Store.CreateOrg(args[0], c.User.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"org": args[0], "role": "admin"}, func(w io.Writer) {
		fmt.Fprintf(w, "created organization %s; you are its admin\n", args[0])
	})
}

func runOrgList(c *Ctx, args []string) int {
	orgs, err := c.Store.ListOrgsForUser(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Org  string `json:"org"`
		Role string `json:"role"`
	}
	var ds []out
	for _, o := range orgs {
		ds = append(ds, out{o.Username, o.Role})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintf(w, "%s\t%s\n", d.Org, d.Role)
		}
	})
}

func runOrgShow(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org show <name>")
	}
	org, err := c.Store.OrgByName(args[0])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no organization %q", args[0])
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	members, err := c.Store.OrgMembers(org.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type memberOut struct {
		User string `json:"user"`
		Role string `json:"role"`
	}
	var ms []memberOut
	for _, m := range members {
		ms = append(ms, memberOut{m.Username, m.Role})
	}
	d := struct {
		Org     string      `json:"org"`
		Members []memberOut `json:"members"`
	}{org.Name, ms}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s\n", d.Org)
		for _, m := range ms {
			fmt.Fprintf(w, "  %s\t%s\n", m.User, m.Role)
		}
	})
}

func runOrgRename(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org rename <old> <new>")
	}
	org, code := orgAdmin(c, args[0])
	if code >= 0 {
		return code
	}
	newName := args[1]
	if err := policy.ValidateOwnerName(newName); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	oldDir := filepath.Join(c.Cfg.Server.Root, "repos", org.Name)
	newDir := filepath.Join(c.Cfg.Server.Root, "repos", newName)
	if _, err := os.Stat(newDir); err == nil {
		return c.fail(protocol.ExitFailure, "repository directory %s already exists", newName)
	}
	if err := c.Store.RenameOrg(org.ID, newName); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	// Repo paths on disk derive from the owner name; move the tree. If the
	// move fails, revert the database so name and disk stay consistent.
	if _, err := os.Stat(oldDir); err == nil {
		if err := os.Rename(oldDir, newDir); err != nil {
			c.Store.RenameOrg(org.ID, org.Name)
			return c.fail(protocol.ExitFailure, "moving repositories: %v", err)
		}
	}
	return c.emit(map[string]string{"org": newName, "was": org.Name}, func(w io.Writer) {
		fmt.Fprintf(w, "renamed %s to %s — clone URLs now use %s/<repo>\n", org.Name, newName, newName)
	})
}

func runOrgDelete(c *Ctx, args []string) int {
	var name string
	yes := false
	for _, a := range args {
		if a == "--yes" {
			yes = true
		} else if name == "" {
			name = a
		} else {
			return c.fail(protocol.ExitUsage, "usage: org delete <name> --yes")
		}
	}
	if name == "" {
		return c.fail(protocol.ExitUsage, "usage: org delete <name> --yes")
	}
	org, code := orgAdmin(c, name)
	if code >= 0 {
		return code
	}
	if !yes {
		return c.fail(protocol.ExitUsage, "org delete is permanent; re-run with --yes")
	}
	if err := c.Store.DeleteOrg(org.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"deleted": name}, func(w io.Writer) {
		fmt.Fprintf(w, "deleted organization %s\n", name)
	})
}

func runOrgMembersAdd(c *Ctx, args []string) int {
	role := "member"
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--role" {
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "--role requires member|admin")
			}
			role = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) != 2 || (role != "member" && role != "admin") {
		return c.fail(protocol.ExitUsage, "usage: org members add <org> <user> [--role member|admin]")
	}
	org, code := orgAdmin(c, rest[0])
	if code >= 0 {
		return code
	}
	target, err := c.Store.UserByUsername(rest[1])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no such user %q", rest[1])
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.SetOrgMember(org.ID, target.ID, role); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	return c.emit(map[string]string{"org": org.Name, "user": target.Username, "role": role}, func(w io.Writer) {
		fmt.Fprintf(w, "%s is now a %s of %s\n", target.Username, role, org.Name)
	})
}

func runOrgMembersRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org members remove <org> <user>")
	}
	org, code := orgAdmin(c, args[0])
	if code >= 0 {
		return code
	}
	target, err := c.Store.UserByUsername(args[1])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no such user %q", args[1])
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.RemoveOrgMember(org.ID, target.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "%s is not a member of %s", target.Username, org.Name)
		}
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	return c.emit(map[string]string{"org": org.Name, "removed": target.Username}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s from %s\n", target.Username, org.Name)
	})
}

func runOrgMembersList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org members list <org>")
	}
	return runOrgShow(c, args)
}
