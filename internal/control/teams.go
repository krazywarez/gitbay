package control

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"org", "team", "create"},
		Summary: "create a team",
		Usage:   "org team create <org> <team>", Run: runTeamCreate})
	register(Command{Path: []string{"org", "team", "delete"},
		Summary: "delete a team (its grants with it)",
		Usage:   "org team delete <org> <team>", Run: runTeamDelete})
	register(Command{Path: []string{"org", "team", "list"},
		Summary: "list an org's teams",
		Usage:   "org team list <org>", ReadOnly: true, Run: runTeamList})
	register(Command{Path: []string{"org", "team", "show"},
		Summary: "show a team's members and grants",
		Usage:   "org team show <org> <team>", ReadOnly: true, Run: runTeamShow})
	register(Command{Path: []string{"org", "team", "add"},
		Summary: "add org members to a team",
		Usage:   "org team add <org> <team> <user>...", Run: runTeamAdd})
	register(Command{Path: []string{"org", "team", "remove"},
		Summary: "remove members from a team",
		Usage:   "org team remove <org> <team> <user>...", Run: runTeamRemove})
	register(Command{Path: []string{"org", "team", "grant"},
		Summary: "grant a team a role on an org repo",
		Usage:   "org team grant <org> <team> <owner/name> read|write|admin", Run: runTeamGrant})
	register(Command{Path: []string{"org", "team", "revoke"},
		Summary: "revoke a team's grant",
		Usage:   "org team revoke <org> <team> <owner/name>", Run: runTeamRevoke})
	register(Command{Path: []string{"org", "settings", "members-role"},
		Summary: "role plain membership implies on every org repo",
		Usage:   "org settings members-role <org> write|read|none (default write)", Run: runOrgMembersRole})
}

// orgAdminRef resolves an org and requires the caller to admin it.
func orgAdminRef(c *Ctx, name string) (store.Org, int) {
	org, err := c.Store.OrgByName(name)
	if err != nil {
		return org, c.fail(protocol.ExitNotFound, "no organization %q", name)
	}
	role, err := c.Store.OrgRole(org.ID, c.User.ID)
	if err != nil {
		return org, c.fail(protocol.ExitFailure, "%v", err)
	}
	if role != "admin" {
		return org, c.fail(protocol.ExitDenied, "only admins of %s can manage teams", name)
	}
	return org, -1
}

// orgMemberRef resolves an org, requiring at least membership.
func orgMemberRef(c *Ctx, name string) (store.Org, int) {
	org, err := c.Store.OrgByName(name)
	if err != nil {
		return org, c.fail(protocol.ExitNotFound, "no organization %q", name)
	}
	role, err := c.Store.OrgRole(org.ID, c.User.ID)
	if err != nil {
		return org, c.fail(protocol.ExitFailure, "%v", err)
	}
	if role == "" {
		return org, c.fail(protocol.ExitNotFound, "no organization %q", name)
	}
	return org, -1
}

func teamRef(c *Ctx, org store.Org, name string) (store.Team, int) {
	team, err := c.Store.TeamByName(org.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		return team, c.fail(protocol.ExitNotFound, "no team %q in %s", name, org.Name)
	}
	if err != nil {
		return team, c.fail(protocol.ExitFailure, "%v", err)
	}
	return team, -1
}

func runTeamCreate(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org team create <org> <team>")
	}
	org, code := orgAdminRef(c, args[0])
	if code >= 0 {
		return code
	}
	if err := policy.ValidateName(args[1]); err != nil {
		return c.failErr(err)
	}
	if _, err := c.Store.CreateTeam(org.ID, args[1]); err != nil {
		return c.failErr(err)
	}
	return c.emit(map[string]string{"team": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "created team %s/%s\n", org.Name, args[1])
	})
}

func runTeamDelete(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org team delete <org> <team>")
	}
	org, code := orgAdminRef(c, args[0])
	if code >= 0 {
		return code
	}
	team, code := teamRef(c, org, args[1])
	if code >= 0 {
		return code
	}
	if err := c.Store.DeleteTeam(team.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"deleted": team.Name}, func(w io.Writer) {
		fmt.Fprintf(w, "deleted team %s/%s\n", org.Name, team.Name)
	})
}

func runTeamList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org team list <org>")
	}
	org, code := orgMemberRef(c, args[0])
	if code >= 0 {
		return code
	}
	teams, err := c.Store.ListTeams(org.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var names []string
	for _, t := range teams {
		names = append(names, t.Name)
	}
	return c.emit(names, func(w io.Writer) {
		for _, n := range names {
			fmt.Fprintln(w, n)
		}
	})
}

