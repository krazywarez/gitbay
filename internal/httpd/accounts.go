package httpd

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"

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
			basePage
			Error string
		}{basePage{Site: s.siteName(), Host: s.cfg.SiteHost()}, ""})
		return
	}
	userID, err := s.st.ConsumeLoginToken(store.HashToken(token))
	if err != nil {
		s.render(w, "login.html", struct {
			basePage
			Error string
		}{basePage{Site: s.siteName(), Host: s.cfg.SiteHost()},
			"that login link is invalid, expired, or already used — mint a new one"})
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

// adminOrgs lists organizations the user administers, for owner pickers.
func (s *Server) adminOrgs(u store.User) []string {
	var out []string
	if orgs, err := s.st.ListOrgsForUser(u.ID); err == nil {
		for _, o := range orgs {
			if o.Role == "admin" {
				out = append(out, o.Username)
			}
		}
	}
	return out
}

func (s *Server) renderNewRepo(w http.ResponseWriter, u store.User, errMsg string) {
	s.render(w, "new.html", struct {
		basePage
		Orgs  []string
		Error string
	}{s.baseFor(u), s.adminOrgs(u), errMsg})
}

func (s *Server) newRepoForm(w http.ResponseWriter, r *http.Request, u store.User) {
	s.renderNewRepo(w, u, "")
}

func (s *Server) newRepoSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	name := r.FormValue("name")
	visibility := "public"
	if r.FormValue("visibility") == "private" {
		visibility = "private"
	}
	fail := func(msg string) { s.renderNewRepo(w, u, msg) }
	if err := policy.ValidateName(name); err != nil {
		fail(err.Error())
		return
	}
	// Owner: yourself, or an org you admin — same rule as repo create.
	owner := r.FormValue("owner")
	ownerKind, ownerID := "user", u.ID
	if owner == "" {
		owner = u.Username
	}
	if owner != u.Username {
		org, err := s.st.OrgByName(owner)
		if err != nil {
			fail("no such organization")
			return
		}
		role, _ := s.st.OrgRole(org.ID, u.ID)
		if role != "admin" {
			fail("only admins of " + owner + " can create repositories there")
			return
		}
		ownerKind, ownerID = "org", org.ID
	}
	id, err := s.st.CreateRepo(ownerKind, ownerID, name, visibility)
	if err != nil {
		fail(err.Error())
		return
	}
	dir := control.RepoDir(s.cfg.Server.Root, owner, name)
	if err := gitutil.InitBare(dir, "main", control.HooksDir(s.cfg.Server.Root)); err != nil {
		s.st.DeleteRepo(id)
		fail("initializing repository failed")
		return
	}
	http.Redirect(w, r, "/"+owner+"/"+name, http.StatusSeeOther)
}

// pinToggle pins or unpins the repo for the logged-in viewer.
func (s *Server) pinToggle(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policy.CanRead)
	if !ok {
		return
	}
	if s.st.IsPinned(u.ID, repo.ID) {
		s.st.UnpinRepo(u.ID, repo.ID)
	} else {
		s.st.PinRepo(u.ID, repo.ID)
	}
	http.Redirect(w, r, "/"+repo.Path(), http.StatusSeeOther)
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

// signupForm and signupSubmit front the SSH registration path for open
// and invite instances: same store transactions, same rules, a pasted
// public key instead of the connecting one.
func (s *Server) signupForm(w http.ResponseWriter, r *http.Request) {
	s.renderSignup(w, "", "")
}

func (s *Server) renderSignup(w http.ResponseWriter, errMsg, username string) {
	s.render(w, "register.html", struct {
		basePage
		Host     string
		Mode     string // open | invite
		Error    string
		Username string
	}{basePage{Site: s.siteName(), Host: s.cfg.SiteHost()}, s.cfg.SiteHost(), s.cfg.Registration.Mode, errMsg, username})
}

