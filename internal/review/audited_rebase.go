package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// Rebase brings the MR's source branch up to date with its target
// (forge.mr.rebase). The wrapped driver must implement forge.MRRebaser; one
// that does not is an error, not a silent no-op. Under dry-run nothing is sent
// and the result is empty.
func (a *AuditedWriter) Rebase(ctx context.Context, mr forge.Ref, o forge.RebaseOpts) (res forge.RebaseResult, err error) {
	rb, ok := a.MRWriter.(forge.MRRebaser)
	if !ok {
		return res, fmt.Errorf("this forge cannot rebase or update a merge request's branch yet (%s)", mr)
	}
	err = a.do("forge.mr.rebase", mr.String(), "", map[string]any{"sha": o.SHA, "skip_ci": o.SkipCI}, func() (e error) {
		res, e = rb.RebaseMR(ctx, mr, o)
		return
	})
	return res, err
}

// LocalRebaseSteps are the git commands that rebase an MR's source branch onto
// its target by hand, for a conflict the forge cannot resolve. dir is the
// worktree on the source branch (for a fork MR, its detached mr-<iid> tree),
// or "" when it is not known. A fork MR's branch is not in origin: the steps
// push to the fork, which needs push access to it, never to origin. ADE never
// runs them: they rewrite history and a conflict needs the author's judgement.
func LocalRebaseSteps(mr forge.MR, dir string) []string {
	var l []string
	switch {
	case dir != "":
		l = append(l, "cd "+shellQuote(dir))
	case mr.Fork:
		l = append(l, fmt.Sprintf("# in the detached tree of the MR (ade mr checkout %s)", mr.Ref))
	default:
		l = append(l, "# in a worktree on "+mr.SourceBranch)
	}
	l = append(l,
		"git fetch origin",
		"git rebase origin/"+mr.TargetBranch,
		"# fix each conflict, then: git add <files> && git rebase --continue",
	)
	if mr.Fork {
		fork := mr.SourceProject
		if fork == "" {
			fork = "the deleted fork"
		}
		return append(l,
			"# the branch lives in the fork "+fork+", not in origin: push there (needs push access to it)",
			"git push --force-with-lease <the fork's git url> HEAD:"+mr.SourceBranch,
		)
	}
	return append(l, "git push --force-with-lease origin "+mr.SourceBranch)
}

// shellQuote quotes s for a POSIX shell only when it needs it.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
