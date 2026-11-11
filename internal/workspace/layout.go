package workspace

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/host/zellij"
)

// Layout names a workspace layout (PLAN 6.6).
const (
	LayoutCode   = "code"   // editor 60% | lazygit + shell stacked
	LayoutAgent  = "agent"  // omp 60% | lazygit + tasks stacked
	LayoutReview = "review" // diff through delta 60% | lazygit + shell stacked
	LayoutManual = "manual" // shell 60% | lazygit + tasks stacked: no agent, no editor pane
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
	Ade     string
	Editor  string
	Lazygit string
	Delta   string
	Omp     string
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
	// Services are requirements running elsewhere (other trees, services.*):
	// they go in one stacked pane. Tasks are the workspace's own and each get a pane.
	Services []Pane
	Tasks    []Pane
}

// BuildLayout renders the zellij KDL for one workspace tab. Every string comes
// from the file system or user config, so all of them are escaped.
func BuildLayout(s LayoutSpec) (string, error) {
	if !ValidLayout(s.Layout) {
		return "", fmt.Errorf("unknown layout %q (want code, agent, review or manual)", s.Layout)
	}
	if s.Tab == "" || s.Dir == "" {
		return "", fmt.Errorf("layout needs a tab name and a directory")
	}
	var b kdl
	b.line(0, "layout {")
	b.line(1, "tab name=%s cwd=%s focus=true {", zellij.Quote(s.Tab), zellij.Quote(s.Dir))
	hasTasks := len(s.Services)+len(s.Tasks) > 0
	// The agent and manual layouts keep tasks beside lazygit; the others get a bottom strip.
	taskColumn := s.Layout == LayoutAgent || s.Layout == LayoutManual

	topAttr := ""
	if hasTasks && !taskColumn {
		topAttr = ` size="70%"`
	}
	b.line(2, `pane%s split_direction="vertical" {`, topAttr)
	b.pane(3, `size="60%" focus=true`, s.primary())
	b.line(3, `pane size="%d%%" stacked=true {`, 40)
	if cmd, ok := s.lazygit(); ok {
		b.pane(4, `name="lazygit" expanded=true`, cmd)
	} else {
		b.pane(4, `name="shell" expanded=true`, command{})
	}
	switch {
	case taskColumn:
		s.stackTasks(&b, 4)
	default:
		b.pane(4, `name="shell"`, command{})
	}
	b.line(3, "}")
	b.line(2, "}")
	if hasTasks && !taskColumn {
		b.line(2, `pane size="%d%%" split_direction="vertical" {`, 30)
		if len(s.Services) > 0 {
			b.line(3, `pane name="services" stacked=true {`)
			for i, p := range s.Services {
				attr := fmt.Sprintf("name=%s", zellij.Quote(p.Name))
				if i == 0 {
					attr += " expanded=true"
				}
				b.pane(4, attr, s.adeCmd(p))
			}
			b.line(3, "}")
		}
		for _, p := range s.Tasks {
			b.pane(3, fmt.Sprintf("name=%s", zellij.Quote(p.Name)), s.adeCmd(p))
		}
		b.line(2, "}")
	}
	b.line(1, "}")
	b.line(0, "}")
	return b.String(), nil
}

// stackTasks adds services and tasks as extra panes of the right-hand stack.
func (s LayoutSpec) stackTasks(b *kdl, depth int) {
	for _, p := range append(append([]Pane(nil), s.Services...), s.Tasks...) {
		b.pane(depth, fmt.Sprintf("name=%s", zellij.Quote(p.Name)), s.adeCmd(p))
	}
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
		if f := strings.Fields(s.Tools.Editor); len(f) > 0 {
			return command{prog: f[0], args: f[1:], name: "editor"}
		}
		return command{name: "editor"}
	case LayoutAgent:
		if s.Tools.Omp != "" {
			return command{prog: s.Tools.Omp, name: "agent"}
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