func (s *Server) signupSubmit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	keyText := strings.TrimSpace(r.FormValue("key"))
	pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(keyText))
	if err != nil {
		s.renderSignup(w, "that does not parse as an SSH public key (expected e.g. \"ssh-ed25519 AAAA... comment\")", username)
		return
	}
	msg, errMsg, code := control.RegisterAccount(s.cfg, s.st, pub, username,
		strings.TrimSpace(r.FormValue("email")), strings.TrimSpace(r.FormValue("invite")))
	if code != 0 {
		s.renderSignup(w, errMsg, username)
		return
	}
	s.render(w, "registered.html", struct {
		basePage
		Username string
		Message  string
		Host     string
	}{basePage{Site: s.siteName(), Host: s.cfg.SiteHost()}, username, msg, s.cfg.SiteHost()})
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
	n, err := s.st.CreateIssue(repo.ID, u.ID, title, r.FormValue("body"), "md")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.st.RecordEvent(repo.ID, u.ID, "issue.created", fmt.Sprintf(`{"number":%d}`, n))
	// Labels need write access, matching the SSH rule; ignored otherwise.
	if labels := strings.Fields(r.FormValue("labels")); len(labels) > 0 {
		grant, _ := s.st.AccessRole(repo.ID, u.ID)
		if policy.CanWrite(u, repo, grant) {
			if iss, err := s.st.IssueByNumber(repo.ID, n); err == nil {
				for _, l := range labels {
					s.st.SetIssueLabel(repo.ID, iss.ID, l, true)
				}
			}
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/issues/%d", repo.Path(), n), http.StatusSeeOther)
}

// issueEditSubmit edits title/body (author or write) and, with write
// access, replaces the label set.
func (s *Server) issueEditSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
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
	grant, _ := s.st.AccessRole(repo.ID, u.ID)
	canWrite := policy.CanWrite(u, repo, grant)
	if iss.Author != u.Username && !canWrite {
		http.Error(w, "only the author or users with write access can edit", http.StatusForbidden)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	body := r.FormValue("body")
	if err := s.st.UpdateIssueText(iss.ID, &title, &body, nil); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if canWrite {
		want := strings.Fields(r.FormValue("labels"))
		for _, l := range iss.Labels {
			if !slices.Contains(want, l) {
				s.st.SetIssueLabel(repo.ID, iss.ID, l, false)
			}
		}
		for _, l := range want {
			s.st.SetIssueLabel(repo.ID, iss.ID, l, true)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/issues/%d", repo.Path(), n), http.StatusSeeOther)
}

// mrEditSubmit edits an MR's title/body (author or write).
func (s *Server) mrEditSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
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
	grant, _ := s.st.AccessRole(repo.ID, u.ID)
	if m.Author != u.Username && !policy.CanWrite(u, repo, grant) {
		http.Error(w, "only the author or users with write access can edit", http.StatusForbidden)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	body := r.FormValue("body")
	if err := s.st.UpdateMRText(m.ID, &title, &body, nil); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/mrs/%d", repo.Path(), n), http.StatusSeeOther)
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
	if err := s.st.AddIssueComment(iss.ID, u.ID, body, "md"); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.st.RecordEvent(repo.ID, u.ID, "issue.commented", fmt.Sprintf(`{"number":%d}`, n))
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
	if err := s.st.AddMRComment(m.ID, u.ID, body, "md"); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.st.RecordEvent(repo.ID, u.ID, "mr.commented", fmt.Sprintf(`{"number":%d}`, n))
	http.Redirect(w, r, fmt.Sprintf("/%s/mrs/%d", repo.Path(), n), http.StatusSeeOther)
}

type editPage struct {
	basePage
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
		basePage: s.baseFor(u), Repo: repo,
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

	// Editing is a control command; the web supplies the form and lets
	// the registry enforce the rules — signed-commit policy, verified
	// identity, archived repositories — so every surface agrees on them.
	argv := []string{"repo", "commit-file", repo.Path(), filePath, "--ref", ref, "--file", "-"}
	if message := strings.TrimSpace(r.FormValue("message")); message != "" {
		argv = append(argv, "--message", message)
	}
	if msg, ok := s.runControlStdin(u, argv, r.FormValue("content")); !ok {
		s.render(w, "edit.html", editPage{
			basePage: s.baseFor(u), Repo: repo,
			Ref: ref, Path: filePath, Content: r.FormValue("content"), Error: msg,
		})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/blob/%s/%s", repo.Path(), ref, filePath), http.StatusSeeOther)
}
