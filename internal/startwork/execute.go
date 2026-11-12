package startwork

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/store"
)

// Outcomes of a step.
const (
	OutcomeCreated = "created"
	OutcomeExists  = "exists"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
	OutcomeWould   = "would create" // dry run
)

// StepResult is what one step did.
type StepResult struct {
	Step
	Outcome string `json:"outcome"`
	Ref     string `json:"ref,omitempty"`
	URL     string `json:"url,omitempty"`
	Error   string `json:"error,omitempty"`
}

// TargetResult is where one repo's work ended up.
type TargetResult struct {
	Repo     string     `json:"repo"`
	Project  string     `json:"project"`
	Child    *forge.Ref `json:"child,omitempty"`
	ChildURL string     `json:"child_url,omitempty"`
	Branch   string     `json:"branch,omitempty"`
	MR       *forge.Ref `json:"mr,omitempty"`
	MRURL    string     `json:"mr_url,omitempty"`
	Worktree string     `json:"worktree,omitempty"`
}

// Preview is what a dry run shows of the MR it would open.
type Preview struct {
	Repo    string        `json:"repo"`
	Branch  string        `json:"branch"`
	Title   string        `json:"title"`
	Body    string        `json:"body"`
	Actions []QuickAction `json:"quick_actions,omitempty"`
	// Stripped names the quick actions removed from Body.
	Stripped []string `json:"stripped,omitempty"`
}

// Result is the outcome of Execute (or a dry run).
type Result struct {
	Item    forge.Ref      `json:"item"`
	Mode    Mode           `json:"mode"`
	DryRun  bool           `json:"dry_run,omitempty"`
	Targets []TargetResult `json:"targets"`
	Steps   []StepResult   `json:"steps"`
	Notes   []string       `json:"notes,omitempty"`
	// Previews are the MR bodies a dry run would send.
	Previews []Preview `json:"previews,omitempty"`
}

// Created counts the steps that changed something.
func (r Result) Created() int {
	n := 0
	for _, s := range r.Steps {
		if s.Outcome == OutcomeCreated {
			n++
		}
	}
	return n
}

// Failed lists the failed steps' errors.
func (r Result) Failed() error {
	var errs []error
	for _, s := range r.Steps {
		if s.Outcome == OutcomeFailed {
			errs = append(errs, fmt.Errorf("%s: %s", s.Text, s.Error))
		}
	}
	return errors.Join(errs...)
}

func (e *Env) actor() string {
	if e.Actor != "" {
		return e.Actor
	}
	return "user"
}

func (e *Env) audit(p Plan, action, target string, params map[string]any, url string, err error) {
	ent := audit.Entry{
		Actor: e.actor(), Action: action, Target: target, Item: p.Item.String(), Project: p.Project,
		Params: params, URL: url, Outcome: "ok",
	}
	if err != nil {
		ent.Outcome, ent.Error = "error", err.Error()
	}
	// An unwritable audit log must not undo a forge write that already happened.
	_ = e.App.Audit.Append(ent)
}

// DryRun reports what Execute would do without doing it. It is audited as a
// single skipped entry so the log shows the preview happened.
func (e *Env) DryRun(p Plan) Result {
	res := Result{Item: p.Item, Mode: p.Mode, DryRun: true}
	for _, tp := range p.Targets {
		tr := TargetResult{Repo: tp.Target.Name, Project: tp.Target.Project, Branch: tp.Found.Branch, Worktree: tp.Found.Worktree}
		if tp.Found.Child != nil {
			ref := tp.Found.Child.Ref
			tr.Child, tr.ChildURL = &ref, tp.Found.Child.WebURL
		}
		if tp.Found.MR != nil {
			ref := tp.Found.MR.Ref
			tr.MR, tr.MRURL = &ref, tp.Found.MR.WebURL
		}
		res.Targets = append(res.Targets, tr)
	}
	for _, tp := range p.Targets {
		if tp.Pinned {
			res.Notes = append(res.Notes, tp.PinnedNote())
		}
	}
	for _, tp := range p.Targets {
		if st := stepOf(p, StepMR, tp.Target.Name); st != nil && !st.Done {
			var child forge.Ref
			if tp.Found.Child != nil {
				child = tp.Found.Child.Ref
			}
			res.Previews = append(res.Previews, Preview{
				Repo: tp.Target.Name, Branch: tp.Branch, Title: tp.MR.Title, Body: tp.MRBody(child, p.Item),
				Actions: tp.MR.Actions, Stripped: tp.MR.Stripped,
			})
		}
	}
	for _, s := range p.Steps {
		out := OutcomeWould
		if s.Done {
			out = OutcomeExists
		}
		res.Steps = append(res.Steps, StepResult{Step: s, Outcome: out})
	}
	res.Notes = append(res.Notes, p.Warnings...)
	if p.WorkflowNote != "" {
		res.Notes = append(res.Notes, p.WorkflowNote)
	}
	_ = e.App.Audit.Append(audit.Entry{
		Actor: e.actor(), Action: "start.plan", Target: p.Item.String(), Item: p.Item.String(), Project: p.Project,
		Params: map[string]any{"mode": string(p.Mode), "pending": p.Pending(), "writes": p.Writes()},
		DryRun: true, Outcome: "skipped",
	})
	return res
}