func runTeamShow(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org team show <org> <team>")
	}
	org, code := orgMemberRef(c, args[0])
	if code >= 0 {
		return code
	}
	team, code := teamRef(c, org, args[1])
	if code >= 0 {
		return code
	}
	members, err := c.Store.TeamMembers(team.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	grants, err := c.Store.TeamGrants(team.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	d := struct {
		Team    string            `json:"team"`
		Members []string          `json:"members,omitempty"`
		Grants  []store.TeamGrant `json:"grants,omitempty"`
	}{team.Name, members, grants}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s/%s\nmembers: %s\n", org.Name, team.Name, strings.Join(members, ", "))
		for _, g := range grants {
			fmt.Fprintf(w, "%s\t%s\n", g.RepoPath, g.Role)
		}
	})
}

func runTeamAdd(c *Ctx, args []string) int    { return editTeamMembers(c, args, true) }
func runTeamRemove(c *Ctx, args []string) int { return editTeamMembers(c, args, false) }

func editTeamMembers(c *Ctx, args []string, add bool) int {
	verb := "add"
	if !add {
		verb = "remove"
	}
	if len(args) < 3 {
		return c.fail(protocol.ExitUsage, "usage: org team %s <org> <team> <user>...", verb)
	}
	org, code := orgAdminRef(c, args[0])
	if code >= 0 {
		return code
	}
	team, code := teamRef(c, org, args[1])
	if code >= 0 {
		return code
	}
	for _, name := range args[2:] {
		u, err := c.Store.UserByUsername(name)
		if err != nil {
			return c.fail(protocol.ExitNotFound, "no such user %q", name)
		}
		if add {
			// Teams organize existing members; they do not grant
			// membership by side effect.
			role, err := c.Store.OrgRole(org.ID, u.ID)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			if role == "" {
				return c.fail(protocol.ExitUsage, "%s is not a member of %s — add them to the org first", name, org.Name)
			}
			if err := c.Store.AddTeamMember(team.ID, u.ID); err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
		} else if err := c.Store.RemoveTeamMember(team.ID, u.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return c.fail(protocol.ExitNotFound, "%s is not in team %s", name, team.Name)
			}
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	}
	return c.emit(map[string]any{"team": team.Name, verb: args[2:]}, func(w io.Writer) {
		fmt.Fprintf(w, "%sed %s on team %s/%s\n", verb, strings.Join(args[2:], ", "), org.Name, team.Name)
	})
}

func runTeamGrant(c *Ctx, args []string) int {
	if len(args) != 4 || !slices.Contains([]string{"read", "write", "admin"}, args[3]) {
		return c.fail(protocol.ExitUsage, "usage: org team grant <org> <team> <owner/name> read|write|admin")
	}
	org, code := orgAdminRef(c, args[0])
	if code >= 0 {
		return code
	}
	team, code := teamRef(c, org, args[1])
	if code >= 0 {
		return code
	}
	repo, err := c.Store.RepoByPath(args[2])
	if err != nil || repo.OwnerKind != "org" || repo.OwnerID != org.ID {
		return c.fail(protocol.ExitUsage, "teams grant access to their own org's repositories; %q is not one", args[2])
	}
	if err := c.Store.GrantTeamRepo(team.ID, repo.ID, args[3]); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"team": team.Name, "repo": repo.Path(), "role": args[3]}, func(w io.Writer) {
		fmt.Fprintf(w, "granted %s to team %s on %s\n", args[3], team.Name, repo.Path())
	})
}

func runTeamRevoke(c *Ctx, args []string) int {
	if len(args) != 3 {
		return c.fail(protocol.ExitUsage, "usage: org team revoke <org> <team> <owner/name>")
	}
	org, code := orgAdminRef(c, args[0])
	if code >= 0 {
		return code
	}
	team, code := teamRef(c, org, args[1])
	if code >= 0 {
		return code
	}
	repo, err := c.Store.RepoByPath(args[2])
	if err != nil {
		return c.fail(protocol.ExitNotFound, "repository %s not found", args[2])
	}
	if err := c.Store.RevokeTeamRepo(team.ID, repo.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "team %s has no grant on %s", team.Name, repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"revoked": repo.Path()}, func(w io.Writer) {
		fmt.Fprintf(w, "revoked team %s on %s\n", team.Name, repo.Path())
	})
}

func runOrgMembersRole(c *Ctx, args []string) int {
	if len(args) != 2 || !slices.Contains([]string{"write", "read", "none"}, args[1]) {
		return c.fail(protocol.ExitUsage, "usage: org settings members-role <org> write|read|none")
	}
	org, code := orgAdminRef(c, args[0])
	if code >= 0 {
		return code
	}
	if err := c.Store.SetOrgMembersRole(org.ID, args[1]); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"members_role": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "plain members of %s now get %q on org repositories\n", org.Name, args[1])
	})
}
