package httpd

import (
	"path"
	"strings"
)

// langByExt names the languages worth reporting on a repository home. It is
// deliberately short: the point is "what is this written in", not a
// linguist-grade census, and an unlisted extension simply does not count.
var langByExt = map[string]string{
	".go": "Go", ".rs": "Rust", ".c": "C", ".h": "C", ".cc": "C++",
	".cpp": "C++", ".hpp": "C++", ".cs": "C#", ".java": "Java",
	".kt": "Kotlin", ".swift": "Swift", ".m": "Objective-C",
	".py": "Python", ".rb": "Ruby", ".pl": "Perl", ".php": "PHP",
	".js": "JavaScript", ".mjs": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript",
	".sh": "Shell", ".bash": "Shell", ".zsh": "Shell", ".fish": "Shell",
	".lua": "Lua", ".el": "Emacs Lisp", ".lisp": "Common Lisp",
	".clj": "Clojure", ".ex": "Elixir", ".exs": "Elixir", ".erl": "Erlang",
	".hs": "Haskell", ".ml": "OCaml", ".scala": "Scala", ".dart": "Dart",
	".zig": "Zig", ".nim": "Nim", ".jl": "Julia", ".r": "R",
	".sql": "SQL", ".html": "HTML", ".css": "CSS", ".scss": "SCSS",
	".vim": "Vim script", ".tf": "HCL", ".nix": "Nix",
	".org": "Org", ".md": "Markdown", ".tex": "TeX",
}

// langOf maps a path to a language name, or "" when it is not code we
// count. Vendored and generated trees are excluded: they describe someone
// else's work, and including them makes the bar meaningless.
func langOf(p string) string {
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "vendor", "node_modules", "third_party", "testdata", "dist":
			return ""
		}
	}
	if strings.HasSuffix(p, ".min.js") || strings.HasSuffix(p, ".min.css") {
		return ""
	}
	return langByExt[strings.ToLower(path.Ext(p))]
}
