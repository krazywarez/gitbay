package httpd

import (
	"net/http"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
)

// compare renders what head adds on top of base: the commits in
// base..head and the diff from their merge base, the same range a merge
// request shows before it exists (#118). /compare/{base}...{head} is the
// link form; /compare?base=&head= is the form on the refs page.
func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "refs"
	base, head := r.URL.Query().Get("base"), r.URL.Query().Get("head")
	if rng := r.PathValue("range"); rng != "" {
		if b, h, ok := strings.Cut(rng, "..."); ok {
			base, head = b, h
		} else if b, h, ok := strings.Cut(rng, ".."); ok {
			base, head = b, h
		}
	}
	if base == "" {
		base = p.Repo.DefaultBranch
	}
	if head == "" {
		http.Redirect(w, r, "/"+p.Repo.Path()+"/refs", http.StatusSeeOther)
		return
	}
	baseSHA, err := gitutil.ResolveRef(p.Dir, base)
	if err != nil {
		s.notFound(w, r)
		return
	}
	headSHA, err := gitutil.ResolveRef(p.Dir, head)
	if err != nil {
		s.notFound(w, r)
		return
	}
	mergeBase, err := gitutil.MergeBase(p.Dir, baseSHA, headSHA)
	if err != nil {
		s.notFound(w, r)
		return
	}
	patch, truncated, _ := gitutil.Diff(p.Dir, mergeBase, headSHA, 4<<20)
	files := parseDiff(patch)
	type row struct {
		SHA, ShortSHA, Subject, AuthorName, AuthorUser, Date string
		Sig                                                  sigView
	}
	names := s.authorNames()
	var commits []row
	shas, _ := gitutil.RevListRange(p.Dir, mergeBase, headSHA)
	const maxCommits = 100
	total := len(shas)
	if len(shas) > maxCommits {
		shas = shas[:maxCommits]
	}
	for _, sha := range shas {
		v, parsed := s.sigFor(p.Repo, p.Dir, sha)
		cr := row{SHA: sha, ShortSHA: sha[:10], Sig: v}
		if parsed != nil {
			cr.Subject = parsed.Subject
			cr.AuthorName = names.name(parsed.AuthorEmail, parsed.AuthorName)
			cr.AuthorUser, _ = names.account(parsed.AuthorEmail)
			cr.Date = time.Unix(parsed.AuthorUnix, 0).UTC().Format("2006-01-02")
		}
		commits = append(commits, cr)
	}
	s.render(w, "compare.html", struct {
		repoPage
		Base, Head, BaseSHA, HeadSHA, MergeBase string
		Commits                                 []row
		CommitsTotal                            int
		DiffFiles                               []diffFile
		DiffTruncated                           bool
		Stat                                    diffStat
		CanWrite                                bool
	}{p, base, head, baseSHA, headSHA, mergeBase, commits, total, files, truncated, statOf(files), s.canWriteRepo(r, p.Repo)})
}
