// Package joblog cleans raw CI job traces for display and for prompts: the
// MR page's log viewer and the AI CI triage read the same text.
package joblog

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Clean turns a raw job trace into the lines to show: ANSI colour is
// dropped (the log is shown in ADE's own ink), a carriage-return line keeps
// what its last non-empty segment left on screen, GitLab's section markers
// become a "── name" header (section_end is dropped), tabs expand to the next
// multiple of 8 columns and other control characters go. A log with no text
// has no lines.
func Clean(text string) []string {
	var lines []string
	for raw := range strings.SplitSeq(strings.TrimRight(text, "\r\n"), "\n") {
		var shown string
		var headers []string
		marked := false
		for seg := range strings.SplitSeq(strings.TrimSuffix(raw, "\r"), "\r") {
			seg = ansi.Strip(seg)
			switch {
			case strings.HasPrefix(seg, "section_start:"):
				marked = true
				if f := strings.SplitN(seg, ":", 3); len(f) == 3 {
					name, _, _ := strings.Cut(f[2], "[") // "[collapsed=true]" options
					headers = append(headers, "── "+strings.TrimSpace(name))
				}
			case strings.HasPrefix(seg, "section_end:"):
				marked = true
			case strings.TrimSpace(seg) != "":
				shown = seg
			}
		}
		lines = append(lines, headers...)
		if shown != "" || !marked {
			lines = append(lines, plainLine(shown))
		}
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.TrimSpace(l) != "" }) {
		return nil
	}
	return lines
}

// plainLine expands tabs to the next multiple of 8 columns and drops the
// other C0 controls, DEL and C1 controls, which a terminal would act on.
func plainLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString(strings.Repeat(" ", 8-ansi.StringWidth(b.String())%8))
		case r < 0x20 || r >= 0x7f && r <= 0x9f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
