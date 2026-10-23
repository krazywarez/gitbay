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
			var err error
			if i, err = doubleQuoted(s, i, &cur); err != nil {
				return nil, err
			}
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			inWord = true
			cur.WriteByte(s[i+1])
			i += 2
		case strings.IndexByte("$`;&|<>()*?[#~", c) >= 0:
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

// doubleQuoted writes the double-quoted string opening at s[i] to cur,
// unescaping \\, \", \$ and \`, and returns the index past its closing
// quote.
func doubleQuoted(s string, i int, cur *strings.Builder) (int, error) {
	for i++; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			return i + 1, nil
		}
		if c == '\\' && i+1 < len(s) && strings.IndexByte("\\\"$`", s[i+1]) >= 0 {
			i++
			c = s[i]
		}
		cur.WriteByte(c)
	}
	return i, errors.New("unterminated double quote")
}
