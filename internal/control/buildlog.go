package control

import "strings"

// LogSection is one part of a build log: the setup before the first
// step (N 0), or one step and its output.
type LogSection struct {
	N    int    // 0 for the setup, else the 1-based step
	Step string // the step's command; "" for the setup
	Text string
}

// SplitBuildLog cuts a log at the "$ <step>" line the runner writes
// before each step, matching the build's steps in order and only at a
// line start. Output before the first step is the setup section, left
// out when empty. A step with no line in the log — the build stopped
// before it — has no section, and neither has any step after it.
func SplitBuildLog(log string, steps []string) []LogSection {
	var out []LogSection
	cur := LogSection{}
	start := 0
	for i, step := range steps {
		marker := "$ " + step + "\n"
		at := findLine(log, marker, start)
		if at < 0 {
			break
		}
		cur.Text = log[start:at]
		if cur.N > 0 || cur.Text != "" {
			out = append(out, cur)
		}
		cur = LogSection{N: i + 1, Step: step}
		start = at + len(marker)
	}
	cur.Text = log[start:]
	if cur.N > 0 || cur.Text != "" {
		out = append(out, cur)
	}
	return out
}

// findLine is the index of line in log at or after from where it starts
// a line, or -1.
func findLine(log, line string, from int) int {
	for i := from; i <= len(log)-len(line); {
		j := strings.Index(log[i:], line)
		if j < 0 {
			return -1
		}
		at := i + j
		if at == 0 || log[at-1] == '\n' {
			return at
		}
		i = at + 1
	}
	return -1
}

// FailedSection is the index of the section a failed build stopped in:
// the step the runner named, or the last section when it named none (an
// older runner, or a failure the runner could not tie to a step). -1
// when the build did not fail or its log is empty.
func FailedSection(sections []LogSection, status string, failedStep int) int {
	if status != "failure" || len(sections) == 0 {
		return -1
	}
	for i, s := range sections {
		if failedStep > 0 && s.N == failedStep {
			return i
		}
	}
	return len(sections) - 1
}

// tailLines is the last n lines of b; a final newline ends the last line
// rather than starting another.
func tailLines(b []byte, n int) []byte {
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if b[i] == '\n' {
			n--
			if n == 0 {
				return b[i+1:]
			}
		}
	}
	return b
}
