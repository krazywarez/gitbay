package deps

import "github.com/BurntSushi/toml"

// parseCargo takes the direct dependency names from Cargo.toml and their
// resolved versions from Cargo.lock where one exists. Dependencies sourced
// from a path or a git remote are skipped: crates.io has nothing to say
// about them.
func parseCargo(read ReadFile) []Dep {
	raw, err := read("Cargo.toml")
	if err != nil {
		return nil
	}
	var manifest struct {
		Dependencies      map[string]any `toml:"dependencies"`
		DevDependencies   map[string]any `toml:"dev-dependencies"`
		BuildDependencies map[string]any `toml:"build-dependencies"`
	}
	if toml.Unmarshal(raw, &manifest) != nil {
		return nil
	}
	locked := cargoLock(read)
	var out []Dep
	for _, set := range []map[string]any{manifest.Dependencies, manifest.DevDependencies, manifest.BuildDependencies} {
		for name, spec := range set {
			req, ok := cargoRequirement(spec)
			if !ok {
				continue
			}
			current := locked[name]
			if current == "" {
				current = pin(req)
			}
			if current != "" {
				out = append(out, Dep{Ecosystem: EcoCargo, Name: name, Current: current})
			}
		}
	}
	return out
}

// cargoRequirement reads the version out of either dependency form —
// `serde = "1.0"` or `serde = { version = "1.0", features = [...] }` —
// and rejects the ones that name a source other than the registry.
func cargoRequirement(spec any) (string, bool) {
	switch v := spec.(type) {
	case string:
		return v, true
	case map[string]any:
		if v["path"] != nil || v["git"] != nil {
			return "", false
		}
		s, ok := v["version"].(string)
		return s, ok
	}
	return "", false
}

func cargoLock(read ReadFile) map[string]string {
	raw, err := read("Cargo.lock")
	if err != nil {
		return nil
	}
	var lock struct {
		Package []struct {
			Name    string `toml:"name"`
			Version string `toml:"version"`
			Source  string `toml:"source"`
		} `toml:"package"`
	}
	if toml.Unmarshal(raw, &lock) != nil {
		return nil
	}
	out := map[string]string{}
	for _, p := range lock.Package {
		if v := pin(p.Version); v != "" {
			out[p.Name] = v
		}
	}
	return out
}
