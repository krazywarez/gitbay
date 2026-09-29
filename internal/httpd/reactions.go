package httpd

import (
	"net/http"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// reactionView is one reaction button (or count) under an item.
type reactionView struct {
	Name  string
	Emoji string
	Count int
	Me    bool
	Label string
}

// reactionBar is what the reactions partial renders for one item:
// Comment is 0 for the issue or merge request body.
type reactionBar struct {
	Action  string
	Comment int64
	Signed  bool
	Items   []reactionView
}

// reactionBars builds the bars for a thread body (key 0) and its
// comments. A signed-in viewer is offered every reaction; anyone else
// sees only the ones somebody gave.
func (s *Server) reactionBars(r *http.Request, noun string, threadID int64, comments []store.IssueComment, action string) map[int64]reactionBar {
	viewer := s.webViewer(r)
	counts, _ := s.st.ReactionCounts(noun, threadID, viewer.ID)
	bar := func(id int64) reactionBar {
		b := reactionBar{Action: action, Comment: id, Signed: viewer.ID != 0}
		given := map[string]store.ReactionCount{}
		for _, rc := range counts[id] {
			given[rc.Reaction] = rc
		}
		for _, r := range store.Reactions {
			rc := given[r.Name]
			if rc.Count == 0 && !b.Signed {
				continue
			}
			label := r.Name
			if rc.Me {
				label = "remove " + r.Name
			}
			b.Items = append(b.Items, reactionView{r.Name, r.Emoji, rc.Count, rc.Me, label})
		}
		return b
	}
	out := map[int64]reactionBar{0: bar(0)}
	for _, c := range comments {
		if c.Kind != "system" {
			out[c.ID] = bar(c.ID)
		}
	}
	return out
}

// reactArgs turns the form into the react command's arguments. The
// button pressed is named add or remove and carries the reaction.
func reactArgs(r *http.Request, noun string) []string {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	args := []string{noun, "react", repo, r.PathValue("n")}
	if c := strings.TrimSpace(r.FormValue("comment")); c != "" {
		args = append(args, "--comment", c)
	}
	if v := r.FormValue("remove"); v != "" {
		return append(args, "--remove", v)
	}
	return append(args, r.FormValue("add"))
}

func (s *Server) issueReactSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	_, msg, code := s.runControlCode(u, reactArgs(r, "issue"))
	s.done(w, r, code, msg, s.issueRedirect)
}

func (s *Server) mrReactSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	_, msg, code := s.runControlCode(u, reactArgs(r, "mr"))
	s.done(w, r, code, msg, s.mrRedirect)
}
