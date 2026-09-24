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
		Summary:  "show a user's or org's profile",
		Usage:    "profile show [name]",
		Examples: []string{"profile show cmc"}, ReadOnly: true, Run: runProfileShow})
	register(Command{Path: []string{"profile", "set"},
		Summary: "set your profile",
		Usage:   "profile set [--description <d>] [--website <url>] [--link <label|url>]... ('' clears)",
		Flags: []Flag{
			{"--description", "<d>", "one line about you", ""},
			{"--website", "<url>", "your website", ""},
			{"--link", "<label|url>", "a profile link, may repeat", ""},
		},
		Examples: []string{`profile set --description "gitbay's author" --website https://cleberg.net`}, Run: runProfileSet})
	register(Command{Path: []string{"org", "profile"},
		Summary: "show or set an org's profile",
		Usage:   "org profile <org> [--description <d>] [--website <url>] [--link <label|url>]...",
		Flags: []Flag{
			{"--description", "<d>", "one line about the org", ""},
			{"--website", "<url>", "the org's website", ""},
			{"--link", "<label|url>", "a profile link, may repeat", ""},
		},
		Examples: []string{"org profile krz", `org profile krz --description "a self-hosted forge"`}, Run: runOrgProfile})
}

// maxProfileLinks caps the free-form link list. A profile is a header,
// not a linktree.
const maxProfileLinks = 5

// ProfileRepoName is the repository that holds an owner's profile
// content. A dot-repo because it is infrastructure rather than a
// project: later per-owner configuration goes beside the about text,
// and the leading dot keeps it out of the listings.
const ProfileRepoName = ".gitbay"

// AboutBase is the about file's path in that repository, without its
// extension.
const AboutBase = "profile/README"

// aboutExts are the formats the about is read from, in resolution order
// — the wiki's order, for the same reason.
var aboutExts = []string{".md", ".org", ".markdown"}

