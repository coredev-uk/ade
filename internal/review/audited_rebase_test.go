package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
)

// rebaser records rebases; the rest of MRWriter is never called.
type rebaser struct {
	forge.MRWriter
	opts []forge.RebaseOpts
	err  error
}

func (w *rebaser) RebaseMR(_ context.Context, _ forge.Ref, o forge.RebaseOpts) (forge.RebaseResult, error) {
	w.opts = append(w.opts, o)
	return forge.RebaseResult{Method: "rebase", HeadSHA: "abc"}, w.err
}

func TestRebaseIsAuditedAndDryRunSendsNothing(t *testing.T) {
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	mr := forge.Ref{Project: "grp/api", IID: 2, Kind: forge.KindMR}
	w := &rebaser{}
	res, err := Audited(w, log, false).Rebase(context.Background(), mr, forge.RebaseOpts{SHA: "old", SkipCI: true})
	if err != nil || res.HeadSHA != "abc" || len(w.opts) != 1 || w.opts[0].SHA != "old" {
		t.Fatalf("rebase = %+v, %v, sent %+v", res, err, w.opts)
	}
	es, _ := log.Query(audit.Filter{Action: "forge.mr.rebase"})
	if len(es) != 1 || es[0].Item != mr.String() || es[0].Outcome != "ok" || es[0].Params["sha"] != "old" || es[0].Params["skip_ci"] != true {
		t.Fatalf("audit = %+v", es)
	}

	res, err = Audited(w, log, true).Rebase(context.Background(), mr, forge.RebaseOpts{})
	if err != nil || res != (forge.RebaseResult{}) || len(w.opts) != 1 {
		t.Fatalf("dry run sent: %+v %v %v", res, err, w.opts)
	}
	es, _ = log.Query(audit.Filter{Action: "forge.mr.rebase"})
	if len(es) != 2 || !es[1].DryRun || es[1].Outcome != "skipped" {
		t.Fatalf("dry-run audit: %+v", es)
	}

	// A failure is recorded and keeps its kind for the caller.
	w.err = forge.ErrRebaseFailed
	if _, err := Audited(w, log, false).Rebase(context.Background(), mr, forge.RebaseOpts{}); !errors.Is(err, forge.ErrRebaseFailed) {
		t.Fatalf("err = %v", err)
	}
	if es, _ = log.Query(audit.Filter{Action: "forge.mr.rebase"}); len(es) != 3 || es[2].Outcome != "error" {
		t.Fatalf("failure audit: %+v", es)
	}
}

func TestRebaseOnADriverWithoutItIsAnError(t *testing.T) {
	_, err := Audited(&struct{ forge.MRWriter }{}, nil, false).Rebase(context.Background(), forge.Ref{Project: "g/p", IID: 1, Kind: forge.KindMR}, forge.RebaseOpts{})
	if err == nil || !strings.Contains(err.Error(), "cannot rebase or update") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalRebaseSteps(t *testing.T) {
	mr := forge.MR{SourceBranch: "12-fix", TargetBranch: "main"}
	got := strings.Join(LocalRebaseSteps(mr, "/work/api/12 fix"), "\n")
	for _, want := range []string{"cd '/work/api/12 fix'", "git fetch origin", "git rebase origin/main", "git push --force-with-lease origin 12-fix"} {
		if !strings.Contains(got, want) {
			t.Errorf("steps lack %q:\n%s", want, got)
		}
	}
	if anon := strings.Join(LocalRebaseSteps(mr, ""), "\n"); !strings.HasPrefix(anon, "# in a worktree on 12-fix") {
		t.Errorf("steps without a worktree:\n%s", anon)
	}
}

func TestLocalRebaseStepsForAForkNeverPushToOrigin(t *testing.T) {
	mr := forge.MR{Ref: forge.Ref{Project: "g/api", IID: 7, Kind: forge.KindMR}, SourceBranch: "feat", TargetBranch: "main", Fork: true, SourceProject: "alice/api"}
	got := strings.Join(LocalRebaseSteps(mr, "/work/api/mr-7"), "\n")
	if strings.Contains(got, "push --force-with-lease origin") {
		t.Errorf("a fork MR's steps push to origin:\n%s", got)
	}
	for _, want := range []string{"cd /work/api/mr-7", "git rebase origin/main", "alice/api", "HEAD:feat"} {
		if !strings.Contains(got, want) {
			t.Errorf("steps lack %q:\n%s", want, got)
		}
	}
}
