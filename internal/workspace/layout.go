package workspace

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/devshell"
	"github.com/coredev-uk/ade/internal/host/zellij"
)

// Layout names a workspace layout (PLAN 6.6).
const (
	LayoutCode   = "code"   // a shell in the dev shell and lazygit, stacked, full width
	LayoutAgent  = "agent"  // omp with the ADE implement skill 60% | shell + lazygit stacked
	LayoutReview = "review" // diff through delta 60% | lazygit + shell stacked
	// LayoutManual lays out the tab as code does. It was the shell layout
	// beside an editor layout; with the editor pane gone the two are one, and
	// the name stays valid for configs and manifests that set it.
	LayoutManual = "manual"
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
	Lazygit string
	Delta   string
	Omp     string
	// Shell is the user's shell (UserShell: $SHELL, or the login shell when
	// $SHELL is a dev shell's bash). Empty when unknown.
	Shell string
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
	// Devshell is the worktree's dev shell; the editor, omp and shell panes
	// run inside it when it is active.
	Devshell devshell.Shell
	// Agent holds the arguments of omp in the agent layout: the config overlay
	// that loads the ADE implement skill and the opening prompt.
	Agent []string
	// AgentUnset names variables the agent pane's omp must not inherit (the
	// write tokens of forges with a read token); the other panes keep them.
	AgentUnset []string
	// Up is `ade ws up --no-zellij` for the whole run graph: the strip's first
	// pane, the one-step way to start everything in order.
	Up Pane
	// Services are requirements running elsewhere (other trees, services.*), Tasks
	// the workspace's own. Every Task gets a pane of the strip along the bottom,
	// and so does every Service unless BackgroundServices moves them to a tab of
	// their own. A strip pane does not start when the tab opens (zellij
	// start_suspended): it shows its command and waits for Enter; Ctrl-C stops it
	// and Enter starts it again.
	Services []Pane
	Tasks    []Pane
	// BackgroundServices moves Up and Services out of the strip into a tab of
	// their own (BuildServicesLayout) that starts them, after Install, when the
	// workspace opens. Tasks stay in the strip, waiting for Enter.
	BackgroundServices bool
	// Install is `ade ws install` for the workspace's worktrees: the first pane
	// of the services tab. Empty when no repo declares install commands.
	Install Pane
}

// ServicesTabSuffix follows the workspace tab's name in the name of its services tab.
const ServicesTabSuffix = " · services"

// HasServicesTab reports whether the services run in a tab of their own: it is
// wanted and there is an install step or a service to put in it.
func (s LayoutSpec) HasServicesTab() bool {
	return s.BackgroundServices && (len(s.Install.Args) > 0 || len(s.Services) > 0)
}

// ServicesTab is the name of the services tab.
func (s LayoutSpec) ServicesTab() string { return s.Tab + ServicesTabSuffix }

// Pane sizes, in percent: the main pane's share of the top row and the strip's share of the tab.
const (
	mainPct    = 60
	stripPct   = 30
	installPct = 25 // the install pane's share of the services tab
)

