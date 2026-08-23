package protocol

import (
	"errors"
	"fmt"
	"strings"
)

// Tokenize splits an SSH exec command string into argv using POSIX
// shell-word rules: whitespace separates words; single quotes preserve
// everything literally; double quotes preserve everything except backslash
// before \, ", or $; a bare backslash escapes the next character. There is
// no expansion of any kind — no globbing, variables, or substitution. The
// client's shell has already applied one layer of quoting before the string
// reaches us.
func Tokenize(s string) ([]string, error) {
	var argv []string
	var cur strings.Builder
	inWord := false

	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				argv = append(argv, cur.String())
				cur.Reset()
				inWord = false
			}
			i++
		case c == '\'':
			inWord = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case c == '"':
			inWord = true
			i++
			closed := false
			for i < len(s) {
				c = s[i]
				if c == '"' {
					closed = true
					i++
					break
				}
				if c == '\\' && i+1 < len(s) {
					switch s[i+1] {
					case '\\', '"', '$', '`':
						cur.WriteByte(s[i+1])
						i += 2
						continue
					}
				}
				cur.WriteByte(c)
				i++
			}
			if !closed {
				return nil, errors.New("unterminated double quote")
			}
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			inWord = true
			cur.WriteByte(s[i+1])
			i += 2
		case c == '$' || c == '`' || c == ';' || c == '&' || c == '|' ||
			c == '<' || c == '>' || c == '(' || c == ')' || c == '*' ||
			c == '?' || c == '[' || c == '#' || c == '~':
			// Unquoted shell metacharacters are rejected outright rather
			// than passed through: there is no shell here, and silently
			// treating them as literals would mask client quoting bugs.
			return nil, fmt.Errorf("unquoted shell metacharacter %q", c)
		default:
			inWord = true
			cur.WriteByte(c)
			i++
		}
	}
	if inWord {
		argv = append(argv, cur.String())
	}
	return argv, nil
}
