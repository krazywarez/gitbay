package deps

import (
	"encoding/json"

	"github.com/BurntSushi/toml"
)

// Names returns the direct dependencies the manifests in read name, whatever
// version each asks for, with Current left empty. Code scanning uses it to
// tell which libraries a repository uses; a range or a git source still
// means the library is there.
func Names(read ReadFile) []Dep {
	var out []Dep
	for _, d := range parseGoMod(read) {
		out = append(out, Dep{Ecosystem: EcoGo, Name: d.Name})
	}
	for _, d := range pythonDeps(read, func(string) string { return "any" }) {
		out = append(out, Dep{Ecosystem: EcoPyPI, Name: d.Name})
	}
	if raw, err := read("package.json"); err == nil {
		var pkg struct {
			Dependencies     map[string]any `json:"dependencies"`
			DevDependencies  map[string]any `json:"devDependencies"`
			PeerDependencies map[string]any `json:"peerDependencies"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			for _, set := range []map[string]any{pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies} {
				for name := range set {
					out = append(out, Dep{Ecosystem: EcoNPM, Name: name})
				}
			}
		}
	}
	if raw, err := read("Cargo.toml"); err == nil {
		var manifest struct {
			Dependencies      map[string]any `toml:"dependencies"`
			DevDependencies   map[string]any `toml:"dev-dependencies"`
			BuildDependencies map[string]any `toml:"build-dependencies"`
		}
		if toml.Unmarshal(raw, &manifest) == nil {
			for _, set := range []map[string]any{manifest.Dependencies, manifest.DevDependencies, manifest.BuildDependencies} {
				for name := range set {
					out = append(out, Dep{Ecosystem: EcoCargo, Name: name})
				}
			}
		}
	}
	return out
}

// ManifestFiles are the files Names reads, for finding the directories of a
// repository worth reading it in.
var ManifestFiles = []string{"go.mod", "requirements.txt", "pyproject.toml", "package.json", "Cargo.toml"}
