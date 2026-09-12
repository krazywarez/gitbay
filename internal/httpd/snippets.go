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
	Name    string
	Size    int64
	Lines   int
	Content string
	HTML    template.HTML
}

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
	views := make([]snippetFileView, 0, len(files))
	for _, f := range files {
		lines := bytes.Count(f.Content, []byte("\n"))
		if len(f.Content) > 0 && f.Content[len(f.Content)-1] != '\n' {
			lines++
		}
		views = append(views, snippetFileView{f.Name, f.Size, lines, string(f.Content), highlight(f.Name, f.Content)})
	}
	s.render(w, "snippet.html", struct {
		basePage
		Owner    string
		Snippet  store.Snippet
		Files    []snippetFileView
		CanWrite bool
		Notice   string
	}{s.baseFor(viewer), sn.OwnerName, sn, views, policy.CanWriteSnippet(viewer, sn), s.takeFlash(w, r)})
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

// snippetNewForm is the owner's own page only: the URL names the owner
// and a snippet cannot be created for someone else.
func (s *Server) snippetNewForm(w http.ResponseWriter, r *http.Request, u store.User) {
	if r.PathValue("owner") != u.Username {
		s.notFound(w, r)
		return
	}
	s.render(w, "snippetnew.html", struct {
		basePage
		Owner string
	}{s.baseFor(u), u.Username})
}

func (s *Server) snippetNewSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	if r.PathValue("owner") != u.Username {
		s.notFound(w, r)
		return
	}
	argv := []string{"snippet", "create", strings.TrimSpace(r.FormValue("name")),
		"--description", strings.TrimSpace(r.FormValue("description")),
		"--visibility", r.FormValue("visibility")}
	var out control.SnippetOut
	code, msg := s.dispatchIntoStdin(u, argv, r.FormValue("content"), &out)
	if code != protocol.ExitOK {
		http.Error(w, msg, statusForExit(code))
		return
	}
	http.Redirect(w, r, "/"+u.Username+"/-/snippets/"+out.ID, http.StatusSeeOther)
}

// snippetAction runs a write on the snippet in the URL and returns to
// its page with the message, or to the list after a delete. A snippet
// the viewer may not read is the 404 page, as on every read.
func (s *Server) snippetAction(w http.ResponseWriter, r *http.Request, u store.User, argv []string, stdin string, dest string) {
	sn, _, ok := s.snippetScope(w, r)
	if !ok {
		return
	}
	if dest == "" {
		dest = "/" + sn.OwnerName + "/-/snippets/" + sn.PublicID
	}
	back := func(w http.ResponseWriter, r *http.Request, msg string) {
		s.setFlash(w, msg)
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
	var msg string
	var code int
	if stdin == "" {
		_, msg, code = s.runControlCode(u, argv)
	} else {
		msg, code = s.runControlStdinCode(u, argv, stdin)
	}
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