// runtime is the state of one target while its steps run.
type runtime struct {
	tp       TargetPlan
	child    forge.Ref
	branch   string
	mr       *forge.MR
	worktree string
	result   *TargetResult
	failed   bool
}

// Execute performs the plan. Steps already done are reported as existing; a
// failing step stops its target (the others carry on) and a re-run resumes at
// that step, because every step looks for its result before creating it.
func (e *Env) Execute(ctx context.Context, p Plan) (Result, error) {
	m := p.in.Manifest
	res := Result{Item: p.Item, Mode: p.Mode}
	rts := map[string]*runtime{}
	res.Targets = make([]TargetResult, len(p.Targets))
	for i, tp := range p.Targets {
		res.Targets[i] = TargetResult{Repo: tp.Target.Name, Project: tp.Target.Project}
		rt := &runtime{tp: tp, branch: tp.Found.Branch, mr: tp.Found.MR, worktree: tp.Found.Worktree, result: &res.Targets[i]}
		if tp.Found.Child != nil {
			rt.child = tp.Found.Child.Ref
			rt.result.Child, rt.result.ChildURL = &tp.Found.Child.Ref, tp.Found.Child.WebURL
		}
		if rt.branch == "" && (!tp.UseChild || !rt.child.IsZero() || tp.Pinned) {
			rt.branch = tp.Branch
		}
		rt.result.Branch = tp.Found.Branch
		if tp.Found.MR != nil {
			rt.result.MR, rt.result.MRURL = &tp.Found.MR.Ref, tp.Found.MR.WebURL
		}
		rt.result.Worktree = tp.Found.Worktree
		rts[tp.Target.Name] = rt
	}
	res.Notes = append(res.Notes, p.Warnings...)
	if p.WorkflowNote != "" {
		res.Notes = append(res.Notes, p.WorkflowNote)
	}

	for _, s := range p.Steps {
		sr := StepResult{Step: s}
		rt := rts[s.Repo]
		switch {
		case s.Done:
			sr.Outcome = OutcomeExists
		case rt != nil && rt.failed:
			sr.Outcome, sr.Error = OutcomeSkipped, "an earlier step of this repo failed"
		case s.Kind == StepWorkflow && anyFailed(rts):
			// The label says the work started; it has not.
			sr.Outcome, sr.Error = OutcomeSkipped, "an earlier step failed"
		default:
			e.run(ctx, p, m, rt, &sr, &res, rts)
		}
		res.Steps = append(res.Steps, sr)
	}
	e.finish(p, res)
	return res, res.Failed()
}

func (e *Env) run(ctx context.Context, p Plan, m *config.Manifest, rt *runtime, sr *StepResult, res *Result, rts map[string]*runtime) {
	var err error
	switch sr.Kind {
	case StepChild:
		err = e.createChild(ctx, p, rt, sr)
	case StepWorkflow:
		err = e.moveWorkflow(ctx, p, sr)
	case StepLink:
		err = e.link(ctx, p, rt, res)
	case StepIteration:
		err = e.copyIteration(ctx, p, rt, sr, res)
	case StepBranch:
		err = e.createBranch(ctx, p, m, rt, sr)
	case StepMR:
		err = e.createMR(ctx, p, m, rt, sr, res)
	case StepWorktree:
		err = e.addWorktree(ctx, p, m, rt, sr)
	case StepWorkspace:
		err = e.openWorkspace(ctx, p, m, rt, sr, res, rts)
	}
	switch {
	case err != nil:
		sr.Outcome, sr.Error = OutcomeFailed, err.Error()
		if rt != nil {
			rt.failed = true
		}
	case sr.Outcome == "":
		sr.Outcome = OutcomeCreated
	}
}

