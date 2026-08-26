package httpd

import (
	"gitbay.org/gitbay/internal/gitutil"
)

// repoFacts is the "what is this repository" summary on a repo home: the
// counts a visitor uses to size up a project before reading any code.
type repoFacts struct {
	Commits      int
	Branches     int
	Tags         int
	Contributors []factContributor
	Languages    []gitutil.Language
	License      string
	Release      string // newest release tag, if any
	Build        string // latest build status on the default branch
}

type factContributor struct {
	Name    string
	User    string // account, when the email is verified here
	Email   string
	Commits int
}

const maxContributors = 12

func (s *Server) factsFor(p repoPage) repoFacts {
	f := repoFacts{
		Commits: gitutil.CountCommits(p.Dir, p.Ref),
		License: detectLicense(p.Dir, p.Ref),
	}
	if heads, err := gitutil.Refs(p.Dir, "heads"); err == nil {
		f.Branches = len(heads)
	}
	if tags, err := gitutil.Refs(p.Dir, "tags"); err == nil {
		f.Tags = len(tags)
	}
	f.Languages = gitutil.Languages(p.Dir, p.Ref, langOf)

	names := s.authorNames()
	for _, c := range gitutil.Contributors(p.Dir, p.Ref, maxContributors) {
		user, _ := names.account(c.Email)
		f.Contributors = append(f.Contributors, factContributor{
			Name: names.name(c.Email, c.Name), User: user, Email: c.Email, Commits: c.Commits,
		})
	}

	if rels, err := s.st.ListReleases(p.Repo.ID); err == nil && len(rels) > 0 {
		f.Release = rels[0].Tag
	}
	if builds, err := s.st.ListBuilds(p.Repo.ID, 1); err == nil && len(builds) > 0 {
		f.Build = builds[0].Status
	}
	return f
}
