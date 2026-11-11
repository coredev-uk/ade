package workspace

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/host/zellij"
)

// Layout names a workspace layout (PLAN 6.6).
const (
	LayoutCode   = "code"   // editor 60% | overview, lazygit + shell stacked
	LayoutAgent  = "agent"  // omp with the ADE implement skill 60% | overview, lazygit + shell stacked
	LayoutReview = "review" // diff through delta 60% | lazygit + shell stacked
	LayoutManual = "manual" // shell 60% | overview, lazygit + shell stacked: no agent, no editor pane
)

// ValidLayout reports whether name is a known layout.
func ValidLayout(name string) bool {
	switch name {
	case LayoutCode, LayoutAgent, LayoutReview, LayoutManual:
		return true
	}
	return false
}

// Tools are the programs panes run. An empty field means the tool is not
// installed and its pane falls back to a plain shell.
type Tools struct {
	Ade    string
	Editor string
	// EditorArgs, when set, are the complete arguments of Editor, which is then
	// a single program (a GUI launcher gets its directory and a trailing shell
	// there); otherwise Editor is split into a program and its arguments.
	EditorArgs []string
	Lazygit    string
	Delta      string
	Omp        string
}

// Pane is one command pane.
type Pane struct {
	Name string
	Args []string // arguments of Tools.Ade
}

// LayoutSpec is everything the KDL of one workspace tab depends on.
type LayoutSpec struct {
	Layout string
	Tab    string
	Dir    string // the worktree: cwd of every pane
	// BaseRef is what the review layout diffs against (e.g. origin/main).
	BaseRef string
	Tools   Tools
	// Overview holds the arguments of `ade` that show the work item live
	// (`item <ref> --watch`); empty when the worktree belongs to no item, which
	// leaves the pane out.
	Overview []string
	// Agent holds the arguments of omp in the agent layout: the config overlay
	// that loads the ADE implement skill and the opening prompt.
	Agent []string
	// Up is `ade ws up --no-zellij` for the whole run graph: the strip's first
	// pane, the one-step way to start everything in order.
	Up Pane
	// Services are requirements running elsewhere (other trees, services.*), Tasks
	// the workspace's own. Every one gets a pane of the strip along the bottom.
	// None starts when the tab opens (zellij start_suspended): the pane shows its
	// command and waits for Enter; Ctrl-C stops it and Enter starts it again.
	Services []Pane
	Tasks    []Pane
}

// Pane sizes, in percent: the main pane's share of the top row, the overview's
// share of the right-hand column (the stack below takes the rest), and the strip's share of the tab.
const (
	mainPct     = 60
	overviewPct = 40
	stripPct    = 30
)

// BuildLayout renders the zellij KDL for one workspace tab. Every string comes
// from the file system or user config, so all of them are escaped.
//
//	main (editor | omp | shell)  | overview   <- ade item --watch
//	                             | lazygit / shell (stacked)
//	services and tasks, one pane each (when the workspace runs any), all waiting for Enter
func BuildLayout(s LayoutSpec) (string, error) {
	if !ValidLayout(s.Layout) {
		return "", fmt.Errorf("unknown layout %q (want code, agent, review or manual)", s.Layout)
	}
	if s.Tab == "" || s.Dir == "" {
		return "", fmt.Errorf("layout needs a tab name and a directory")
	}
	var b kdl
	b.line(0, "layout {")
	b.WriteString(zellij.TabTemplate)
	b.line(1, "tab name=%s cwd=%s focus=true {", zellij.Quote(s.Tab), zellij.Quote(s.Dir))
	hasStrip := len(s.Services)+len(s.Tasks) > 0

	topAttr := ""
	if hasStrip {
		topAttr = fmt.Sprintf(` size="%d%%"`, 100-stripPct)
	}
	b.line(2, `pane%s split_direction="vertical" {`, topAttr)
	b.pane(3, fmt.Sprintf(`size="%d%%" focus=true`, mainPct), s.primary())
	right := 100 - mainPct
	if len(s.Overview) > 0 {
		b.line(3, `pane size="%d%%" split_direction="horizontal" {`, right)
		b.pane(4, fmt.Sprintf(`name="overview" size="%d%%"`, overviewPct), s.adeCmd(Pane{Args: s.Overview}))
		// The stack takes the rest: giving both children a percentage makes
		// zellij size them differently from one run to the next.
		b.line(4, "pane stacked=true {")
		s.stackPanes(&b, 5)
		b.line(4, "}")
		b.line(3, "}")
	} else {
		b.line(3, `pane size="%d%%" stacked=true {`, right)
		s.stackPanes(&b, 4)
		b.line(3, "}")
	}
	b.line(2, "}")
	if hasStrip {
		b.line(2, `pane size="%d%%" split_direction="vertical" {`, stripPct)
		if len(s.Up.Args) > 0 {
			c := s.adeCmd(s.Up)
			c.suspended = true
			b.pane(3, fmt.Sprintf("name=%s", zellij.Quote(s.Up.Name)), c)
		}
		for _, p := range append(append([]Pane(nil), s.Services...), s.Tasks...) {
			c := s.adeCmd(p)
			c.suspended = true
			b.pane(3, fmt.Sprintf("name=%s", zellij.Quote(p.Name)), c)
		}
		b.line(2, "}")
	}
	b.line(1, "}")
	b.line(0, "}")
	return b.String(), nil
}

