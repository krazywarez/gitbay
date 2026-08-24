package httpd

import "net/http"

// Route is one entry in the explicit route table. The view-only guarantee is
// structural: Handler() consults web.mode when building the table, and the
// route test asserts no mutating route exists in view_only mode.
type Route struct {
	Method  string
	Pattern string // without method prefix
	// Mutating marks routes that can change server state. The git transport
	// POSTs are not mutating: upload-pack is a pure read, and the
	// receive-pack endpoint is a static refusal that writes nothing.
	Mutating bool
	Handler  http.HandlerFunc
}

// Routes returns the route table for the configured web.mode.
func (s *Server) Routes() []Route {
	// Git smart transport (anonymous, public repos only).
	routes := []Route{
		{Method: "GET", Pattern: "/{owner}/{repo}/info/refs", Handler: s.infoRefs},
		{Method: "POST", Pattern: "/{owner}/{repo}/git-upload-pack", Handler: s.uploadPack},
		{Method: "POST", Pattern: "/{owner}/{repo}/git-receive-pack", Handler: s.receivePackRefusal},
	}

	// Web UI, read-only. These exist in every mode.
	routes = append(routes,
		Route{Method: "GET", Pattern: "/{$}", Handler: s.index},
		Route{Method: "GET", Pattern: "/static/style.css", Handler: s.stylesheet},
		Route{Method: "GET", Pattern: "/favicon.svg", Handler: s.favicon},
		Route{Method: "GET", Pattern: "/{owner}", Handler: s.ownerPage},
		Route{Method: "GET", Pattern: "/{owner}/{repo}", Handler: s.repoHome},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/tree/{ref}/{path...}", Handler: s.tree},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/blob/{ref}/{path...}", Handler: s.blob},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/blame/{ref}/{path...}", Handler: s.blame},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/raw/{ref}/{path...}", Handler: s.raw},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/log", Handler: s.log},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/log/{ref}", Handler: s.log},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/commit/{sha}", Handler: s.commit},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/refs", Handler: s.refs},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/archive/{file}", Handler: s.archive},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/issues", Handler: s.issues},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/issues/{n}", Handler: s.issue},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/mrs", Handler: s.mrs},
		Route{Method: "GET", Pattern: "/{owner}/{repo}/mrs/{n}", Handler: s.mr},
	)

	// The JSON API is its own opt-in surface, independent of web.mode.
	if s.cfg.API.Enabled {
		routes = append(routes,
			Route{Method: "POST", Pattern: "/api/v1/cmd", Mutating: true, Handler: s.apiCmd},
		)
	}

	// Account-mode routes exist only when web.mode = "accounts". In
	// view_only they are never registered — the structural guarantee.
	if s.cfg.Web.Mode == "accounts" {
		routes = append(routes,
			Route{Method: "GET", Pattern: "/login", Handler: s.login, Mutating: true}, // consumes a one-time token
			Route{Method: "POST", Pattern: "/logout", Mutating: true,
				Handler: s.checkOrigin(s.logout)},
			Route{Method: "GET", Pattern: "/new", Handler: s.requireUser(s.newRepoForm)},
			Route{Method: "POST", Pattern: "/new", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.newRepoSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/new", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueCreateSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/issues/{n}/comment", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.issueCommentSubmit))},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/mrs/{n}/comment", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.mrCommentSubmit))},
			Route{Method: "GET", Pattern: "/{owner}/{repo}/edit/{ref}/{path...}",
				Handler: s.requireUser(s.editForm)},
			Route{Method: "POST", Pattern: "/{owner}/{repo}/edit/{ref}/{path...}", Mutating: true,
				Handler: s.checkOrigin(s.requireUser(s.editSubmit))},
		)
	}
	return routes
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.Routes() {
		mux.HandleFunc(r.Method+" "+r.Pattern, r.Handler)
	}
	return mux
}
