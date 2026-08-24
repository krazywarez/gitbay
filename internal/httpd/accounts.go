package httpd

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

const sessionCookie = "gitbay_session"

// viewer returns the logged-in user, or a zero User for anonymous visitors.
// Only meaningful in accounts mode; in view_only no session route exists so
// every request is anonymous.
func (s *Server) viewer(r *http.Request) store.User {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return store.User{}
	}
	u, err := s.st.WebSessionUser(store.HashToken(ck.Value))
	if err != nil {
		return store.User{}
	}
	return u
}

// requireUser wraps a handler that needs a session.
func (s *Server) requireUser(h func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.viewer(r)
		if u.ID == 0 {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r, u)
	}
}

// checkOrigin rejects cross-site POSTs. Sessions also use SameSite=Strict;
// this is the second layer.
func (s *Server) checkOrigin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			host := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
			if host != r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		h(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		s.render(w, "login.html", struct {
			Site  string
			Error string
		}{s.siteName(), ""})
		return
	}
	userID, err := s.st.ConsumeLoginToken(store.HashToken(token))
	if err != nil {
		s.render(w, "login.html", struct {
			Site  string
			Error string
		}{s.siteName(), "that login link is invalid, expired, or already used — mint a new one"})
		return
	}
	sessTok, sessHash, err := store.NewToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.st.CreateWebSession(sessHash, userID, 7*24*time.Hour); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sessTok, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.cfg.HTTP.TLS != "off",
		MaxAge: 7 * 24 * 3600,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		s.st.DeleteWebSession(store.HashToken(ck.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) newRepoForm(w http.ResponseWriter, r *http.Request, u store.User) {
	s.render(w, "new.html", struct {
		Site   string
		Viewer string
		Error  string
	}{s.siteName(), u.Username, ""})
}

func (s *Server) newRepoSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	name := r.FormValue("name")
	visibility := "public"
	if r.FormValue("visibility") == "private" {
		visibility = "private"
	}
	fail := func(msg string) {
		s.render(w, "new.html", struct {
			Site   string
			Viewer string
			Error  string
		}{s.siteName(), u.Username, msg})
	}
	if err := policy.ValidateName(name); err != nil {
		fail(err.Error())
		return
	}
	id, err := s.st.CreateRepo("user", u.ID, name, visibility)
	if err != nil {
		fail(err.Error())
		return
	}
	dir := control.RepoDir(s.cfg.Server.Root, u.Username, name)
	if err := gitutil.InitBare(dir, "main", control.HooksDir(s.cfg.Server.Root)); err != nil {
		s.st.DeleteRepo(id)
		fail("initializing repository failed")
		return
	}
	http.Redirect(w, r, "/"+u.Username+"/"+name, http.StatusSeeOther)
}

// repoForUser is repoFor with a write/read permission requirement for a
// logged-in user.
func (s *Server) repoForUser(w http.ResponseWriter, r *http.Request, u store.User,
	perm func(store.User, store.Repo, string) bool) (store.Repo, bool) {
	repo, err := s.st.RepoByPath(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if err != nil {
		http.NotFound(w, r)
		return store.Repo{}, false
	}
	grant, err := s.st.AccessRole(repo.ID, u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return store.Repo{}, false
	}
	if !policy.CanRead(u, repo, grant) {
		http.NotFound(w, r) // invisible: same as nonexistent
		return store.Repo{}, false
	}
	if !perm(u, repo, grant) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return store.Repo{}, false
	}
	return repo, true
}

// issueCreateForm renders the new-issue form, prefilled from the repo's
// default issue template when one exists.
func (s *Server) issueCreateForm(w http.ResponseWriter, r *http.Request, u store.User) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "issues"
	templates := control.IssueTemplates(p.Dir, p.Repo.DefaultBranch)
	body, tplName := "", ""
	if want := r.URL.Query().Get("template"); want != "" {
		for _, t := range templates {
			if t.Name == want {
				body, tplName = t.Body, t.Name
			}
		}
	} else {
		for _, t := range templates {
			if t.Name == "issue-template.md" || body == "" {
				body, tplName = t.Body, t.Name
			}
			if t.Name == "issue-template.md" {
				break
			}
		}
	}
	s.render(w, "issuenew.html", struct {
		repoPage
		Body      string
		Template  string
		Templates []control.IssueTemplate
	}{p, body, tplName, templates})
}

func (s *Server) issueCreateSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanRead)
	if !ok {
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	n, err := s.st.CreateIssue(repo.ID, u.ID, title, r.FormValue("body"))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/issues/%d", repo.Path(), n), http.StatusSeeOther)
}

func (s *Server) issueCommentSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanRead)
	if !ok {
		return
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	iss, err := s.st.IssueByNumber(repo.ID, n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "empty comment", http.StatusBadRequest)
		return
	}
	if err := s.st.AddIssueComment(iss.ID, u.ID, body); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/issues/%d", repo.Path(), n), http.StatusSeeOther)
}

func (s *Server) mrCommentSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanRead)
	if !ok {
		return
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	m, err := s.st.MRByNumber(repo.ID, n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "empty comment", http.StatusBadRequest)
		return
	}
	if err := s.st.AddMRComment(m.ID, u.ID, body); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/mrs/%d", repo.Path(), n), http.StatusSeeOther)
}

type editPage struct {
	Site    string
	Viewer  string
	Repo    store.Repo
	Ref     string
	Path    string
	Content string
	Error   string
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanWrite)
	if !ok {
		return
	}
	ref := r.PathValue("ref")
	filePath := strings.Trim(r.PathValue("path"), "/")
	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	content, err := gitutil.ReadBlob(dir, "refs/heads/"+ref, filePath, maxRenderBytes)
	if err != nil {
		content = nil // new file
	}
	if gitutil.IsBinary(content) {
		http.Error(w, "binary files cannot be edited in the browser", http.StatusBadRequest)
		return
	}
	s.render(w, "edit.html", editPage{
		Site: s.siteName(), Viewer: u.Username, Repo: repo,
		Ref: ref, Path: filePath, Content: string(content),
	})
}

func (s *Server) editSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanWrite)
	if !ok {
		return
	}
	ref := r.PathValue("ref")
	filePath := strings.Trim(r.PathValue("path"), "/")
	fail := func(msg string) {
		s.render(w, "edit.html", editPage{
			Site: s.siteName(), Viewer: u.Username, Repo: repo,
			Ref: ref, Path: filePath, Content: r.FormValue("content"), Error: msg,
		})
	}
	// Web edits produce unsigned commits; a repo that requires signed
	// commits must refuse them rather than violate its own policy.
	if repo.Settings.RequireSignedCommits {
		fail("this repository requires signed commits; web edits are unsigned — push a signed commit over SSH instead")
		return
	}
	email, err := s.st.PrimaryVerifiedEmail(u.ID)
	if err != nil {
		fail("internal error")
		return
	}
	if email == "" {
		fail("commits carry your identity: your account needs a verified primary email")
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if message == "" {
		message = "edit " + filePath
	}
	dir := control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)
	if _, err := gitutil.CommitFileChange(dir, ref, filePath,
		[]byte(r.FormValue("content")), u.Username, email, message); err != nil {
		fail(err.Error())
		return
	}
	s.st.MarkMirrorsDirty(repo.ID, "push")
	http.Redirect(w, r, fmt.Sprintf("/%s/blob/%s/%s", repo.Path(), ref, filePath), http.StatusSeeOther)
}