// ownerAbout reads an owner's about text from <owner>/.gitbay. Anything
// missing — the repository, the branch, the file — is an empty about,
// and so is a repository this caller cannot read: a profile must not
// confirm a private namespace. path is the file it came from, so a
// client can link to it instead of guessing the extension.
func ownerAbout(c *Ctx, owner string) (text, format, path string) {
	repo, err := c.Store.RepoByPath(owner + "/" + ProfileRepoName)
	if err != nil {
		return "", "", ""
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil || !policy.CanRead(c.User, repo, grant) {
		return "", "", ""
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	for _, ext := range aboutExts {
		raw, err := gitutil.ReadBlob(dir, repo.DefaultBranch, AboutBase+ext, maxCommitFileBytes)
		if err != nil || len(raw) == 0 {
			continue
		}
		f := "md"
		if ext == ".org" {
			f = "org"
		}
		return string(raw), f, AboutBase + ext
	}
	return "", "", ""
}

// profileEdit is the set of profile fields a command may change. A nil
// field is left alone; an empty value clears it.
type profileEdit struct {
	Description *string
	Website     *string
	Links       *[]store.ProfileLink
}

func (e profileEdit) empty() bool {
	return e.Description == nil && e.Website == nil && e.Links == nil
}

// parseProfileFlags pulls the profile flags out of args. --link repeats,
// and a single empty --link clears the list. The about text is not here:
// it is a file in <owner>/.gitbay, written like any other file.
func parseProfileFlags(args []string) (rest []string, e profileEdit, err error) {
	var links []store.ProfileLink
	f, err := parseFlags(args, flagSpec{Values: []string{"--description", "--website"}, Multi: []string{"--link"}, MaxPos: -1})
	if err != nil {
		return nil, e, err
	}
	rest = f.Pos
	for _, name := range []string{"--description", "--website"} {
		if !f.Has(name) {
			continue
		}
		v := f.Value(name)
		switch name {
		case "--description":
			e.Description = &v
		case "--website":
			e.Website = &v
		}
	}
	for _, v := range f.List("--link") {
		if v == "" {
			links = nil
			e.Links = &links
			continue
		}
		l, lerr := parseProfileLink(v)
		if lerr != nil {
			return nil, e, lerr
		}
		links = append(links, l)
		e.Links = &links
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
	if e.Links != nil {
		p.Links = *e.Links
	}
	return p, nil
}

type ProfileOut struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
	// About is the long-form text from <owner>/.gitbay, rendered by the
	// web between the header and the activity graph.
	About       string `json:"about,omitempty"`
	AboutFormat string `json:"about_format,omitempty"`
	// AboutPath is where the about was read from in <owner>/.gitbay, so a
	// client can link to the file rather than guess its extension.
	AboutPath string              `json:"about_path,omitempty"`
	Links     []store.ProfileLink `json:"links,omitempty"`
	// The rest is what a profile page shows: who they work with, what
	// they own that you can see, and how active they have been. The web
	// read these straight out of the store, which kept them off every
	// other surface.
	Orgs    []ProfileMember `json:"orgs,omitempty"`    // for a user
	Members []ProfileMember `json:"members,omitempty"` // for an org
	Repos   []ProfileRepo   `json:"repos"`
	// Snippets counts the owner's snippets the caller may list: public
	// ones, or all of them for the owner and admins. Orgs own none.
	Snippets int           `json:"snippets"`
	Activity []ActivityDay `json:"activity,omitempty"`
	// ActivityTotal counts the same window the days cover.
	ActivityTotal int `json:"activity_total"`
}

type ProfileMember struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// ProfileRepo is one repository as a profile lists it. The listing
// metadata — topics, license, last commit — is here because a profile is
// a listing: a client that renders repositories without it is showing
// less than the web does, which is why the web kept its own copy.
type ProfileRepo struct {
	Path          string   `json:"path"`
	Visibility    string   `json:"visibility"`
	Description   string   `json:"description,omitempty"`
	DefaultBranch string   `json:"default_branch"`
	Topics        []string `json:"topics,omitempty"`
	License       string   `json:"license,omitempty"`
	Updated       string   `json:"updated,omitempty"`
	Archived      bool     `json:"archived,omitempty"`
}

// ActivityDay is one day's contribution count. Days with nothing are
// omitted; a client fills the calendar it wants to draw.
type ActivityDay struct {
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

func emitProfile(c *Ctx, d ProfileOut) int {
	return c.emit(d, func(w io.Writer) {
		activity := ""
		if d.ActivityTotal > 0 {
			activity = fmt.Sprintf("%d in the last year", d.ActivityTotal)
		}
		v := c.view(w)
		v.title(d.Name, d.Description, d.Kind)
		v.fields(
			"website", d.Website,
			"url", c.siteURL(d.Name),
			"activity", activity,
		)
		if len(d.Links) > 0 {
			v.section("link")
			tb := c.table(w, "LINK", "URL")
			for _, l := range d.Links {
				tb.row(cText(l.Label), cFlex(l.URL))
			}
			tb.flush()
		}
		if len(d.Orgs) > 0 {
			v.section("org")
			tb := c.table(w, "ORG", "ROLE")
			for _, m := range d.Orgs {
				tb.row(cRef(m.Name), cState(m.Role))
			}
			tb.flush()
		}
		if len(d.Members) > 0 {
			v.section("member")
			tb := c.table(w, "MEMBER", "ROLE")
			for _, m := range d.Members {
				tb.row(cRef(m.Name), cState(m.Role))
			}
			tb.flush()
		}
		if len(d.Repos) > 0 {
			v.section("repo")
			tb := c.table(w, "REPO", "VISIBILITY", "DESCRIPTION")
			for _, r := range d.Repos {
				tb.row(cRef(r.Path), cState(r.Visibility), cFlex(r.Description))
			}
			tb.flush()
		}
		v.body(d.About, d.AboutFormat)
	})
}

func runProfileShow(c *Ctx, args []string) int {
	name := c.User.Username
	if len(args) == 1 {
		name = args[0]
	} else if len(args) > 1 {
		return c.usage()
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
	about, aboutFormat, aboutPath := ownerAbout(c, name)
	d := ProfileOut{Name: name, Kind: kind, Description: p.Description, Website: p.Website,
		About: about, AboutFormat: aboutFormat, AboutPath: aboutPath,
		Links: p.Links, Repos: []ProfileRepo{}}

	// Who they work with. Both lists are public on a profile — the web
	// has always shown them — and neither exposes anything a member
	// listing would not.
	if kind == "user" {
		orgs, err := c.Store.ListOrgsForUser(id)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		for _, o := range orgs {
			d.Orgs = append(d.Orgs, ProfileMember{Name: o.Username, Role: o.Role})
		}
	} else {
		members, err := c.Store.OrgMembers(id)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		for _, m := range members {
			d.Members = append(d.Members, ProfileMember{Name: m.Username, Role: m.Role})
		}
	}

	// Only repositories this caller may read: a private repo must not
	// surface on a profile any more than it does in a listing.
	all, err := c.Store.ListReposForOwner(kind, id)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	for _, repo := range all {
		// A dot-repo is infrastructure, not a project: .gitbay holds this
		// profile's about text and does not belong in its listing.
		if strings.HasPrefix(repo.Name, ".") {
			continue
		}
		grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if !policy.CanRead(c.User, repo, grant) {
			continue
		}
		dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
		topics, err := c.Store.ListTopics(repo.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		d.Repos = append(d.Repos, ProfileRepo{
			Path:          repo.Path(),
			Visibility:    repo.Visibility,
			Description:   gitutil.ReadDescription(dir),
			DefaultBranch: repo.DefaultBranch,
			Topics:        topics,
			License:       DetectLicense(dir, repo.DefaultBranch),
			Updated:       gitutil.LastCommitDate(dir, repo.DefaultBranch),
			Archived:      repo.Settings.Archived,
		})
	}

	if kind == "user" {
		seeAll := id == c.User.ID || c.User.IsAdmin
		if d.Snippets, err = c.Store.CountSnippets(id, seeAll); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
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
		d.Activity = append(d.Activity, ActivityDay{Date: day, Count: counts[day]})
		d.ActivityTotal += counts[day]
	}
	return emitProfile(c, d)
}

func runProfileSet(c *Ctx, args []string) int {
	rest, e, err := parseProfileFlags(args)
	if err != nil {
		return c.failInput(err)
	}
	if len(rest) != 0 {
		return c.usage()
	}
	if e.empty() {
		return c.fail(protocol.ExitUsage, "nothing to set: pass --description, --website and/or --link")
	}
	p, err := c.Store.OwnerProfile("user", c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	p, err = applyProfile(p, e)
	if err != nil {
		return c.failInput(err)
	}
	if err := c.Store.SetOwnerProfile("user", c.User.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, ProfileOut{Name: c.User.Username, Kind: "user",
		Description: p.Description, Website: p.Website, Links: p.Links,
		Repos: []ProfileRepo{}})
}

func runOrgProfile(c *Ctx, args []string) int {
	rest, e, err := parseProfileFlags(args)
	if err != nil {
		return c.failInput(err)
	}
	if len(rest) != 1 {
		return c.usage()
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
		return c.failInput(err)
	}
	if err := c.Store.SetOwnerProfile("org", org.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, ProfileOut{Name: org.Name, Kind: "org",
		Description: p.Description, Website: p.Website, Links: p.Links,
		Repos: []ProfileRepo{}})
}
