package deps

import "strings"

// parseGoMod reads the direct requirements from go.mod. Indirect
// requirements are the module graph's business rather than the maintainer's,
// and a replaced module does not come from the proxy at all, so both are
// skipped.
func parseGoMod(read ReadFile) []Dep {
	raw, err := read("go.mod")
	if err != nil {
		return nil
	}
	replaced := map[string]bool{}
	var reqs [][2]string
	block := "" // the directive whose ( ... ) block we are inside
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		indirect := false
		if i := strings.Index(line, "//"); i >= 0 {
			indirect = strings.Contains(line[i:], "indirect")
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		directive := block
		if block == "" {
			d, rest, ok := strings.Cut(line, " ")
			if !ok || (d != "require" && d != "replace") {
				continue
			}
			if rest = strings.TrimSpace(rest); rest == "(" {
				block = d
				continue
			}
			directive, line = d, rest
		} else if line == ")" {
			block = ""
			continue
		}
		fields := strings.Fields(line)
		switch {
		case directive == "require" && !indirect && len(fields) >= 2:
			reqs = append(reqs, [2]string{fields[0], fields[1]})
		case directive == "replace" && len(fields) >= 1:
			replaced[fields[0]] = true
		}
	}
	var out []Dep
	for _, r := range reqs {
		if replaced[r[0]] {
			continue
		}
		if v := pin(r[1]); v != "" {
			out = append(out, Dep{Ecosystem: EcoGo, Name: r[0], Current: r[1]})
		}
	}
	return out
}