// stackPanes writes the stack beside the main pane: lazygit when installed, and a shell.
func (s LayoutSpec) stackPanes(b *kdl, depth int) {
	if cmd, ok := s.lazygit(); ok {
		b.pane(depth, `name="lazygit" expanded=true`, cmd)
		b.pane(depth, `name="shell"`, command{})
		return
	}
	b.pane(depth, `name="shell" expanded=true`, command{})
}

func (s LayoutSpec) adeCmd(p Pane) command {
	if s.Tools.Ade == "" {
		return command{}
	}
	return command{prog: s.Tools.Ade, args: p.Args}
}

func (s LayoutSpec) lazygit() (command, bool) {
	if s.Tools.Lazygit == "" {
		return command{}, false
	}
	return command{prog: s.Tools.Lazygit}, true
}

// primary is the large left pane.
func (s LayoutSpec) primary() command {
	switch s.Layout {
	case LayoutCode:
		if len(s.Tools.EditorArgs) > 0 {
			return command{prog: s.Tools.Editor, args: s.Tools.EditorArgs, name: "editor"}
		}
		if f := strings.Fields(s.Tools.Editor); len(f) > 0 {
			return command{prog: f[0], args: f[1:], name: "editor"}
		}
		return command{name: "editor"}
	case LayoutAgent:
		if s.Tools.Omp != "" {
			return command{prog: s.Tools.Omp, args: s.Agent, name: "agent"}
		}
		return command{name: "agent"}
	case LayoutReview:
		diff := "git diff"
		if s.BaseRef != "" {
			diff = "git diff " + shellQuote(s.BaseRef) + "...HEAD"
		}
		if s.Tools.Delta != "" {
			diff += " | " + shellQuote(s.Tools.Delta)
		}
		return command{prog: "sh", args: []string{"-c", diff}, name: "diff"}
	}
	return command{name: "shell"}
}

// shellQuote single-quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// command is a pane's program; the zero value is a plain shell.
type command struct {
	prog string
	args []string
	name string
	// suspended shows the command in the pane and runs it on Enter, not at once.
	suspended bool
}

// kdl accumulates indented lines.
type kdl struct{ strings.Builder }

func (b *kdl) line(depth int, format string, args ...any) {
	b.WriteString(strings.Repeat("    ", depth))
	fmt.Fprintf(&b.Builder, format, args...)
	b.WriteByte('\n')
}

// pane writes one pane. attrs are raw KDL properties; a command's own name is
// used when attrs carry none.
func (b *kdl) pane(depth int, attrs string, c command) {
	if c.name != "" && !strings.Contains(attrs, "name=") {
		attrs = "name=" + zellij.Quote(c.name) + " " + attrs
	}
	attrs = strings.TrimSpace(attrs)
	if c.prog == "" {
		b.line(depth, "pane %s", attrs)
		return
	}
	if c.suspended {
		attrs += " start_suspended=true"
	}
	if len(c.args) == 0 {
		b.line(depth, "pane %s command=%s", attrs, zellij.Quote(c.prog))
		return
	}
	quoted := make([]string, len(c.args))
	for i, a := range c.args {
		quoted[i] = zellij.Quote(a)
	}
	b.line(depth, "pane %s command=%s {", attrs, zellij.Quote(c.prog))
	b.line(depth+1, "args %s", strings.Join(quoted, " "))
	b.line(depth, "}")
}