func (e *Env) createChild(ctx context.Context, p Plan, rt *runtime, sr *StepResult) error {
	spec := rt.tp.Child
	d := forge.IssueDraft{
		Title: spec.Title, Description: spec.Body, Labels: spec.Labels, AssigneeIDs: spec.AssigneeIDs,
		MilestoneID: spec.MilestoneID, Weight: spec.Weight,
	}
	is, err := e.Forge.CreateIssue(ctx, rt.tp.Target.Project, d)
	e.audit(p, "forge.issue.create", rt.tp.Target.Project, map[string]any{
		"title": spec.Title, "labels": spec.Labels, "assignees": spec.AssigneeIDs, "milestone": spec.MilestoneID,
		"parent": p.Item.String(), "iid": is.Ref.IID,
	}, is.WebURL, err)
	if err != nil {
		return err
	}
	rt.child = is.Ref
	rt.result.Child, rt.result.ChildURL = &is.Ref, is.WebURL
	sr.Ref, sr.URL = is.Ref.String(), is.WebURL
	// The branch is named after the child, which only exists now.
	if rt.branch == "" || strings.Contains(rt.branch, ChildIIDPlaceholder) {
		rt.branch = BranchName(p.in.Manifest, spec.Title, p.in.Item, is.Ref.IID)
	}
	return nil
}

func (e *Env) moveWorkflow(ctx context.Context, p Plan, sr *StepResult) error {
	mv := p.Workflow
	var add []string
	if mv.Add != "" {
		add = []string{mv.Add}
	}
	err := e.Forge.SetLabels(ctx, mv.Ref, add, mv.Remove)
	e.audit(p, "forge.issue.labels", mv.Ref.String(), map[string]any{"add": add, "remove": mv.Remove, "reason": "start work"}, "", err)
	sr.Ref = mv.Ref.String()
	return err
}

func (e *Env) link(ctx context.Context, p Plan, rt *runtime, res *Result) error {
	if rt.child.IsZero() {
		return errors.New("no child issue to link")
	}
	err := e.Forge.LinkIssues(ctx, p.Item, rt.child, "relates_to")
	e.audit(p, "forge.issue.link", p.Item.String(), map[string]any{"to": rt.child.String(), "type": "relates_to"}, "", err)
	if err != nil {
		return err
	}
	e.setParent(ctx, p, rt.child, res)
	return nil
}

// parentRefusedTTL is how long a forge's refusal to nest one project's issues
// under another's is remembered: types do not change between starts, and a
// retry on every start would only repeat the refusal.
const parentRefusedTTL = 7 * 24 * time.Hour

// ParentRefusedKey is the kv key of a remembered refusal to place issues of
// childProject under issues of trackerProject.
func ParentRefusedKey(forgeName, trackerProject, childProject string) string {
	return "startwork:parent-refused:" + forgeName + ":" + trackerProject + ":" + childProject
}

// setParent also places the child under the tracker issue in the forge's
// hierarchy, where it has one. It is best effort: the relates_to link and the
// Parent: line already tie the two together, so a refusal (the forge only
// nests some pairs of types, such as an issue under an epic) or an unreachable
// hierarchy is a note, not a failed step, and a refusal of the pair of types is
// remembered (a success forgets it).
func (e *Env) setParent(ctx context.Context, p Plan, child forge.Ref, res *Result) {
	pr, ok := e.Forge.(forge.Parenter)
	if !ok {
		return
	}
	st, serr := e.App.Cache()
	key := ParentRefusedKey(p.in.Manifest.Forge, p.Item.Project, child.Project)
	now := time.Now()
	if serr == nil {
		if refused, ok := store.Peek[bool](st, key, parentRefusedTTL, now); ok && refused {
			res.Notes = append(res.Notes, fmt.Sprintf("%s not placed under %s in the hierarchy: refused earlier (the relates_to link and Parent: line stand)", child, p.Item))
			return
		}
	}
	err := pr.SetParent(ctx, child, p.Item)
	if errors.Is(err, forge.ErrUnavailable) {
		return // an instance without a hierarchy has nothing to warn about
	}
	e.audit(p, "forge.issue.parent", child.String(), map[string]any{"parent": p.Item.String()}, "", err)
	switch {
	case err == nil:
		if serr == nil {
			store.Put(st, key, now, false) // a pair that nests after all is not refused
		}
	case errors.Is(err, forge.ErrParentRefused):
		if serr == nil {
			store.Put(st, key, now, true)
		}
		res.Notes = append(res.Notes, fmt.Sprintf("%s not placed under %s in the hierarchy: the forge does not allow that pair of types (the relates_to link and Parent: line stand)", child, p.Item))
	default:
		res.Notes = append(res.Notes, fmt.Sprintf("%s not placed under %s in the hierarchy: %v (the relates_to link and Parent: line stand)", child, p.Item, err))
	}
}

