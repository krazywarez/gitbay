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
		Summary: "set your profile: profile set [--description <d>] [--website <url>] [--about <text>|--file -] [--about-format md|org] [--link <label|url>]... ('' clears)", ReadsStdin: true, Run: runProfileSet})
	register(Command{Path: []string{"org", "profile"},
		Summary: "show or set an org's profile: org profile <org> [--description <d>] [--website <url>] [--about <text>|--file -] [--about-format md|org] [--link <label|url>]...", ReadsStdin: true, Run: runOrgProfile})
}

// maxProfileLinks caps the free-form link list. A profile is a header,
// not a linktree.
const maxProfileLinks = 5

// profileEdit is the set of profile fields a command may change. A nil
// field is left alone; an empty value clears it.
type profileEdit struct {
	Description *string
	Website     *string
	About       *string
	AboutFormat *string
	Links       *[]store.ProfileLink
}

func (e profileEdit) empty() bool {
	return e.Description == nil && e.Website == nil && e.About == nil &&
		e.AboutFormat == nil && e.Links == nil
}

// parseProfileFlags pulls the profile flags out of args. --about takes
// inline text or reads stdin via --file -; --link repeats, and a single
// empty --link clears the list.
func parseProfileFlags(c *Ctx, args []string) (rest []string, e profileEdit, err error) {
	about, file := "", ""
	sawAbout := false
	var links []store.ProfileLink
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--description", "--website", "--about", "--about-format", "--file", "--link":
			if i+1 >= len(args) {
				return nil, e, fmt.Errorf("%s requires a value", args[i])
			}
			v := args[i+1]
			switch args[i] {
			case "--description":
				e.Description = &v
			case "--website":
				e.Website = &v
			case "--about":
				about, sawAbout = v, true
			case "--about-format":
				e.AboutFormat = &v
			case "--file":
				file, sawAbout = v, true
			case "--link":
				if v == "" {
					links = nil
					e.Links = &links
					break
				}
				l, lerr := parseProfileLink(v)
				if lerr != nil {
					return nil, e, lerr
				}
				links = append(links, l)
				e.Links = &links
			}
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if sawAbout {
		body, berr := bodyFrom(c, about, file)
		if berr != nil {
			return nil, e, berr
		}
		e.About = &body
	}
	if len(links) > maxProfileLinks {
		return nil, e, fmt.Errorf("at most %d links", maxProfileLinks)
	}
	return rest, e, nil
}

// parseProfileLink splits "label|url"; without a separator the whole
// value is the URL.
func parseProfileLink(v string) (store.ProfileLink, error) {
	label, url, ok := strings.Cut(v, "|")
	if !ok {
		label, url = "", v
	}
	label = strings.TrimSpace(label)
	if len(label) > 32 {
		label = label[:32]
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return store.ProfileLink{}, errors.New("a link needs a url")
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return store.ProfileLink{}, errors.New("link url must start with https:// or http://")
	}
	return store.ProfileLink{Label: label, URL: url}, nil
}

func validateWebsite(url string) error {
	if url == "" || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return nil
	}
	return errors.New("website must start with https:// or http://")
}

func applyProfile(p store.Profile, e profileEdit) (store.Profile, error) {
	if e.Description != nil {
		d, _, _ := strings.Cut(strings.TrimSpace(*e.Description), "\n")
		if len(d) > 256 {
			d = d[:256]
		}
		p.Description = d
	}
	if e.Website != nil {
		s := strings.TrimSpace(*e.Website)
		if err := validateWebsite(s); err != nil {
			return p, err
		}
		p.Website = s
	}
	if e.About != nil {
		p.About = strings.TrimSpace(*e.About)
	}
	if e.AboutFormat != nil {
		f := strings.TrimSpace(*e.AboutFormat)
		if f != "md" && f != "org" {
			return p, errors.New("about format must be md or org")
		}
		p.AboutFormat = f
	}
	if e.Links != nil {
		p.Links = *e.Links
	}
	return p, nil
}

type profileOut struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	// About is long-form markdown, rendered by the web between the
	// header and the activity graph.
	About       string              `json:"about,omitempty"`
	AboutFormat string              `json:"about_format,omitempty"`
	Links       []store.ProfileLink `json:"links,omitempty"`
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
		for _, l := range d.Links {
			fmt.Fprintf(w, "link\t%s\t%s\n", l.Label, l.URL)
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
		if d.About != "" {
			fmt.Fprintf(w, "\n%s\n", d.About)
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
		About: p.About, AboutFormat: p.AboutFormat, Links: p.Links, Repos: []profileRepo{}}

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
	rest, e, err := parseProfileFlags(c, args)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(rest) != 0 {
		return c.fail(protocol.ExitUsage,
			"usage: profile set [--description <d>] [--website <url>] [--about <text>|--file -] [--about-format md|org] [--link <label|url>]...")
	}
	if e.empty() {
		return c.fail(protocol.ExitUsage, "nothing to set: pass --description, --website, --about and/or --link")
	}
	p, err := c.Store.OwnerProfile("user", c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	p, err = applyProfile(p, e)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := c.Store.SetOwnerProfile("user", c.User.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, profileOut{Name: c.User.Username, Kind: "user",
		Description: p.Description, Website: p.Website, About: p.About,
		AboutFormat: p.AboutFormat, Links: p.Links, Repos: []profileRepo{}})
}

func runOrgProfile(c *Ctx, args []string) int {
	rest, e, err := parseProfileFlags(c, args)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(rest) != 1 {
		return c.fail(protocol.ExitUsage,
			"usage: org profile <org> [--description <d>] [--website <url>] [--about <text>|--file -] [--about-format md|org] [--link <label|url>]...")
	}
	name := rest[0]
	if e.empty() {
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
	p, err = applyProfile(p, e)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := c.Store.SetOwnerProfile("org", org.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, profileOut{Name: org.Name, Kind: "org",
		Description: p.Description, Website: p.Website, About: p.About,
		AboutFormat: p.AboutFormat, Links: p.Links, Repos: []profileRepo{}})
}
