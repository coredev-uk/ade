// Package itemdoc reads the structure teams put in issue descriptions: an
// "Acceptance criteria" section and task-list checkboxes. It is pure text
// handling, shared by the issue-overview pane and the agent skill.
package itemdoc

import (
	"regexp"
	"strings"
)

// Task is one `- [ ]` / `- [x]` line of a description.
type Task struct {
	Done bool
	Text string
}

// Doc is a description split into what readers look for.
type Doc struct {
	// Body is the description without the acceptance section.
	Body string
	// Acceptance is the text of the acceptance section (heading excluded); empty when none.
	Acceptance string
	// Tasks are the checkboxes of the whole description, in order.
	Tasks []Task
}

var (
	headingRE = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	// boldHeadRE matches "**Acceptance criteria**" or "**Acceptance criteria:**" on its own line.
	boldHeadRE = regexp.MustCompile(`^\*\*(.+?):?\*\*:?\s*$`)
	acceptRE   = regexp.MustCompile(`(?i)^\s*acceptance(\s+criteria)?\s*:?\s*$`)
	taskRE     = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX])\]\s+(.*)$`)
)

// Parse splits a description. Markdown fences are respected: a "# comment" in
// a code block is not a heading and a checkbox in one is not a task.
func Parse(description string) Doc {
	lines := strings.Split(strings.ReplaceAll(description, "\r\n", "\n"), "\n")
	var d Doc
	var body, accept []string
	inFence := false
	inAccept := false
	acceptLevel := 0
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "```") || strings.HasPrefix(strings.TrimSpace(ln), "~~~") {
			inFence = !inFence
		}
		if !inFence {
			if m := taskRE.FindStringSubmatch(ln); m != nil {
				d.Tasks = append(d.Tasks, Task{Done: m[1] != " ", Text: strings.TrimSpace(m[2])})
			}
			level, title, isHead := heading(ln)
			if isHead {
				switch {
				case acceptRE.MatchString(title):
					inAccept, acceptLevel = true, level
					continue
				case inAccept && level <= acceptLevel:
					inAccept = false
				}
			}
		}
		if inAccept {
			accept = append(accept, ln)
		} else {
			body = append(body, ln)
		}
	}
	d.Body = strings.TrimSpace(strings.Join(body, "\n"))
	d.Acceptance = strings.TrimSpace(strings.Join(accept, "\n"))
	return d
}

// heading reports a markdown heading (level 1-6) or a bold-only line (level 7,
// weaker than any heading, so it ends a section only at the same level).
func heading(ln string) (level int, title string, ok bool) {
	if m := headingRE.FindStringSubmatch(ln); m != nil {
		return len(m[1]), m[2], true
	}
	if m := boldHeadRE.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
		return 7, m[1], true
	}
	return 0, "", false
}

// Progress counts done and total tasks.
func (d Doc) Progress() (done, total int) {
	for _, t := range d.Tasks {
		if t.Done {
			done++
		}
	}
	return done, len(d.Tasks)
}
