package httpd

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

const sessionCookie = "gitbay_session"

// sessionSameSite is Lax so a login link followed from a mail client keeps
// its session through the redirect. Cross-site POSTs are refused by
// checkOrigin and carry no Lax cookie anyway.
const sessionSameSite = http.SameSiteLaxMode

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

// renderLogin draws the login page. Mode carries the registration mode so
// the page can tell a brand-new visitor how to get an account.
func (s *Server) renderLogin(w http.ResponseWriter, errMsg string) {
	s.render(w, "login.html", struct {
		basePage
		Mode  string // closed | invite | open
		Error string
	}{basePage{Site: s.siteName(), Host: s.cfg.SiteHost()}, s.cfg.Registration.Mode, errMsg})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		s.renderLogin(w, "")
		return
	}
	userID, err := s.st.ConsumeLoginToken(store.HashToken(token))
	if err != nil {
		s.renderLogin(w, "that login link is invalid, expired, or already used — mint a new one")
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
		HttpOnly: true, SameSite: sessionSameSite,
		Secure: s.cfg.HTTP.TLS != "off",
		MaxAge: 7 * 24 * 3600,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		s.st.DeleteWebSession(store.HashToken(ck.Value))
	}
	http.SetCookie(w, s.clearCookie(sessionCookie, sessionSameSite))
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
	owner := r.FormValue("owner")
	if owner == "" {
		owner = u.Username
	}
	name := r.FormValue("name")
	argv := []string{"repo", "create", owner + "/" + name}
	if r.FormValue("visibility") == "private" {
		argv = append(argv, "--private")
	}
	if _, msg, ok := s.runControl(u, argv); !ok {
		s.renderNewRepo(w, u, msg)
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

// Issue and merge request writes run the command the CLI runs, so the
// archived check, notifications, body format and the audit entry have one
// implementation. Bodies travel on stdin, the way --file - does.

func (s *Server) issueCreateSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repoPath := r.PathValue("owner") + "/" + r.PathValue("repo")
	title := strings.TrimSpace(r.FormValue("title"))
	var created control.Created
	code, msg := s.dispatchIntoStdin(u, []string{"issue", "create", repoPath, "--title", title, "--file", "-"}, r.FormValue("body"), &created)
	if code != protocol.ExitOK {
		http.Error(w, msg, statusForExit(code))
		return
	}
	n := created.Number
	// Labels need write access, matching the SSH rule; the command refuses
	// otherwise and the issue stands without them.
	if args := fieldArgs("--add", r.FormValue("labels")); len(args) > 0 {
		s.runControl(u, append([]string{"issue", "label", repoPath, fmt.Sprint(n)}, args...))
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/issues/%d", repoPath, n), http.StatusSeeOther)
}

// issueEditSubmit edits title/body (author or write) and, with write
// access, replaces the label set.
func (s *Server) issueEditSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repoPath := r.PathValue("owner") + "/" + r.PathValue("repo")
	n := r.PathValue("n")
	title := strings.TrimSpace(r.FormValue("title"))
	code, msg := s.dispatchJSON(u, []string{"issue", "edit", repoPath, n, "--title", title, "--file", "-"}, r.FormValue("body"))
	if code != protocol.ExitOK {
		http.Error(w, msg, statusForExit(code))
		return
	}
	var cur struct {
		Labels []string `json:"labels"`
	}
	if _, ok := s.runControlInto(u, []string{"issue", "show", repoPath, n}, &cur); ok {
		want := strings.Fields(r.FormValue("labels"))
		var args []string
		for _, l := range cur.Labels {
			if !slices.Contains(want, l) {
				args = append(args, "--remove", l)
			}
		}
		for _, l := range want {
			if !slices.Contains(cur.Labels, l) {
				args = append(args, "--add", l)
			}
		}
		if len(args) > 0 {
			s.runControl(u, append([]string{"issue", "label", repoPath, n}, args...))
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/issues/%s", repoPath, n), http.StatusSeeOther)
}

// mrEditSubmit edits an MR's title/body (author or write).
func (s *Server) mrEditSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repoPath := r.PathValue("owner") + "/" + r.PathValue("repo")
	n := r.PathValue("n")
	title := strings.TrimSpace(r.FormValue("title"))
	code, msg := s.dispatchJSON(u, []string{"mr", "edit", repoPath, n, "--title", title, "--file", "-"}, r.FormValue("body"))
	if code != protocol.ExitOK {
		http.Error(w, msg, statusForExit(code))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/mrs/%s", repoPath, n), http.StatusSeeOther)
}

func (s *Server) issueCommentSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	s.commentSubmit(w, r, u, "issue", "issues")
}

func (s *Server) mrCommentSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	s.commentSubmit(w, r, u, "mr", "mrs")
}

func (s *Server) commentSubmit(w http.ResponseWriter, r *http.Request, u store.User, noun, segment string) {
	repoPath := r.PathValue("owner") + "/" + r.PathValue("repo")
	n := r.PathValue("n")
	code, msg := s.dispatchJSON(u, []string{noun, "comment", repoPath, n, "--file", "-"}, strings.TrimSpace(r.FormValue("body")))
	if code != protocol.ExitOK {
		http.Error(w, msg, statusForExit(code))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%s/%s/%s", repoPath, segment, n), http.StatusSeeOther)
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