func (e *Env) copyIteration(ctx context.Context, p Plan, rt *runtime, sr *StepResult, res *Result) error {
	it := rt.tp.Child.Iteration
	if rt.child.IsZero() || it == nil {
		return errors.New("no child issue to set the iteration on")
	}
	err := e.Forge.SetIteration(ctx, rt.child, it.ID)
	e.audit(p, "forge.issue.iteration", rt.child.String(), map[string]any{"iteration": it.ID}, "", err)
	if errors.Is(err, forge.ErrUnavailable) {
		sr.Outcome = OutcomeSkipped
		sr.Error = "iterations are not available on this forge"
		res.Notes = append(res.Notes, "iteration not copied to "+rt.child.String()+": not available on this forge")
		return nil
	}
	return err
}

func (e *Env) createBranch(ctx context.Context, p Plan, m *config.Manifest, rt *runtime, sr *StepResult) error {
	if rt.branch == "" || strings.Contains(rt.branch, ChildIIDPlaceholder) {
		return errors.New("branch name is unknown: the child issue was not created")
	}
	b, err := e.Forge.CreateBranch(ctx, rt.tp.Target.Project, rt.branch, rt.tp.Base)
	if errors.Is(err, forge.ErrExists) {
		sr.Outcome, err = OutcomeExists, nil // raced with another run: the branch is there
	}
	e.audit(p, "forge.branch.create", rt.tp.Target.Project+":"+rt.branch, map[string]any{"from": rt.tp.Base, "outcome": sr.Outcome}, b.WebURL, err)
	rt.result.Branch = rt.branch
	sr.Ref, sr.URL = rt.branch, b.WebURL
	return err
}

func (e *Env) createMR(ctx context.Context, p Plan, m *config.Manifest, rt *runtime, sr *StepResult, res *Result) error {
	if rt.branch == "" {
		return errors.New("no branch to open the MR from")
	}
	d := forge.MRDraft{
		Project: rt.tp.Target.Project, Source: rt.branch, Target: rt.tp.Base, Title: rt.tp.MR.Title,
		Description: rt.tp.MRBody(rt.child, p.Item), Draft: rt.tp.MR.Draft,
		CommitPrefix: commitprefix.ForBranch(m, p.Item.IID, rt.child.IID, rt.branch),
	}
	spec := rt.tp.MR
	reviewers := make([]string, len(spec.ReviewerUsers))
	for i, u := range spec.ReviewerUsers {
		d.ReviewerIDs = append(d.ReviewerIDs, u.ID)
		reviewers[i] = u.Username
	}
	if spec.ReviewerGroup != "" && spec.ReviewerMembers == 0 {
		d.ReviewerTeam = spec.ReviewerGroup
	}
	mr, err := e.Forge.CreateMR(ctx, d)
	var reviewerErr error
	if errors.Is(err, forge.ErrReviewersNotRequested) {
		// The MR exists; only the review request failed.
		reviewerErr, err = err, nil
	}
	if errors.Is(err, forge.ErrExists) {
		mrs, lerr := e.Forge.ListMRs(ctx, forge.MRQuery{Project: d.Project, SourceBranch: d.Source})
		if found := pickMR(mrs); lerr == nil && found != nil {
			mr, err, sr.Outcome = *found, nil, OutcomeExists
		}
	}
	params := map[string]any{
		"source": d.Source, "target": d.Target, "draft": d.Draft, "template": rt.tp.MR.Template,
		"stripped": rt.tp.MR.Stripped, "quick_actions": len(rt.tp.MR.Actions), "outcome": sr.Outcome,
	}
	if spec.ReviewerGroup != "" && sr.Outcome != OutcomeExists {
		params["reviewer_group"], params["reviewers"] = spec.ReviewerGroup, reviewers
		if reviewerErr != nil {
			params["reviewers_error"] = reviewerErr.Error()
		}
	}
	e.audit(p, "forge.mr.create", mr.Ref.String(), params, mr.WebURL, err)
	if err != nil {
		return err
	}
	if reviewerErr != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("review not requested from %s on %s: %v", spec.ReviewerGroup, mr.Ref, reviewerErr))
	}
	rt.mr = &mr
	rt.result.MR, rt.result.MRURL = &mr.Ref, mr.WebURL
	sr.Ref, sr.URL = mr.Ref.String(), mr.WebURL
	return nil
}

