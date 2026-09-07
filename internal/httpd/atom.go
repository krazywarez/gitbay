package httpd

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/sig"
	"gitbay.org/gitbay/internal/store"
)

// Atom feeds (#192): a repository's releases and commits, and an owner's
// public activity. Renderers over rows other surfaces already serve;
// nothing is stored for them. A feed reader carries no session, so the
// owner feed covers public repositories only, and a private repository
// answers 404 as its pages do.

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	ID      string       `xml:"id"`
	Title   string       `xml:"title"`
	Updated string       `xml:"updated"`
	Author  *atomAuthor  `xml:"author,omitempty"`
	Link    atomLink     `xml:"link"`
	Content *atomContent `xml:"content,omitempty"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

const atomLimit = 50

func (s *Server) site() string { return strings.TrimSuffix(s.cfg.Server.SiteURL, "/") }

// writeAtom serialises the feed. Updated falls back to the newest entry,
// then to now: a feed with no entries is still a feed.
func (s *Server) writeAtom(w http.ResponseWriter, path, title string, entries []atomEntry) {
	f := atomFeed{ID: s.site() + path, Title: title, Entries: entries,
		Links: []atomLink{
			{Rel: "self", Href: s.site() + path, Type: "application/atom+xml"},
			{Rel: "alternate", Href: s.site() + strings.TrimSuffix(strings.TrimSuffix(path, ".atom"), "/activity")},
		}}
	if len(entries) > 0 {
		f.Updated = entries[0].Updated
	} else {
		f.Updated = time.Now().UTC().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprint(w, xml.Header)
	xml.NewEncoder(w).Encode(f)
}

func (s *Server) releasesAtom(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	rels, err := s.st.ListReleases(p.Repo.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	base := "/" + p.Repo.Path()
	var entries []atomEntry
	for _, rel := range rels {
		title := rel.Tag
		if rel.Title != "" {
			title = rel.Tag + ": " + rel.Title
		}
		e := atomEntry{ID: s.site() + base + "/releases#" + rel.Tag, Title: title, Updated: rel.CreatedAt,
			Link: atomLink{Rel: "alternate", Href: s.site() + base + "/releases#" + rel.Tag}}
		if rel.Author != "" {
			e.Author = &atomAuthor{Name: rel.Author}
		}
		if rel.Notes != "" {
			e.Content = &atomContent{Type: "text", Text: rel.Notes}
		}
		entries = append(entries, e)
	}
	s.writeAtom(w, base+"/releases.atom", p.Repo.Path()+" releases", entries)
}

func (s *Server) logAtom(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
	shas, err := gitutil.RevList(p.Dir, p.Ref, atomLimit)
	if err != nil {
		s.notFound(w, r)
		return
	}
	base := "/" + p.Repo.Path()
	var entries []atomEntry
	for _, sha := range shas {
		e := atomEntry{ID: s.site() + base + "/commit/" + sha, Title: sha[:10],
			Link: atomLink{Rel: "alternate", Href: s.site() + base + "/commit/" + sha}}
		if raw, err := gitutil.ReadCommit(p.Dir, sha); err == nil {
			if c, err := sig.ParseCommit(raw); err == nil {
				e.Title = c.Subject
				e.Updated = time.Unix(c.AuthorUnix, 0).UTC().Format(time.RFC3339)
				e.Author = &atomAuthor{Name: c.AuthorName}
			}
		}
		entries = append(entries, e)
	}
	path := base + "/log.atom"
	if r.PathValue("ref") != "" {
		path += "/" + p.Ref
	}
	s.writeAtom(w, path, p.Repo.Path()+" commits on "+p.Ref, entries)
}

// ownerAtom is an owner's activity on their public repositories, the
// feed command's rows without a viewer.
func (s *Server) ownerAtom(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("owner")
	kind, id := "", int64(0)
	if u, err := s.st.UserByUsername(name); err == nil {
		kind, id = "user", u.ID
	} else if o, err := s.st.OrgByName(name); err == nil {
		kind, id = "org", o.ID
	} else {
		s.notFound(w, r)
		return
	}
	events, err := s.st.OwnerPublicEvents(kind, id, atomLimit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var entries []atomEntry
	for _, ev := range events {
		title, link := eventTitle(ev)
		e := atomEntry{ID: fmt.Sprintf("%s/%s/activity.atom#%d", s.site(), name, ev.ID), Title: title, Updated: ev.CreatedAt,
			Link: atomLink{Rel: "alternate", Href: s.site() + link}}
		if ev.Actor != "" {
			e.Author = &atomAuthor{Name: ev.Actor}
		}
		entries = append(entries, e)
	}
	s.writeAtom(w, "/"+name+"/activity.atom", name+" activity", entries)
}

// eventTitle names an event and where it points, from the kind and the
// number or tag its data carries.
func eventTitle(ev store.FeedEvent) (title, link string) {
	var data struct {
		Number int64  `json:"number"`
		Tag    string `json:"tag"`
	}
	json.Unmarshal([]byte(ev.Data), &data)
	link = "/" + ev.RepoPath
	title = ev.Kind + " in " + ev.RepoPath
	switch {
	case strings.HasPrefix(ev.Kind, "issue.") && data.Number > 0:
		link += fmt.Sprintf("/issues/%d", data.Number)
		title = fmt.Sprintf("%s %s#%d", ev.Kind, ev.RepoPath, data.Number)
	case strings.HasPrefix(ev.Kind, "mr.") && data.Number > 0:
		link += fmt.Sprintf("/mrs/%d", data.Number)
		title = fmt.Sprintf("%s %s!%d", ev.Kind, ev.RepoPath, data.Number)
	case strings.HasPrefix(ev.Kind, "release.") && data.Tag != "":
		link += "/releases#" + data.Tag
		title = fmt.Sprintf("%s %s %s", ev.Kind, ev.RepoPath, data.Tag)
	}
	if ev.Actor != "" {
		title = ev.Actor + ": " + title
	}
	return title, link
}
