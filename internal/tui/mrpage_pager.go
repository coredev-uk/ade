package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/syntax"
)

// D in the Diff tab hands the MR's whole diff to delta (PLAN 6.2). Inside a
// zellij session it opens in a floating pane that closes when the pager quits;
// anywhere else the dashboard steps aside and the pager runs in this terminal.
// Without delta the pager is the one git diff itself would use ($GIT_PAGER,
// core.pager, $PAGER, else less -R). The diff is the forge's, so it is the
// version the page shows and no checkout is needed.

// patchOf renders the diff as a unified patch: file headers, then the hunks
// the forge returned.
func patchOf(d forge.Diff) string {
	var b strings.Builder
	for _, f := range d.Files {
		old, nw := "a/"+f.OldPath, "b/"+f.NewPath
		fmt.Fprintf(&b, "diff --git %s %s\n", old, nw)
		switch {
		case f.NewFile:
			b.WriteString("new file mode 100644\n")
			old = "/dev/null"
		case f.Deleted:
			b.WriteString("deleted file mode 100644\n")
			nw = "/dev/null"
		case f.OldPath != f.NewPath:
			fmt.Fprintf(&b, "rename from %s\nrename to %s\n", f.OldPath, f.NewPath)
		}
		if f.Diff == "" {
			continue
		}
		fmt.Fprintf(&b, "--- %s\n+++ %s\n%s", old, nw, f.Diff)
		if !strings.HasSuffix(f.Diff, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// shQuote single-quotes s for sh.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// pagerLine is the shell line that shows the patch file: delta when it is on
// PATH, else the pager git diff would use.
func pagerLine(file string, hasDelta bool, gitPager string, getenv func(string) string) string {
	in := " < " + shQuote(file)
	if hasDelta {
		return "delta" + in
	}
	for _, p := range []string{getenv("GIT_PAGER"), gitPager, getenv("PAGER"), "less -R"} {
		if p = strings.TrimSpace(p); p != "" {
			return p + in
		}
	}
	return "less -R" + in
}

// defaultPagerLine is the pager line pagerLine ends on when nothing else is
// configured.
func defaultPagerLine(file string) string { return "less -R < " + shQuote(file) }

// openPager shows the MR's diff in delta or the git pager. The patch is
// written here (it is already in memory); the pager itself runs off the UI
// loop in a zellij pane, or takes the terminal for as long as it is open.
func (m *model) openPager() tea.Cmd {
	d := m.mr.data
	if d == nil || d.diffL.st != loadOK || len(d.diff.Files) == 0 {
		return nil
	}
	ref, name := d.ref, d.ref.String()+" diff"
	f, err := os.CreateTemp("", "ade-mr-*.diff")
	if err != nil {
		m.notice(events.Error, "Could not open the diff: "+oneLine(err.Error()))
		return nil
	}
	_, derr := exec.LookPath("delta")
	var gitPager string
	if derr != nil {
		if out, err := exec.Command("git", "config", "--get", "core.pager").Output(); err == nil {
			gitPager = string(out)
		}
	}
	line := pagerLine(f.Name(), derr == nil, gitPager, os.Getenv)
	patch := patchOf(d.diff)
	if line == defaultPagerLine(f.Name()) {
		// No delta and no pager of the user's own: less -R shows escape
		// sequences, so the patch is coloured here (none under NO_COLOR).
		patch = syntax.Patch(patch, syntax.NoColor())
	}
	_, werr := f.WriteString(patch)
	if err := firstErr(werr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		m.notice(events.Error, "Could not open the diff: "+oneLine(err.Error()))
		return nil
	}
	script := line + "; rm -f " + shQuote(f.Name())
	if os.Getenv("ZELLIJ") == "" {
		return tea.ExecProcess(exec.Command("sh", "-c", script), func(err error) tea.Msg {
			return mrResultMsg{ref: ref, what: "pager", err: err}
		})
	}
	// The pane opens in the session this dashboard lives in and closes when
	// the pager quits, so there is nothing to wait for.
	return func() tea.Msg {
		out, err := exec.Command("zellij", "run", "--floating", "--close-on-exit", "--name", name,
			"--width", "90%", "--height", "90%", "-x", "5%", "-y", "5%", "--", "sh", "-c", script).CombinedOutput()
		if err != nil {
			_ = os.Remove(f.Name())
			err = fmt.Errorf("zellij run: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return mrResultMsg{ref: ref, what: "pager", err: err}
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// onPager finishes the pager: a failure shows as a pop-up.
func (m *model) onPager(msg mrResultMsg) {
	if msg.err != nil {
		m.notice(events.Error, "Could not open the diff: "+oneLine(msg.err.Error()))
	}
}