// addWorktree fetches and adds the tracking worktree (2.2): under
// <root>/<project>/<repo>/<branch>, then runs post_worktree hooks. Browse
// uses the default branch's baseline tree.
func (e *Env) addWorktree(ctx context.Context, p Plan, m *config.Manifest, rt *runtime, sr *StepResult) error {
	t := rt.tp.Target
	if t.Tracker {
		sr.Outcome = OutcomeSkipped
		return nil
	}
	bare := e.bare(m, t.Name)
	if !exists(filepath.Join(bare, "HEAD")) {
		return fmt.Errorf("%s is not cloned: run `ade project add %s` first", t.Name, m.Name)
	}
	gc, err := e.gitClient(ctx, m, bare)
	if err != nil {
		return err
	}
	branch := rt.branch
	browse := p.Mode == ModeBrowse
	if browse {
		if branch = rt.tp.Found.DefaultBranch; branch == "" {
			if branch, err = gc.DefaultBranch(ctx, bare); err != nil {
				return err
			}
		}
	}
	if branch == "" || strings.Contains(branch, ChildIIDPlaceholder) {
		return errors.New("branch is unknown: nothing to check out")
	}
	if err := gc.PruneWorktrees(ctx, bare); err != nil {
		return err
	}
	if trees, err := gc.Worktrees(ctx, bare); err == nil {
		if i := slices.IndexFunc(trees, func(w git.Worktree) bool { return w.Branch == branch }); i >= 0 {
			rt.worktree, rt.result.Worktree, sr.Ref, sr.Outcome = trees[i].Path, trees[i].Path, trees[i].Path, OutcomeExists
			e.recordWorktree(ctx, p, m, rt, browse)
			return nil
		}
	}
	dir := filepath.Join(e.App.Root, m.Name, t.Name, git.BranchDir(branch))
	if exists(dir) {
		return fmt.Errorf("%s exists but is not a worktree of %s", dir, branch)
	}
	if err := gc.Fetch(ctx, bare); err != nil {
		e.audit(p, "git.fetch", bare, nil, "", err)
		return err
	}
	err = gc.AddBaseline(ctx, bare, dir, branch)
	e.audit(p, "git.worktree.add", dir, map[string]any{"branch": branch, "repo": t.Name}, "", err)
	if err != nil {
		return err
	}
	rt.worktree, rt.result.Worktree, sr.Ref = dir, dir, dir
	e.recordWorktree(ctx, p, m, rt, browse)
	if !browse {
		e.runHooks(ctx, p, m, t.Name, dir)
	}
	return nil
}

// recordWorktree writes the item's git config into the worktree (ade.parent,
// ade.commitPrefix) so commit hooks and ADE's commits can read it. Baseline
// (browse) trees belong to no item. A failure is a warning: the tree is usable.
func (e *Env) recordWorktree(ctx context.Context, p Plan, m *config.Manifest, rt *runtime, browse bool) {
	if browse {
		return
	}
	prefix := commitprefix.ForBranch(m, p.Item.IID, rt.child.IID, rt.branch)
	if err := commitprefix.WriteWorktreeConfig(ctx, rt.worktree, p.Item.IID, prefix, rt.tp.Base); err != nil {
		e.audit(p, "git.worktree.config", rt.worktree, nil, "", err)
	}
}

// runHooks runs post_worktree hooks; a failing hook raises a warning but
// never fails the start (the tree exists and is usable).
func (e *Env) runHooks(ctx context.Context, p Plan, m *config.Manifest, repo, dir string) {
	for _, cmd := range m.Repos[repo].Hooks.PostWorktree {
		out, err := git.RunHook(ctx, dir, cmd)
		e.audit(p, "git.hook", dir, map[string]any{"hook": "post_worktree", "cmd": cmd}, "", err)
		if err != nil {
			ev := events.New("git.hook.failed", events.Warn, "post_worktree hook failed in "+repo+": "+cmd)
			ev.Project, ev.Subject, ev.Detail = p.Project, repo+"/"+filepath.Base(dir), strings.TrimSpace(out)
			e.App.Bus.Publish(ev)
		}
	}
}

