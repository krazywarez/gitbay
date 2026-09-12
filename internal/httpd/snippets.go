package httpd

import (
	"bytes"
	"html/template"
	"net/http"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// snippetScope resolves the owner and id in the URL for the viewer. A
// missing owner, an id under another owner, and a private snippet the
// viewer may not read are all the same 404.
func (s *Server) snippetScope(w http.ResponseWriter, r *http.Request) (store.Snippet, store.User, bool) {
	viewer := s.viewer(r)
	sn, err := s.st.SnippetByPublicID(r.PathValue("id"))
	if err != nil || sn.OwnerName != r.PathValue("owner") || !policy.CanReadSnippet(viewer, sn) {
		s.notFound(w, r)
		return sn, viewer, false
	}
	return sn, viewer, true
}

type snippetRow struct {
	store.Snippet
	Names string
}

func (s *Server) snippetsPage(w http.ResponseWriter, r *http.Request) {
	viewer := s.viewer(r)
	owner, err := s.st.UserByUsername(r.PathValue("owner"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	self := viewer.ID != 0 && viewer.ID == owner.ID
	all := self || viewer.IsAdmin
	list, err := s.st.ListSnippets(owner.ID, all, 0, 0)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rows := make([]snippetRow, 0, len(list))
	for _, sn := range list {
		var names bytes.Buffer
		for i, f := range sn.Files {
			if i > 0 {
				names.WriteString(", ")
			}
			names.WriteString(f.Name)
		}
		rows = append(rows, snippetRow{sn, names.String()})
	}
	s.render(w, "snippets.html", struct {
		basePage
		Owner    string
		Self     bool
		All      bool
		Snippets []snippetRow
		Notice   string
	}{s.baseFor(viewer), owner.Username, self, all, rows, s.takeFlash(w, r)})
}

type snippetFileView struct {
	Name     string
	Size     int64
	Lines    int
	Content  string
	HTML     template.HTML
	TooLarge bool
}

// snippetPage highlights files up to a shared budget across the page: a
// snippet with many or large files does not make one request highlight
// megabytes of markup. Content is filled only for the owner, whose edit
// textarea needs the raw text regardless of the budget.
func (s *Server) snippetPage(w http.ResponseWriter, r *http.Request) {
	sn, viewer, ok := s.snippetScope(w, r)
	if !ok {
		return
	}
	files, err := s.st.SnippetFiles(sn.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	canWrite := policy.CanWriteSnippet(viewer, sn)
	budget := int64(maxRenderBytes)
	views := make([]snippetFileView, 0, len(files))
	for _, f := range files {
		lines := bytes.Count(f.Content, []byte("\n"))
		if len(f.Content) > 0 && f.Content[len(f.Content)-1] != '\n' {
			lines++
		}
		view := snippetFileView{Name: f.Name, Size: f.Size, Lines: lines}
		if canWrite {
			view.Content = string(f.Content)
		}
		if f.Size <= budget {
			view.HTML = highlightPlain(f.Name, f.Content)
			budget -= f.Size
		} else {
			view.TooLarge = true
		}
		views = append(views, view)
	}
	s.render(w, "snippet.html", struct {
		basePage
		Owner    string
		Snippet  store.Snippet
		Files    []snippetFileView
		CanWrite bool
		Notice   string
	}{s.baseFor(viewer), sn.OwnerName, sn, views, canWrite, s.takeFlash(w, r)})
}

// snippetRaw serves one file as text, inert on the forge's origin.
func (s *Server) snippetRaw(w http.ResponseWriter, r *http.Request) {
	sn, _, ok := s.snippetScope(w, r)
	if !ok {
		return
	}
	f, err := s.st.SnippetFile(sn.ID, r.PathValue("name"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(f.Content)
}

type snippetNewPage struct {
	basePage
	Owner       string
	Name        string
	Description string
	Visibility  string
	Content     string
	Error       string
}

// snippetNewForm is the owner's own page only: the URL names the owner
// and a snippet cannot be created for someone else.
func (s *Server) snippetNewForm(w http.ResponseWriter, r *http.Request, u store.User) {
	if r.PathValue("owner") != u.Username {
		s.notFound(w, r)
		return
	}
	s.render(w, "snippetnew.html", snippetNewPage{basePage: s.baseFor(u), Owner: u.Username})
}

// snippetNewSubmit re-renders the form with the submitted values on a
// refusal, so a typo in the name does not throw away a pasted body.
func (s *Server) snippetNewSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	if r.PathValue("owner") != u.Username {
		s.notFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	visibility := r.FormValue("visibility")
	content := r.FormValue("content")
	argv := []string{"snippet", "create", name, "--description", description, "--visibility", visibility}
	var out control.SnippetOut
	code, msg := s.dispatchIntoStdin(u, argv, content, &out)
	if code != protocol.ExitOK {
		s.render(w, "snippetnew.html", snippetNewPage{
			basePage: s.baseFor(u), Owner: u.Username,
			Name: name, Description: description, Visibility: visibility, Content: content, Error: msg,
		})
		return
	}
	http.Redirect(w, r, "/"+u.Username+"/-/snippets/"+out.ID, http.StatusSeeOther)
}

// snippetAction runs a write on the snippet in the URL and returns to its
// page with the message, or to dest (the list, for a delete) on success.
// A snippet the viewer may not read is the 404 page, as on every read.
func (s *Server) snippetAction(w http.ResponseWriter, r *http.Request, u store.User, argv []string, stdin string, dest string) {
	sn, _, ok := s.snippetScope(w, r)
	if !ok {
		return
	}
	page := "/" + sn.OwnerName + "/-/snippets/" + sn.PublicID
	if dest == "" {
		dest = page
	}
	back := func(w http.ResponseWriter, r *http.Request, msg string) {
		s.setFlash(w, msg)
		to := dest
		if msg != "" {
			to = page
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
	msg, code := s.runControlStdinCode(u, argv, stdin)
	if code == protocol.ExitDenied {
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	s.done(w, r, code, msg, back)
}

func (s *Server) snippetEditSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	s.snippetAction(w, r, u, []string{"snippet", "edit", r.PathValue("id"),
		"--description", strings.TrimSpace(r.FormValue("description")),
		"--visibility", r.FormValue("visibility")}, "", "")
}

func (s *Server) snippetDeleteSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	s.snippetAction(w, r, u, []string{"snippet", "delete", r.PathValue("id")}, "",
		"/"+r.PathValue("owner")+"/-/snippets")
}

// An empty textarea reaches the command as empty stdin, which it refuses;
// the message lands on the page like any other.
func (s *Server) snippetFileSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	s.snippetAction(w, r, u, []string{"snippet", "file", "set", r.PathValue("id"), strings.TrimSpace(r.FormValue("name"))},
		r.FormValue("content"), "")
}

func (s *Server) snippetFileRemoveSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	s.snippetAction(w, r, u, []string{"snippet", "file", "remove", r.PathValue("id"), strings.TrimSpace(r.FormValue("name"))}, "", "")
}
