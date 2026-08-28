package control

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"profile", "show"},
		Summary: "show a user's or org's profile: profile show [name]", ReadOnly: true, Run: runProfileShow})
	register(Command{Path: []string{"profile", "set"},
		Summary: "set your profile: profile set [--description <d>] [--website <url>] ('' clears)", Run: runProfileSet})
	register(Command{Path: []string{"org", "profile"},
		Summary: "show or set an org's profile: org profile <org> [--description <d>] [--website <url>]", Run: runOrgProfile})
}

// parseProfileFlags pulls --description/--website out of args; a flag given
// with an empty value clears the field, an absent flag leaves it untouched.
func parseProfileFlags(args []string) (rest []string, desc, site *string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--description", "--website":
			if i+1 >= len(args) {
				return nil, nil, nil, fmt.Errorf("%s requires a value", args[i])
			}
			v := args[i+1]
			if args[i] == "--description" {
				desc = &v
			} else {
				site = &v
			}
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, desc, site, nil
}

func validateWebsite(url string) error {
	if url == "" || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return nil
	}
	return errors.New("website must start with https:// or http://")
}

func applyProfile(p store.Profile, desc, site *string) (store.Profile, error) {
	if desc != nil {
		d, _, _ := strings.Cut(strings.TrimSpace(*desc), "\n")
		if len(d) > 256 {
			d = d[:256]
		}
		p.Description = d
	}
	if site != nil {
		s := strings.TrimSpace(*site)
		if err := validateWebsite(s); err != nil {
			return p, err
		}
		p.Website = s
	}
	return p, nil
}

type profileOut struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	// The rest is what a profile page shows: who they work with, what
	// they own that you can see, and how active they have been. The web
	// read these straight out of the store, which kept them off every
	// other surface.
	Orgs     []profileMember `json:"orgs,omitempty"`    // for a user
	Members  []profileMember `json:"members,omitempty"` // for an org
	Repos    []profileRepo   `json:"repos"`
	Activity []activityDay   `json:"activity,omitempty"`
	// ActivityTotal counts the same window the days cover.
	ActivityTotal int `json:"activity_total"`
}

type profileMember struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

type profileRepo struct {
	Path        string `json:"path"`
	Visibility  string `json:"visibility"`
	Description string `json:"description,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
}

// activityDay is one day's contribution count. Days with nothing are
// omitted; a client fills the calendar it wants to draw.
type activityDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// ActivityWindow is the span a profile reports: the start of the web's
// 53-week calendar, so every surface shows the same year.
func ActivityWindow() string {
	today := time.Now().UTC()
	end := today.AddDate(0, 0, int(time.Saturday-today.Weekday()))
	return end.AddDate(0, 0, -53*7+1).Format("2006-01-02")
}

func emitProfile(c *Ctx, d profileOut) int {
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s (%s)\n", d.Name, d.Kind)
		if d.Description != "" {
			fmt.Fprintf(w, "%s\n", d.Description)
		}
		if d.Website != "" {
			fmt.Fprintf(w, "%s\n", d.Website)
		}
		for _, m := range d.Orgs {
			fmt.Fprintf(w, "org\t%s\t%s\n", m.Name, m.Role)
		}
		for _, m := range d.Members {
			fmt.Fprintf(w, "member\t%s\t%s\n", m.Name, m.Role)
		}
		for _, r := range d.Repos {
			fmt.Fprintf(w, "repo\t%s\t%s\t%s\n", r.Path, r.Visibility, r.Description)
		}
		if d.ActivityTotal > 0 {
			fmt.Fprintf(w, "activity\t%d in the last year\n", d.ActivityTotal)
		}
	})
}

func runProfileShow(c *Ctx, args []string) int {
	name := c.User.Username
	if len(args) == 1 {
		name = args[0]
	} else if len(args) > 1 {
		return c.fail(protocol.ExitUsage, "usage: profile show [name]")
	}
	kind, id := "", int64(0)
	if u, err := c.Store.UserByUsername(name); err == nil {
		kind, id = "user", u.ID
	} else if o, err := c.Store.OrgByName(name); err == nil {
		kind, id = "org", o.ID
	} else {
		return c.fail(protocol.ExitNotFound, "no user or organization %q", name)
	}
	p, err := c.Store.OwnerProfile(kind, id)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	d := profileOut{Name: name, Kind: kind, Description: p.Description, Website: p.Website,
		Repos: []profileRepo{}}

	// Who they work with. Both lists are public on a profile — the web
	// has always shown them — and neither exposes anything a member
	// listing would not.
	if kind == "user" {
		orgs, err := c.Store.ListOrgsForUser(id)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		for _, o := range orgs {
			d.Orgs = append(d.Orgs, profileMember{Name: o.Username, Role: o.Role})
		}
	} else {
		members, err := c.Store.OrgMembers(id)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		for _, m := range members {
			d.Members = append(d.Members, profileMember{Name: m.Username, Role: m.Role})
		}
	}

	// Only repositories this caller may read: a private repo must not
	// surface on a profile any more than it does in a listing.
	all, err := c.Store.ListReposForOwner(kind, id)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, repo := range all {
		grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !policy.CanRead(c.User, repo, grant) {
			continue
		}
		d.Repos = append(d.Repos, profileRepo{
			Path:        repo.Path(),
			Visibility:  repo.Visibility,
			Description: gitutil.ReadDescription(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)),
			Archived:    repo.Settings.Archived,
		})
	}

	var counts map[string]int
	if kind == "user" {
		counts, err = c.Store.ActivityByDay(id, ActivityWindow())
	} else {
		counts, err = c.Store.OrgActivityByDay(id, ActivityWindow())
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	days := make([]string, 0, len(counts))
	for day := range counts {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		d.Activity = append(d.Activity, activityDay{Date: day, Count: counts[day]})
		d.ActivityTotal += counts[day]
	}
	return emitProfile(c, d)
}

func runProfileSet(c *Ctx, args []string) int {
	rest, desc, site, err := parseProfileFlags(args)
	if err != nil || len(rest) != 0 {
		return c.fail(protocol.ExitUsage, "usage: profile set [--description <d>] [--website <url>]")
	}
	if desc == nil && site == nil {
		return c.fail(protocol.ExitUsage, "nothing to set: pass --description and/or --website")
	}
	p, err := c.Store.OwnerProfile("user", c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	p, err = applyProfile(p, desc, site)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := c.Store.SetOwnerProfile("user", c.User.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, profileOut{Name: c.User.Username, Kind: "user",
		Description: p.Description, Website: p.Website, Repos: []profileRepo{}})
}

func runOrgProfile(c *Ctx, args []string) int {
	rest, desc, site, err := parseProfileFlags(args)
	if err != nil || len(rest) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org profile <org> [--description <d>] [--website <url>]")
	}
	name := rest[0]
	if desc == nil && site == nil {
		return runProfileShow(c, []string{name})
	}
	org, code := orgAdmin(c, name)
	if code >= 0 {
		return code
	}
	p, err := c.Store.OwnerProfile("org", org.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	p, err = applyProfile(p, desc, site)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := c.Store.SetOwnerProfile("org", org.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, profileOut{Name: org.Name, Kind: "org",
		Description: p.Description, Website: p.Website, Repos: []profileRepo{}})
}
