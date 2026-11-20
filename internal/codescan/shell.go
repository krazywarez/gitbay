package codescan

import "strings"

// unquotedExpansionRule flags a parameter expansion in a command. It
// reports $1 in printf "%$(($1 - 1))s", where the shell splits nothing:
// arithmetic is never word-split, and here it is inside double quotes as
// well.
// splitLines reads the script to tell those apart from the real thing.
const unquotedExpansionRule = "bash.lang.correctness.unquoted-variable-expansion-in-command"

// Shell contexts. In a command, a backtick or a $( ) an expansion is
// word-split; in double quotes and arithmetic it is not.
const (
	shCommand = iota
	shSubst
	shBacktick
	shDouble
	shArith
)

type shFrame struct {
	kind  int
	depth int // ( open within the frame
}

type heredoc struct {
	delim         string
	strip, quoted bool
}

// splitLines returns the lines of a shell script that hold an expansion
// the shell word-splits: $name, ${…}, $1, $* or $@ outside double quotes
// and arithmetic. A command that runs over several lines marks all of
// them. ok is false when the script does not read to the end with every
// quote, bracket and heredoc closed, and then nothing is known.
func splitLines(src []byte) (lines map[int]bool, ok bool) {
	s := string(src)
	lines = map[int]bool{}
	stack := []shFrame{{kind: shCommand}}
	line, first := 1, 1
	split := false
	var pending []heredoc
	push := func(kind int) { stack = append(stack, shFrame{kind: kind}) }
	pop := func() { stack = stack[:len(stack)-1] }
	for i := 0; i < len(s); i++ {
		c := s[i]
		top := &stack[len(stack)-1]
		if c == '\n' {
			line++
			if top.kind == shDouble || top.kind == shArith {
				continue
			}
			end, l, sp, ok := heredocs(s, i+1, pending)
			if !ok {
				return nil, false
			}
			i, line, split, pending = end-1, line+l, split || sp, nil
			if len(stack) == 1 {
				if split {
					for l := first; l < line; l++ {
						lines[l] = true
					}
				}
				first, split = line, false
			}
			continue
		}
		if c == '\\' {
			if i+1 < len(s) && s[i+1] == '\n' {
				line++
			}
			i++
			continue
		}
		if c == '$' {
			n, kind, splits := dollar(s, i, top.kind)
			if n < 0 {
				return nil, false
			}
			line += strings.Count(s[i:i+n], "\n")
			split = split || splits
			if kind >= 0 {
				push(kind)
			}
			i += n - 1
			continue
		}
		switch top.kind {
		case shDouble:
			switch c {
			case '"':
				pop()
			case '`':
				push(shBacktick)
			}
		case shArith:
			switch {
			case c == '"':
				push(shDouble)
			case c == '(':
				top.depth++
			case c == ')' && top.depth > 0:
				top.depth--
			case c == ')':
				if i+1 >= len(s) || s[i+1] != ')' {
					return nil, false
				}
				pop()
				i++
			}
		default: // shCommand, shSubst, shBacktick
			switch {
			case c == '\'':
				end := strings.IndexByte(s[i+1:], '\'')
				if end < 0 {
					return nil, false
				}
				line += strings.Count(s[i+1:i+1+end], "\n")
				i += 1 + end
			case c == '"':
				push(shDouble)
			case c == '`' && top.kind == shBacktick:
				pop()
			case c == '`':
				push(shBacktick)
			case c == '#' && (i == 0 || strings.IndexByte(" \t\n;|&(", s[i-1]) >= 0):
				if end := strings.IndexByte(s[i:], '\n'); end >= 0 {
					i += end - 1
				} else {
					i = len(s)
				}
			case c == '(':
				top.depth++
			case c == ')' && top.depth > 0:
				top.depth--
			case c == ')' && top.kind == shSubst:
				pop()
			case strings.HasPrefix(s[i:], "<<") && !strings.HasPrefix(s[i:], "<<<"):
				h, n := heredocOp(s, i)
				if n < 0 {
					return nil, false
				}
				pending = append(pending, h)
				i += n - 1
			}
		}
	}
	if len(stack) != 1 || len(pending) > 0 {
		return nil, false
	}
	if split {
		for l := first; l <= line; l++ {
			lines[l] = true
		}
	}
	return lines, true
}

