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
	"unicode"
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
var quickAction = regexp.MustCompile(`^([\s\p{Zs}\x{200b}\x{feff}]*)/(\w)`)

// QuickActionName reports whether line is a quick-action line by the rule
// NoQuickActions applies (indentation, a slash, a word character) and returns
// its command: the slash and the text up to the first whitespace. Callers that
// warn about quick actions use this, so the warning and the escaping agree.
func QuickActionName(line string) (string, bool) {
	m := quickAction.FindStringSubmatchIndex(line)
	if m == nil {
		return "", false
	}
	cmd := line[m[4]-1:]
	if i := strings.IndexFunc(cmd, unicode.IsSpace); i >= 0 {
		cmd = cmd[:i]
	}
	return cmd, true
}

// NoQuickActions makes every line of body that starts with "/command"
// harmless by putting a backslash before the slash: Markdown renders
// "\/approve" as "/approve", and the forge no longer sees a command.
//
// Every such line is escaped, code fences included. Which lines the forge
// treats as code depends on its version and on Markdown nesting: GitLab 16.9
// and later reads commands from top-level paragraphs of the parsed Markdown
// (so an indented fence inside a list item, an HTML comment or a <div> block
// can hide a fence line that is not one, and a "/approve" at column 0 after it
// is live), while older versions matched fenced blocks with a regular
// expression that knew only backtick fences. No reading of fences is right for
// both, so none is attempted. The cost is cosmetic: a code line that starts
// with a slash and a letter inside a code block shows a backslash.
func NoQuickActions(body string) string {
	if !strings.Contains(body, "/") {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = quickAction.ReplaceAllString(l, `$1\/$2`)
	}
	return strings.Join(lines, "\n")
}