func (e *Env) openWorkspace(ctx context.Context, p Plan, m *config.Manifest, rt *runtime, sr *StepResult, res *Result, rts map[string]*runtime) error {
	if e.Workspace == nil {
		sr.Outcome = OutcomeSkipped
		sr.Error = "no workspace host available"
		return nil
	}
	if rt.worktree == "" {
		sr.Outcome = OutcomeSkipped
		sr.Error = "no worktree to open"
		return nil
	}
	trees := map[string]string{}
	for name, r := range rts {
		if r.worktree != "" {
			trees[name] = r.worktree
		}
	}
	title := rt.branch
	if p.Mode == ModeBrowse {
		title = rt.tp.Target.Name
	}
	err := e.Workspace(ctx, WorkspaceRequest{
		Project: m.Name, Repo: rt.tp.Target.Name, Worktree: rt.worktree, Title: title, ItemTrees: trees,
		Layout: p.in.Choices.Layout, Item: itemAfterStart(p.in.Item, rts), Forge: m.Forge,
	})
	if err != nil {
		// The work exists; a tab that did not open is a warning, not a failed start.
		sr.Outcome, sr.Error = OutcomeSkipped, err.Error()
		res.Notes = append(res.Notes, "workspace not opened: "+err.Error())
		return nil
	}
	return nil
}

// finish publishes the summary event.
func (e *Env) finish(p Plan, res Result) {
	if err := res.Failed(); err != nil {
		ev := events.New("work.start.failed", events.Error, "start work failed on "+p.Item.String())
		ev.Project, ev.Subject, ev.Detail = p.Project, p.Item.String(), err.Error()
		e.App.Bus.Publish(ev)
		return
	}
	title := "Started " + p.Item.String()
	kind := "work.started"
	var parts []string
	for _, t := range res.Targets {
		switch {
		case t.MR != nil:
			parts = append(parts, fmt.Sprintf("%s %s", t.Repo, t.MR))
		case t.Child != nil:
			parts = append(parts, fmt.Sprintf("%s %s", t.Repo, t.Child))
		default:
			parts = append(parts, t.Repo)
		}
	}
	if p.Mode == ModeBrowse {
		kind, title = "work.browse", "Browsing "+p.Item.String()
	}
	if p.in.Choices.OnlyChild {
		kind, title = "work.child", "Child issue for "+p.Item.String()
	}
	if res.Created() == 0 {
		kind = "work.resumed"
		title = "Nothing to do for " + p.Item.String() + ": already started"
	}
	ev := events.New(kind, events.Success, title+" · "+strings.Join(parts, " · "))
	ev.Project, ev.Subject = p.Project, p.Item.String()
	ev.Transient = res.Created() == 0
	for _, t := range res.Targets {
		if t.MRURL != "" {
			ev.Link = t.MRURL
			break
		}
	}
	e.App.Bus.Publish(ev)
}

func anyFailed(rts map[string]*runtime) bool {
	for _, rt := range rts {
		if rt.failed {
			return true
		}
	}
	return false
}

func stepOf(p Plan, kind StepKind, repo string) *Step {
	for i := range p.Steps {
		if p.Steps[i].Kind == kind && p.Steps[i].Repo == repo {
			return &p.Steps[i]
		}
	}
	return nil
}

// itemAfterStart is the work item with the child issues and MRs start work just
// created added, which the sync cache does not know yet: the workspace shows
// them and the agent skill names them.
func itemAfterStart(it domain.WorkItem, rts map[string]*runtime) domain.WorkItem {
	it.Children = slices.Clone(it.Children)
	it.MRs = slices.Clone(it.MRs)
	for _, name := range slices.Sorted(maps.Keys(rts)) {
		rt := rts[name]
		if !rt.child.IsZero() && !slices.ContainsFunc(it.Children, func(c forge.Issue) bool { return c.Ref == rt.child }) {
			c := forge.Issue{Ref: rt.child, Title: rt.tp.Child.Title, Description: rt.tp.Child.Body, State: "opened"}
			if rt.result.ChildURL != "" {
				c.WebURL = rt.result.ChildURL
			}
			it.Children = append(it.Children, c)
		}
		if rt.mr != nil && !slices.ContainsFunc(it.MRs, func(m forge.MR) bool { return m.Ref == rt.mr.Ref }) {
			it.MRs = append(it.MRs, *rt.mr)
		}
	}
	return it
}