// dollar reads the expansion starting with the $ at i, in a context of
// kind ctx. n is how many bytes it takes, or -1 when it is not closed;
// kind is the frame it opens, or -1; splits is whether the shell
// word-splits it there.
func dollar(s string, i, ctx int) (n, kind int, splits bool) {
	splitting := ctx != shDouble && ctx != shArith
	rest := s[i+1:]
	switch {
	case strings.HasPrefix(rest, "(("):
		return 3, shArith, false
	case strings.HasPrefix(rest, "("):
		return 2, shSubst, false
	case strings.HasPrefix(rest, "{"):
		depth := 0
		for j := 1; j < len(s)-i; j++ {
			switch s[i+j] {
			case '\\':
				j++
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					return j + 1, -1, splitting
				}
			}
		}
		return -1, -1, false
	case strings.HasPrefix(rest, "'") && ctx != shDouble:
		for j := 2; j < len(s)-i; j++ {
			switch s[i+j] {
			case '\\':
				j++
			case '\'':
				return j + 1, -1, false
			}
		}
		return -1, -1, false
	case strings.HasPrefix(rest, `"`) && ctx != shDouble:
		return 2, shDouble, false
	case rest != "" && (rest[0] == '_' || isLetter(rest[0])):
		j := 1
		for j < len(rest) && (rest[j] == '_' || isLetter(rest[j]) || rest[j] >= '0' && rest[j] <= '9') {
			j++
		}
		return j + 1, -1, splitting
	case rest != "" && (rest[0] >= '0' && rest[0] <= '9' || rest[0] == '*' || rest[0] == '@'):
		return 2, -1, splitting
	}
	// $# $? $$ $! $- expand to text with no separators in it; a bare $ is
	// literal.
	if rest != "" && strings.IndexByte("#?$!-", rest[0]) >= 0 {
		return 2, -1, false
	}
	return 1, -1, false
}

// heredocOp reads a << or <<- operator and its delimiter word at i. n is
// how many bytes it takes, or -1 when there is no word.
func heredocOp(s string, i int) (heredoc, int) {
	j := i + 2
	var h heredoc
	if j < len(s) && s[j] == '-' {
		h.strip = true
		j++
	}
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	start := j
	for j < len(s) && strings.IndexByte(" \t\n;|&<>()", s[j]) < 0 {
		j++
	}
	word := s[start:j]
	h.quoted = strings.ContainsAny(word, `'"\`)
	h.delim = strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(word)
	if h.delim == "" {
		return h, -1
	}
	return h, j - i
}

// heredocs reads the bodies of the pending heredocs from i, the start of
// the line after their operators. end is just past the last delimiter
// line, n the lines read. A body that is not quoted and runs a command
// substitution counts as splitting, since the command inside is not
// read here.
func heredocs(s string, i int, pending []heredoc) (end, n int, splits, ok bool) {
	for _, h := range pending {
		for {
			if i >= len(s) {
				return 0, 0, false, false
			}
			nl := strings.IndexByte(s[i:], '\n')
			text := s[i:]
			next := len(s)
			if nl >= 0 {
				text, next = s[i:i+nl], i+nl+1
				n++
			}
			if !h.quoted && (strings.Contains(text, "$(") || strings.Contains(text, "`")) {
				splits = true
			}
			i = next
			if h.strip {
				text = strings.TrimLeft(text, "\t")
			}
			if text == h.delim {
				break
			}
		}
	}
	return i, n, splits, true
}
