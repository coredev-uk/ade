// Package aimark says on the forge that text was drafted by a model, and makes
// model text safe to post. It is a leaf package: no ADE imports.
//
// Two rules (docs/PLAN.md §4.3, §6.9):
//
//   - AI-drafted text that reaches the forge carries a visible mark (Mark), so
//     the people reading it know a model wrote the first draft.
//   - A forge runs some lines of a comment as commands: GitLab executes quick
//     actions ("/approve", "/merge", "/run_pipeline", ...) in descriptions and
//     in comments, including review comments, when they are posted. Text a model
//     wrote from forge content can be steered into containing one, so every such
//     line is neutralised (NoQuickActions) before it is posted.
package aimark

import (
	"regexp"
	"strings"
)

// Marker is the line appended to AI-drafted text. It is HTML, so the forge
// renders it small; the raw text still reads plainly.
const Marker = "<sub>Drafted with AI in ADE</sub>"

// Mark appends the AI mark to body: a blank line, then Marker. It is
// idempotent (an existing mark is removed first, so it is never doubled and
// always last). Text with no content is returned as it is.
func Mark(body string) string {
	body = Strip(body)
	if body == "" {
		return ""
	}
	return body + "\n\n" + Marker
}

// Strip removes the AI mark (and the blank line before it) from the end of
// body, and trailing whitespace.
func Strip(body string) string {
	body = strings.TrimRight(body, " \t\r\n")
	if rest, ok := strings.CutSuffix(body, Marker); ok {
		return strings.TrimRight(rest, " \t\r\n")
	}
	return body
}

// Marked reports whether body ends with the AI mark.
func Marked(body string) bool {
	return strings.HasSuffix(strings.TrimRight(body, " \t\r\n"), Marker)
}

// quickAction matches the start of a line a forge may run as a command: a
// slash and a word character, after optional indentation. It is deliberately
// generic: no list of action names to go out of date, so "/spend_time",
// "/unlock" and whatever GitLab adds next are covered. A file path at the start
// of a line ("/usr/bin/env") is escaped as well, which renders the same.
var quickAction = regexp.MustCompile(`^([ \t]*)/(\w)`)

// NoQuickActions makes every line of body that starts with "/command"
// harmless by putting a backslash before the slash: Markdown renders
// "\/approve" as "/approve", and the forge no longer sees a command. Lines
// inside a closed fenced code block are code, not commands, and are left as
// they are. A fence that is never closed protects nothing: the forge may not
// treat it as code either.
func NoQuickActions(body string) string {
	if !strings.Contains(body, "/") {
		return body
	}
	lines := strings.Split(body, "\n")
	code := fencedLines(lines)
	for i, l := range lines {
		if !code[i] {
			lines[i] = quickAction.ReplaceAllString(l, `$1\/$2`)
		}
	}
	return strings.Join(lines, "\n")
}

// fencedLines marks the lines of every closed fenced code block, fences
// included.
func fencedLines(lines []string) []bool {
	code := make([]bool, len(lines))
	for i := 0; i < len(lines); {
		ch, n, ok := fenceOpen(lines[i])
		if !ok {
			i++
			continue
		}
		j := i + 1
		for ; j < len(lines); j++ {
			if fenceClose(lines[j], ch, n) {
				break
			}
		}
		if j == len(lines) { // never closed
			i++
			continue
		}
		for k := i; k <= j; k++ {
			code[k] = true
		}
		i = j + 1
	}
	return code
}

// fenceOpen reads a code fence opener: three or more backticks or tildes, up
// to three spaces of indentation (CommonMark).
func fenceOpen(line string) (ch byte, n int, ok bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 || len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return 0, 0, false
	}
	ch = t[0]
	for n < len(t) && t[n] == ch {
		n++
	}
	if n < 3 || (ch == '`' && strings.IndexByte(t[n:], '`') >= 0) { // a backtick fence's info string has none
		return 0, 0, false
	}
	return ch, n, true
}

// fenceClose reports whether line closes a fence opened with n of ch: at
// least as many of the same character and nothing else.
func fenceClose(line string, ch byte, n int) bool {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return false
	}
	t = strings.TrimRight(t, " \t\r")
	if len(t) < n {
		return false
	}
	for i := range len(t) {
		if t[i] != ch {
			return false
		}
	}
	return true
}