// BuildLayout renders the zellij KDL for one workspace tab. Every string comes
// from the file system or user config, so all of them are escaped.
//
//	code, manual:   shell / lazygit (stacked, the shell in front), full width
//	agent, review:  main (omp | diff) | lazygit / shell (stacked)
//	services and tasks, one pane each (when the workspace runs any), all waiting for Enter
//
// The work item's overview is not part of the tab: the dashboard's home tab
// shows it, and Open makes sure that tab exists.
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
	// With a services tab the strip keeps only the workspace's own tasks.
	var up Pane
	services := s.Services
	if s.HasServicesTab() {
		services = nil
	} else {
		up = s.Up
	}
	hasStrip := len(services)+len(s.Tasks) > 0 || len(up.Args) > 0

	topAttr := ""
	if hasStrip {
		topAttr = fmt.Sprintf(` size="%d%%"`, 100-stripPct)
	}
	if s.Layout == LayoutCode || s.Layout == LayoutManual {
		b.line(2, `pane%s stacked=true {`, topAttr)
		s.stackPanes(&b, 3, true)
		b.line(2, "}")
	} else {
		b.line(2, `pane%s split_direction="vertical" {`, topAttr)
		b.pane(3, fmt.Sprintf(`size="%d%%" focus=true`, mainPct), s.wrap(s.primary()))
		b.line(3, `pane size="%d%%" stacked=true {`, 100-mainPct)
		s.stackPanes(&b, 4, false)
		b.line(3, "}")
		b.line(2, "}")
	}
	if hasStrip {
		b.line(2, `pane size="%d%%" split_direction="vertical" {`, stripPct)
		if len(up.Args) > 0 {
			c := s.adeCmd(up)
			c.suspended = true
			b.pane(3, fmt.Sprintf("name=%s", zellij.Quote(up.Name)), c)
		}
		for _, p := range append(append([]Pane(nil), services...), s.Tasks...) {
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

// BuildServicesLayout renders the zellij KDL of the services tab: the install
// step on top, then one pane per service side by side in start order. Nothing
// is suspended: the panes run when the tab opens and each waits on its own for
// the install and for what it requires (`ade task run --after-install
// --wait-requires`). The tab is not focused, so creating it first and the
// workspace tab second leaves the user on the workspace tab.
func BuildServicesLayout(s LayoutSpec) (string, error) {
	if s.Tab == "" || s.Dir == "" {
		return "", fmt.Errorf("layout needs a tab name and a directory")
	}
	if !s.HasServicesTab() {
		return "", fmt.Errorf("workspace %s has no services tab", s.Tab)
	}
	var b kdl
	b.line(0, "layout {")
	b.WriteString(zellij.TabTemplate)
	b.line(1, "tab name=%s cwd=%s {", zellij.Quote(s.ServicesTab()), zellij.Quote(s.Dir))
	hasInstall := len(s.Install.Args) > 0
	if hasInstall {
		attr := fmt.Sprintf("name=%s", zellij.Quote(s.Install.Name))
		if len(s.Services) > 0 {
			attr += fmt.Sprintf(` size="%d%%"`, installPct)
		}
		b.pane(2, attr, s.adeCmd(s.Install))
	}
	switch len(s.Services) {
	case 0:
	case 1:
		b.pane(2, fmt.Sprintf("name=%s", zellij.Quote(s.Services[0].Name)), s.adeCmd(s.Services[0]))
	default:
		b.line(2, `pane split_direction="vertical" {`)
		for _, p := range s.Services {
			b.pane(3, fmt.Sprintf("name=%s", zellij.Quote(p.Name)), s.adeCmd(p))
		}
		b.line(2, "}")
	}
	b.line(1, "}")
	b.line(0, "}")
	return b.String(), nil
}

// stackPanes writes the stack of a shell and lazygit (when installed). As the
// tab's main pane (shellMain) the shell is in front and focused; beside omp or
// a diff lazygit is.
func (s LayoutSpec) stackPanes(b *kdl, depth int, shellMain bool) {
	shell := s.wrap(s.shell())
	lg, ok := s.lazygit()
	switch {
	case !ok && shellMain:
		b.pane(depth, `name="shell" expanded=true focus=true`, shell)
	case !ok:
		b.pane(depth, `name="shell" expanded=true`, shell)
	case shellMain:
		b.pane(depth, `name="shell" expanded=true focus=true`, shell)
		b.pane(depth, `name="lazygit"`, lg)
	default:
		b.pane(depth, `name="lazygit" expanded=true`, lg)
		b.pane(depth, `name="shell"`, shell)
	}
}

// shell is the shell pane's program: the user's shell when known, so the pane
// does not depend on what zellij's own environment says; else zellij's default.
func (s LayoutSpec) shell() command {
	if s.Tools.Shell == "" {
		return command{}
	}
	return command{prog: s.Tools.Shell}
}

// wrap runs a pane's program inside the worktree's dev shell (direnv exec,
// nix develop) so omp, the diff and the shell see the repo's tools. A plain
// shell pane becomes an explicit program, because zellij needs one to wrap.
// `nix develop` (and `nix print-dev-env`, which `use flake` loads) exports $SHELL as stdenv's bash,
// which has no line editing, so the user's shell is restored inside it: the
// shell pane runs it and every wrapped program (an editor started from the
// shell, omp's shell) sees it as $SHELL. Panes of a tree without a dev shell are untouched.
func (s LayoutSpec) wrap(c command) command {
	if !s.Devshell.Active() {
		return c
	}
	sh := s.Tools.Shell
	switch {
	case c.prog == "" && sh != "":
		c.prog, c.args = sh, nil
	case c.prog == "":
		c.prog, c.args = "sh", []string{"-c", `exec "${SHELL:-sh}"`}
	}
	argv := append([]string{c.prog}, c.args...)
	if sh != "" {
		argv = append([]string{"env", "SHELL=" + sh}, argv...)
	}
	argv = s.Devshell.Wrap(argv)
	c.prog, c.args = argv[0], argv[1:]
	return c
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

// AgentRunEnv names the variable ADE sets to 1 in the environment of every
// agent process it launches, so a git hook can tell an agent's own `git push`
// from the user's.
const AgentRunEnv = "ADE_AGENT_RUN"

// primary is the large left pane of the agent and review layouts.
func (s LayoutSpec) primary() command {
	switch s.Layout {
	case LayoutAgent:
		if s.Tools.Omp != "" {
			// env sets the marker for the agent only and drops the write-token
			// variables; the pane's shell (and a hook that blocks agent pushes)
			// stays unmarked outside this process.
			args := make([]string, 0, 2*len(s.AgentUnset)+2+len(s.Agent))
			for _, v := range s.AgentUnset {
				args = append(args, "-u", v)
			}
			args = append(append(args, AgentRunEnv+"=1", s.Tools.Omp), s.Agent...)
			return command{prog: "env", args: args, name: "agent"}
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
