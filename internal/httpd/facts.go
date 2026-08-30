package httpd

import (
	"fmt"
	"sort"

	"gitbay.org/gitbay/internal/control"
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
	Email   string // the account's busiest address, when several collapsed
	Commits int
}

// Title is the hover text: which address the commits carry, and how many.
func (c factContributor) Title() string {
	if c.Commits == 1 {
		return c.Email + " · 1 commit"
	}
	return fmt.Sprintf("%s · %d commits", c.Email, c.Commits)
}

const maxContributors = 12

// collapseContributors folds the addresses of one account into a single
// row. .mailmap has already merged whatever the repository claims about
// its own history; this merges what an account has proven here, which a
// repository cannot know about. Addresses with no account stay distinct —
// two people sharing a git name are not one contributor.
func (s *Server) collapseContributors(cs []gitutil.Contributor) []factContributor {
	names := s.authorNames()
	var out []factContributor
	byUser := map[string]int{}
	for _, c := range cs {
		user, known := names.account(c.Email)
		if known {
			if i, seen := byUser[user]; seen {
				out[i].Commits += c.Commits
				continue
			}
			byUser[user] = len(out)
		}
		out = append(out, factContributor{
			Name: names.name(c.Email, c.Name), User: user, Email: c.Email, Commits: c.Commits,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Commits > out[j].Commits })
	if len(out) > maxContributors {
		out = out[:maxContributors]
	}
	return out
}

func (s *Server) factsFor(p repoPage) repoFacts {
	f := repoFacts{
		Commits: gitutil.CountCommits(p.Dir, p.Ref),
		License: control.DetectLicense(p.Dir, p.Ref),
	}
	if heads, err := gitutil.Refs(p.Dir, "heads"); err == nil {
		f.Branches = len(heads)
	}
	if tags, err := gitutil.Refs(p.Dir, "tags"); err == nil {
		f.Tags = len(tags)
	}
	f.Languages = gitutil.Languages(p.Dir, p.Ref, langOf)

	f.Contributors = s.collapseContributors(gitutil.Contributors(p.Dir, p.Ref))

	if rels, err := s.st.ListReleases(p.Repo.ID); err == nil && len(rels) > 0 {
		f.Release = rels[0].Tag
	}
	if builds, err := s.st.ListBuilds(p.Repo.ID, 1); err == nil && len(builds) > 0 {
		f.Build = builds[0].Status
	}
	return f
}
