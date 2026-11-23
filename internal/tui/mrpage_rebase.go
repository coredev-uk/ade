package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// rebaseKey (u) opens the update-the-branch dialog: the forge rebases the
// source branch onto its target (GitLab) or merges the target into it
// (GitHub). It never needs AI.
func (m *model) rebaseKey() tea.Cmd {
	d := m.mr.data
	if d.mr.State != "opened" {
		m.notice(events.Warn, d.ref.String()+" is "+d.mr.State+": nothing to update")
		return nil
	}
	if d.busy != "" {
		m.notice(events.Warn, "Wait for "+d.busy+" to finish")
		return nil
	}
	m.mr.modal = &mrModal{kind: modalRebase}
	return nil
}

// rebaseModalKey: enter sends the request, y copies the git commands that do
// it by hand, for a conflict the forge cannot resolve.
func (m *model) rebaseModalKey(ks string) tea.Cmd {
	switch ks {
	case "enter":
		m.mr.modal = nil
		return m.actRebase()
	case "y":
		steps := review.LocalRebaseSteps(m.mr.data.mr, "")
		e := events.New("ui.copied", events.Info, "copied the git commands to rebase "+m.mr.data.mr.SourceBranch)
		e.Transient = true
		m.nc.push(e)
		return tea.SetClipboard(strings.Join(steps, "\n"))
	}
	return nil
}

// actRebase asks the forge to bring the source branch up to date with its
// target (forge.mr.rebase), pinned to the head the page showed. When the forge
// cannot, the failure pop-up carries the git commands to do it locally.
func (m *model) actRebase() tea.Cmd {
	d := m.mr.data
	mr := d.mr
	return m.mrWrite(d, "rebase", "updating the branch", func(ctx context.Context, w forge.MRWriter) (mrWrote, error) {
		aw, ok := w.(*review.AuditedWriter)
		if !ok {
			return mrWrote{}, errors.New("the writer does not audit: not writing")
		}
		res, err := aw.Rebase(ctx, d.ref, forge.RebaseOpts{SHA: mr.HeadSHA})
		if errors.Is(err, forge.ErrRebaseFailed) {
			return mrWrote{}, fmt.Errorf("%w\n\nrebase it locally, then push:\n  %s", err,
				strings.Join(review.LocalRebaseSteps(mr, ""), "\n  "))
		}
		if err != nil {
			return mrWrote{}, err
		}
		switch {
		case res.UpToDate:
			return mrWrote{done: fmt.Sprintf("%s is already up to date with %s", d.ref, mr.TargetBranch), reload: relMR}, nil
		case res.Method == "merge":
			return mrWrote{done: fmt.Sprintf("Merged %s into %s on %s", mr.TargetBranch, mr.SourceBranch, d.ref), reload: relMR | relJobs | relDiff}, nil
		}
		return mrWrote{done: fmt.Sprintf("Rebased %s onto %s", d.ref, mr.TargetBranch), reload: relMR | relJobs | relDiff}, nil
	})
}

// rebaseDialog says what the forge will do and, while it reports conflicts,
// the git commands that do it by hand.
func (m model) rebaseDialog(inner int) (string, []string) {
	d := m.mr.data
	mr := d.mr
	vl := stFaint.Render(m.g.Vertical) + " "
	l := []string{vl + stBold.Render(mr.SourceBranch) + stFaint.Render(m.g.T(" with ")) + stBold.Render(mr.TargetBranch)}
	switch {
	case mr.HasConflicts:
		l = append(l, vl+stWarn.Render(m.g.Warn+" it conflicts with "+mr.TargetBranch+": the forge will probably refuse"))
	case mr.DetailedStatus == "need_rebase" || mr.DetailedStatus == "behind":
		l = append(l, vl+stOK.Render(m.g.OK+" behind "+mr.TargetBranch+": the forge can update it"))
	default:
		l = append(l, vl+stFaint.Render("no conflicts or lag reported"))
	}
	l = append(l, "",
		stFaint.Render(ansi.Wrap("GitLab rebases the source branch (it is rewritten, so a checkout of it needs a reset); GitHub merges the target into it. The head you see now is pinned where the forge can.", inner, "")))
	if mr.HasConflicts {
		l = append(l, "", stBold.Render("By hand, in the worktree (w opens it):"))
		for _, s := range review.LocalRebaseSteps(mr, "") {
			l = append(l, vl+stFaint.Render(ansi.Truncate(s, max(10, inner-2), m.g.Ell)))
		}
	}
	return m.g.T("Update branch · ") + d.ref.String(), l
}
