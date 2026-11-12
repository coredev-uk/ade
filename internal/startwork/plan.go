// Package startwork turns a tracker issue into started work (PLAN 6.3): a
// linked child issue in the code repo, the workflow label moved, a branch, a
// draft MR from the repo's template and a worktree. Plan is pure: it turns the
// issue, the manifest, the user's choices and what already exists into an
// ordered list of steps. Execute performs them and is idempotent: every step
// looks for its result first, so a second run creates nothing and a failed run
// resumes at the first missing step.
package startwork

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
)

// Mode is what start work creates (PLAN 6.3, step 3).
type Mode string

const (
	// ModeDraft creates child, link, branch and a draft MR, plus the worktree.
	ModeDraft Mode = "draft"
	// ModeBranch stops before the MR; `M` creates it later from the template.
	ModeBranch Mode = "branch"
	// ModeBrowse writes nothing to the forge: a worktree to read the code.
	ModeBrowse Mode = "browse"
)

// ParseMode accepts the CLI/manifest spellings.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case ModeDraft, ModeBranch, ModeBrowse:
		return m, nil
	}
	return "", fmt.Errorf("unknown mode %q (want draft, branch or browse)", s)
}

// ChildPolicy is conventions.child_issue.
type ChildPolicy string

const (
	ChildAuto   ChildPolicy = "auto"
	ChildAlways ChildPolicy = "always"
	ChildNever  ChildPolicy = "never"
)

// ParseChildPolicy accepts auto, always, never; empty means auto.
func ParseChildPolicy(s string) (ChildPolicy, error) {
	switch p := ChildPolicy(strings.ToLower(strings.TrimSpace(s))); p {
	case "":
		return ChildAuto, nil
	case ChildAuto, ChildAlways, ChildNever:
		return p, nil
	}
	return "", fmt.Errorf("unknown child policy %q (want auto, always or never)", s)
}

// TrackerRepo names the tracker project itself as a start-work target, for
// work that lives in the tracker (PLAN 6.3, step 1). A manifest repo of the
// same name takes precedence.
const TrackerRepo = "tracker"

// ChildPlaceholder stands for the child issue's reference in an MR body
// before the child exists (and in one edited by hand); Execute substitutes the
// real one.
const ChildPlaceholder = "<child issue, created first>"

// ChildIIDPlaceholder stands for the child's number in a branch name planned
// before the child exists; Execute names the real branch once it does.
const ChildIIDPlaceholder = "<child iid>"

// Choices are the dialog's answers (or the CLI flags).
type Choices struct {
	Repos []string    `json:"repos"` // manifest repo names, or TrackerRepo
	Mode  Mode        `json:"mode"`
	Child ChildPolicy `json:"child"`
	// Template names a file in TemplateDir without ".md". Empty uses
	// conventions.mr.template, then "Default".
	Template string `json:"template,omitempty"`
	// StripActions lists quick-action names to remove from the MR body; "*"
	// strips all of them.
	StripActions []string `json:"strip_actions,omitempty"`
	// MRBody, when set, is the edited MR body and replaces the rendered one.
	// ChildPlaceholder in it becomes the child's reference.
	MRBody string `json:"mr_body,omitempty"`
	// MoveLabel confirms the workflow-label move when labels.workflow.sync is
	// "ask"; with "auto" the move happens unless NoMoveLabel is set.
	MoveLabel   bool `json:"move_label,omitempty"`
	NoMoveLabel bool `json:"no_move_label,omitempty"`
	// OnlyMR plans just the MR (`M` after Branch only): every earlier step
	// must already be done.
	OnlyMR bool `json:"only_mr,omitempty"`
	// OnlyChild plans just the child issue of the tracker issue in each repo,
	// with its link, hierarchy parent and iteration: no branch, MR, worktree,
	// workflow move or workspace (`ade issue child`). Mode and Child are ignored.
	OnlyChild bool `json:"only_child,omitempty"`
	// Layout is the workspace tab's layout (code, agent or manual: editor, omp
	// with the ADE skill, or a shell as the main pane). Empty: the default of
	// host.layout, the manifest's workspace, then code.
	Layout string `json:"layout,omitempty"`
	// Branches names the branch to work on in a repo, instead of the one
	// conventions.branch derives (a second set of changes in a sibling repo).
	// An existing branch and its MR are reused; otherwise the name is created.
	// Entries for repos not in Repos, and Browse, ignore it.
	Branches map[string]string `json:"branches,omitempty"`
}

// ValidBranchName rejects a name git would refuse; the forge has the last word
// on the rest.
func ValidBranchName(name string) error {
	bad := name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") ||
		strings.ContainsFunc(name, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`~^:?*[\`, r)
		})
	if bad {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	return nil
}

// DefaultChoices preselects what the manifest and the issue say: the repos the
// app labels route to, the mode from conventions.mr.create, the child policy
// from conventions.child_issue.
func DefaultChoices(m *config.Manifest, item domain.WorkItem) Choices {
	c := Choices{Repos: slices.Clone(item.Repos), Mode: ModeDraft, Child: ChildAuto}
	if p, err := ParseChildPolicy(m.Conventions.ChildIssue); err == nil {
		c.Child = p
	}
	if m.Conventions.MR.Create == "never" {
		c.Mode = ModeBranch
	}
	return c
}

// RequiredRepos lists the repos the selected ones require (2.1) that are not
// selected themselves: the dialog offers a child there too, off by default.
func RequiredRepos(m *config.Manifest, selected []string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		for _, req := range m.Repos[name].Requires {
			if seen[req] {
				continue
			}
			seen[req] = true
			if !slices.Contains(selected, req) {
				out = append(out, req)
			}
			walk(req)
		}
	}
	for _, s := range selected {
		walk(s)
	}
	slices.Sort(out)
	return out
}

// Target is one repo the work is started in.
type Target struct {
	Name    string `json:"name"`    // manifest repo name, or TrackerRepo
	Project string `json:"project"` // forge project path
	Tracker bool   `json:"tracker,omitempty"`
}

// Found is what already exists for one target; Discover fills it. Plan only
// reads it, which keeps Plan pure and makes re-runs no-ops.
type Found struct {
	DefaultBranch string       `json:"default_branch,omitempty"`
	Child         *forge.Issue `json:"child,omitempty"`
	Linked        bool         `json:"linked,omitempty"`
	Branch        string       `json:"branch,omitempty"`
	MR            *forge.MR    `json:"mr,omitempty"`
	// Cloned reports the repo's bare clone exists under the project root.
	Cloned bool `json:"cloned"`
	// Worktree is the existing local tree of Branch (of the default branch
	// for Browse).
	Worktree string `json:"worktree,omitempty"`
	// Templates are the template names the repo offers; TemplateBody is the
	// chosen one's content ("" when the repo has none).
	Templates    []string `json:"templates,omitempty"`
	TemplateName string   `json:"template_name,omitempty"`
	TemplateBody string   `json:"template_body,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// PlanInput is everything Plan needs.
type PlanInput struct {
	Item     domain.WorkItem
	Manifest *config.Manifest
	Choices  Choices
	Me       forge.User
	Root     string // paths.root, for worktree locations
	Found    map[string]Found
	// ChildBody is the content of conventions.child_body (see LoadChildBody);
	// empty uses the default body.
	ChildBody string
}

// StepKind names a step.
type StepKind string

const (
	StepChild     StepKind = "child"
	StepWorkflow  StepKind = "workflow"
	StepLink      StepKind = "link"
	StepIteration StepKind = "iteration"
	StepBranch    StepKind = "branch"
	StepMR        StepKind = "mr"
	StepWorktree  StepKind = "worktree"
	StepWorkspace StepKind = "workspace"
)

// Step is one line of the confirm screen.
type Step struct {
	Kind StepKind `json:"kind"`
	Repo string   `json:"repo,omitempty"`
	Text string   `json:"text"`
	// Path is the worktree directory of a worktree step, for surfaces that
	// shorten it from the left instead of cutting Text.
	Path string `json:"path,omitempty"`
	// Done marks a step whose result already exists; Execute skips it.
	Done bool `json:"done,omitempty"`
	// Write marks a forge write (as opposed to local git).
	Write bool `json:"write,omitempty"`
}

// ChildSpec is the child issue Execute creates when none exists.
type ChildSpec struct {
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Labels      []string `json:"labels,omitempty"`
	AssigneeIDs []int64  `json:"assignee_ids,omitempty"`
	MilestoneID int64    `json:"milestone_id,omitempty"`
	Weight      *int     `json:"weight,omitempty"`
	// Iteration is copied with issueSetIteration after the create (Premium).
	Iteration *forge.Iteration `json:"iteration,omitempty"`
}

// MRSpec describes the MR of one target.
type MRSpec struct {
	Title        string        `json:"title"`
	Draft        bool          `json:"draft"`
	Target       string        `json:"target"`
	Template     string        `json:"template,omitempty"`
	Actions      []QuickAction `json:"actions,omitempty"` // quick actions of the template
	Stripped     []string      `json:"stripped,omitempty"`
	closes       string        // conventions.mr.closes
	mention      string        // conventions.mr.mention
	templateBody string
	override     string
}

// WorkflowMove is the tracker label change of the move step.
type WorkflowMove struct {
	Ref    forge.Ref `json:"ref"`
	Add    string    `json:"add"`
	Remove []string  `json:"remove,omitempty"`
}

// TargetPlan is the plan for one repo.
type TargetPlan struct {
	Target Target `json:"target"`
	// UseChild says whether a child issue carries the work; without one the
	// branch and MR reference the tracker issue directly.
	UseChild bool `json:"use_child"`
	// Branch is the branch name; with a child still to be created its number
	// is unknown and shows as ChildIIDPlaceholder.
	Branch string `json:"branch,omitempty"`
	// Pinned says Choices.Branches named the branch.
	Pinned bool      `json:"pinned,omitempty"`
	Base   string    `json:"base,omitempty"` // branch cut from
	Child  ChildSpec `json:"child"`
	MR     MRSpec    `json:"mr"`
	Found  Found     `json:"found"`
}

// PinnedNote words the branch Choices.Branches chose for this repo, existing
// or to be created.
func (t TargetPlan) PinnedNote() string {
	if t.Found.Branch != "" {
		return fmt.Sprintf("branch for %s: %s (existing)", t.Target.Name, t.Branch)
	}
	return fmt.Sprintf("branch for %s: %s (new, from %s)", t.Target.Name, t.Branch, t.Base)
}

// MRBody renders the MR description for this target. child is the child issue
// (zero when none exists yet: the body shows ChildPlaceholder); tracker is the
// tracker issue. An edited body replaces the rendering.
func (t TargetPlan) MRBody(child, tracker forge.Ref) string {
	closesChild := ChildPlaceholder
	if !child.IsZero() {
		closesChild = refIn(t.Target.Project, child)
	}
	if t.MR.override != "" {
		return strings.ReplaceAll(t.MR.override, ChildPlaceholder, closesChild)
	}
	var head []string
	parent := refIn(t.Target.Project, tracker)
	var closes string
	switch {
	case t.MR.closes == "parent" || !t.UseChild:
		closes = parent
	case t.MR.closes == "child" || t.MR.closes == "":
		closes = closesChild
	}
	if closes != "" && t.MR.closes != "none" {
		head = append(head, "Closes "+closes)
	}
	// "Part of" names the tracker even when it is also the closed issue, so
	// the tracker issue always shows the MR as a reference.
	if t.MR.mention != "none" && (t.MR.mention == "parent" || t.MR.mention == "") && closes != parent {
		head = append(head, "Part of "+tracker.String())
	}
	body := strings.Join(head, "\n\n")
	if tb := strings.TrimSpace(StripQuickActions(t.MR.templateBody, t.MR.Stripped)); tb != "" {
		if body != "" {
			body += "\n\n"
		}
		body += tb
	}
	return body + "\n"
}

// Plan is the ordered list of writes.
type Plan struct {
	Item     forge.Ref     `json:"item"`
	Title    string        `json:"title"`
	Project  string        `json:"project"` // ADE project (manifest name)
	Mode     Mode          `json:"mode"`
	Targets  []TargetPlan  `json:"targets"`
	Workflow *WorkflowMove `json:"workflow,omitempty"`
	// WorkflowNote says why the workflow label is not written (sync is ask or
	// off, --no-move-label, or the item is further along), so a dry run
	// accounts for the label; empty when the move is a step or nothing maps.
	WorkflowNote string   `json:"workflow_note,omitempty"`
	Steps        []Step   `json:"steps"`
	Warnings     []string `json:"warnings,omitempty"`

	in PlanInput
}

// Pending counts the steps that would change something. Opening the
// workspace is not counted: the host reuses a tab that is open already.
func (p Plan) Pending() int {
	n := 0
	for _, s := range p.Steps {
		if !s.Done && s.Kind != StepWorkspace {
			n++
		}
	}
	return n
}

// Writes counts the pending forge writes: zero for Browse.
func (p Plan) Writes() int {
	n := 0
	for _, s := range p.Steps {
		if !s.Done && s.Write {
			n++
		}
	}
	return n
}

// BuildPlan derives the steps. It never touches the forge or the disk.
func BuildPlan(in PlanInput) (Plan, error) {
	m := in.Manifest
	if m == nil {
		return Plan{}, errors.New("startwork: no manifest")
	}
	if in.Choices.OnlyChild {
		in.Choices.Child, in.Choices.Mode = ChildAlways, ModeBranch
	}
	ch := in.Choices
	if _, err := ParseMode(string(ch.Mode)); err != nil {
		return Plan{}, err
	}
	if len(ch.Repos) == 0 {
		return Plan{}, errors.New("no repo chosen: the issue has no app label that routes to a repo; pick one with --repo")
	}
	p := Plan{Item: in.Item.Ref, Title: in.Item.Issue.Title, Project: m.Name, Mode: ch.Mode, in: in}
	for i, name := range ch.Repos {
		if slices.Contains(ch.Repos[:i], name) {
			continue
		}
		t, err := resolveTarget(m, name)
		if err != nil {
			return Plan{}, err
		}
		tp := planTarget(in, t)
		p.Targets = append(p.Targets, tp)
		p.Warnings = append(p.Warnings, tp.Found.Warnings...)
	}
	if ch.OnlyChild {
		return onlyChild(p), nil
	}
	p.Workflow, p.WorkflowNote = planWorkflow(in)
	if ch.OnlyMR {
		return onlyMR(p)
	}
	p.Steps = planSteps(&p)
	return p, nil
}

// onlyMR narrows p to the MR steps. The branch (and the child, when one
// carries the work) must exist already: `M` finishes what Branch only left.
func onlyMR(p Plan) (Plan, error) {
	if p.Mode == ModeBrowse {
		return Plan{}, errors.New("browse mode creates no MR")
	}
	p.Workflow, p.WorkflowNote = nil, ""
	for _, tp := range p.Targets {
		switch {
		case tp.Found.Branch == "":
			return Plan{}, fmt.Errorf("%s has no branch for %s yet: run `ade start` first", tp.Target.Name, p.Item)
		case tp.UseChild && tp.Found.Child == nil:
			return Plan{}, fmt.Errorf("%s has no child issue for %s yet: run `ade start` first", tp.Target.Name, p.Item)
		}
		p.Steps = append(p.Steps, mrStep(tp))
	}
	return p, nil
}

// onlyChild narrows p to the steps that make the child issue and tie it to the
// tracker issue.
func onlyChild(p Plan) Plan {
	for _, tp := range p.Targets {
		p.Steps = append(p.Steps, childSteps(p.in, tp)...)
	}
	return p
}

func resolveTarget(m *config.Manifest, name string) (Target, error) {
	if r, ok := m.Repos[name]; ok {
		return Target{Name: name, Project: r.Path}, nil
	}
	if name == TrackerRepo && m.Tracker.Project != "" {
		return Target{Name: name, Project: m.Tracker.Project, Tracker: true}, nil
	}
	return Target{}, fmt.Errorf("unknown repo %q (have: %s)", name, strings.Join(m.RepoNames(), ", "))
}

// useChild decides whether a child issue carries the work in target t: on for a
// repo other than the tracker project, off when the issue already lives in the
// code repo (conventions.child_issue), and never for Browse, which writes nothing.
func useChild(m *config.Manifest, ch Choices, t Target) bool {
	switch {
	case ch.Mode == ModeBrowse, ch.Child == ChildNever:
		return false
	case ch.Child == ChildAlways:
		return true
	}
	return t.Project != m.Tracker.Project
}

func planTarget(in PlanInput, t Target) TargetPlan {
	m, ch, item := in.Manifest, in.Choices, in.Item
	f := in.Found[t.Name]
	tp := TargetPlan{Target: t, Found: f, Base: f.DefaultBranch}
	if tp.Base == "" {
		tp.Base = m.Repos[t.Name].DefaultBranch
	}
	if tp.Base == "" {
		tp.Base = "main"
	}
	tp.UseChild = useChild(m, ch, t)
	tp.Child = childSpec(in, t)

	// The work issue number the branch is named after.
	workIID := item.Ref.IID
	if tp.UseChild {
		workIID = 0
		if f.Child != nil {
			workIID = f.Child.Ref.IID
		}
	}
	pin := ch.Branches[t.Name]
	if ch.Mode == ModeBrowse {
		pin = "" // Browse stays on the default branch
	}
	switch {
	case f.Branch != "":
		tp.Branch = f.Branch
		tp.Pinned = pin != ""
	case pin != "":
		tp.Branch, tp.Pinned = pin, true
	default:
		tp.Branch = BranchName(m, tp.Child.Title, item, workIID)
	}

	conv := m.Conventions.MR
	tp.MR = MRSpec{
		Title: tp.Child.Title, Draft: conv.DraftMR(), Target: tp.Base, Template: f.TemplateName,
		closes: conv.Closes, mention: conv.Mention, templateBody: f.TemplateBody, override: ch.MRBody,
		Stripped: ch.StripActions,
	}
	if f.TemplateBody != "" {
		tp.MR.Actions = ParseQuickActions(f.TemplateBody)
	}
	return tp
}

// childSpec builds the child issue the manifest describes for target t.
func childSpec(in PlanInput, t Target) ChildSpec {
	m, item := in.Manifest, in.Item
	tr := item.Issue
	vars := map[string]string{
		"parent_title": tr.Title, "parent_iid": strconv.Itoa(tr.Ref.IID), "type": item.Type,
		"parent_ref": tr.Ref.String(), "parent_url": tr.WebURL,
	}
	title := tr.Title
	if m.Conventions.ChildTitle != "" {
		title = strings.TrimSpace(expand(m.Conventions.ChildTitle, vars))
	}
	if title == "" {
		title = tr.Title
	}
	spec := ChildSpec{Title: title, Body: childBody(in.ChildBody, vars), Iteration: tr.Iteration, Weight: tr.Weight}
	if l := facetLabel(m.Labels.Type.Pattern, item.Type); l != "" {
		spec.Labels = []string{l}
	}
	for _, a := range tr.Assignees {
		spec.AssigneeIDs = append(spec.AssigneeIDs, a.ID)
	}
	if len(spec.AssigneeIDs) == 0 && in.Me.ID != 0 {
		spec.AssigneeIDs = []int64{in.Me.ID} // whoever starts the work owns it
	}
	if tr.Milestone != nil {
		spec.MilestoneID = tr.Milestone.ID
	}
	return spec
}

// childBody renders the child's description: the manifest's body template when
// present, always with the "Parent:" reference line that makes the child findable
// again (reading accepts it, and idempotency depends on it).
func childBody(tmpl string, vars map[string]string) string {
	body := "Implementation issue for " + vars["parent_ref"] + "."
	if tmpl != "" {
		body = strings.TrimSpace(expand(tmpl, vars))
	}
	parent := "Parent: " + vars["parent_ref"]
	if !strings.Contains(body, parent) {
		body = parent + "\n\n" + body
	}
	return body + "\n"
}

// BranchName renders conventions.branch. workIID is the issue the branch is
// named after: the child, or the tracker issue when there is no child. Zero
// means a child that does not exist yet and renders as ChildIIDPlaceholder.
func BranchName(m *config.Manifest, title string, item domain.WorkItem, workIID int) string {
	tmpl := m.Conventions.Branch
	if tmpl == "" {
		tmpl = "{child_iid}-{slug}"
	}
	iid := ChildIIDPlaceholder
	if workIID > 0 {
		iid = strconv.Itoa(workIID)
	}
	return expand(tmpl, map[string]string{
		"child_iid": iid, "parent_iid": strconv.Itoa(item.Ref.IID), "slug": Slug(title, 50), "type": item.Type,
	})
}

// MoveLabelHint ends the note of a label move left alone by sync = ask. It
// words the CLI's way to confirm; the dialog swaps it for its own key.
const MoveLabelHint = "pass --move-label to apply"

// planWorkflow decides the tracker label move, or nil when labels.workflow
// maps nothing for `started`, sync is off, or the label is further along. The
// note says why a mapped label is not written: it is set for nil and for a
// move that will not run (sync = ask without MoveLabel, or NoMoveLabel).
func planWorkflow(in PlanInput) (*WorkflowMove, string) {
	m, item := in.Manifest, in.Item
	wf := m.Labels.Workflow
	if in.Choices.Mode == ModeBrowse || wf.Pattern == "" {
		return nil, ""
	}
	want := domain.WantWorkflow(domain.StageStarted, m.Labels)
	if want == "" {
		return nil, ""
	}
	if wf.SyncMode() == "off" {
		return nil, "workflow label: not moved (sync = off)"
	}
	wantLabel := domain.WorkflowLabel(want, m.Labels)
	var remove []string
	for _, l := range item.Issue.Labels {
		if v, ok := facetValue(wf.Pattern, l); ok && !strings.EqualFold(v, want) {
			remove = append(remove, l)
		}
	}
	has := slices.ContainsFunc(item.Issue.Labels, func(l string) bool { return strings.EqualFold(l, wantLabel) })
	// Edge-triggered and never backwards: when the current workflow value is
	// further along than "in progress" (or is the done value), the item moves
	// by derived stage later, not by this step.
	if cur := item.Workflow; cur != "" && !strings.EqualFold(cur, want) &&
		domain.FacetsOf([]string{wantLabel, domain.WorkflowLabel(cur, m.Labels)}, m.Labels).Workflow == cur {
		return nil, fmt.Sprintf("workflow label: not moved (%s is further along than %s)", item.Ref, wantLabel)
	}
	if has && len(remove) == 0 {
		return &WorkflowMove{Ref: item.Ref, Add: wantLabel}, "" // already there: reported as done
	}
	mv := &WorkflowMove{Ref: item.Ref, Remove: remove}
	if !has {
		mv.Add = wantLabel
	}
	switch {
	case wfWanted(in, mv):
		return mv, ""
	case in.Choices.NoMoveLabel:
		return mv, fmt.Sprintf("workflow label: would %s (--no-move-label: not applied)", workflowText(mv))
	default:
		return mv, fmt.Sprintf("workflow label: would %s (sync = ask: %s)", workflowText(mv), MoveLabelHint)
	}
}

// facetValue returns the value a label carries under pattern.
func facetValue(pattern, label string) (string, bool) {
	for _, ph := range []string{"{value}", "{n}"} {
		before, after, ok := strings.Cut(pattern, ph)
		if !ok {
			continue
		}
		if len(label) > len(before)+len(after) && strings.EqualFold(label[:len(before)], before) &&
			strings.EqualFold(label[len(label)-len(after):], after) {
			return label[len(before) : len(label)-len(after)], true
		}
		return "", false
	}
	return "", false
}

// workflowDone reports the workflow move needs no write.
func workflowDone(in PlanInput, mv *WorkflowMove) bool {
	return len(mv.Remove) == 0 && (mv.Add == "" || slices.ContainsFunc(in.Item.Issue.Labels, func(l string) bool {
		return strings.EqualFold(l, mv.Add)
	}))
}

func planSteps(p *Plan) []Step {
	in := p.in
	ch := in.Choices
	var steps []Step
	add := func(s Step) { steps = append(steps, s) }
	for i, tp := range p.Targets {
		t, f := tp.Target, tp.Found
		repo := t.Name
		if ch.Mode == ModeBrowse {
			add(worktreeStep(in, tp, f.Worktree != ""))
			continue
		}
		childAt := len(steps)
		for _, st := range childSteps(in, tp) {
			add(st)
		}
		if i == 0 && p.Workflow != nil && (workflowDone(in, p.Workflow) || wfWanted(in, p.Workflow)) {
			// The label moves right after the child, before the link.
			steps = slices.Insert(steps, min(childAt+1, len(steps)), workflowStep(in, p.Workflow))
		}
		add(Step{
			Kind: StepBranch, Repo: repo, Done: f.Branch != "", Write: true,
			Text: branchText(tp, f.Branch != ""),
		})
		if ch.Mode == ModeDraft {
			add(mrStep(tp))
		}
		add(worktreeStep(in, tp, f.Worktree != ""))
	}
	// The tab opens last, on the first repo's worktree, so it knows the item's
	// worktrees in every repo (the services of one repo run from the item's tree of another).
	if len(p.Targets) > 0 {
		repo := p.Targets[0].Target.Name
		add(Step{Kind: StepWorkspace, Repo: repo, Text: "open workspace for " + repo})
	}
	return steps
}

// childSteps are the steps that create the child issue in a target and tie it
// to the tracker issue: the issue, the link, the iteration.
func childSteps(in PlanInput, tp TargetPlan) []Step {
	t, f, repo := tp.Target, tp.Found, tp.Target.Name
	if !tp.UseChild {
		return nil
	}
	var steps []Step
	if f.Child != nil {
		steps = append(steps, Step{Kind: StepChild, Repo: repo, Done: true, Text: fmt.Sprintf("child issue %s exists", f.Child.Ref)})
	} else {
		steps = append(steps, Step{Kind: StepChild, Repo: repo, Write: true, Text: fmt.Sprintf("create child issue in %s: %s", t.Project, tp.Child.Title)})
	}
	if contains(in.Manifest.Conventions.Link, "relates_to", true) {
		steps = append(steps, Step{
			Kind: StepLink, Repo: repo, Done: f.Child != nil && f.Linked, Write: true,
			Text: fmt.Sprintf("link %s relates_to the child (and Parent: line)", in.Item.Ref),
		})
	}
	if tp.Child.Iteration != nil {
		done := f.Child != nil && f.Child.Iteration != nil && f.Child.Iteration.ID == tp.Child.Iteration.ID
		steps = append(steps, Step{
			Kind: StepIteration, Repo: repo, Done: done, Write: true,
			Text: "copy iteration " + iterationName(*tp.Child.Iteration) + " to the child",
		})
	}
	return steps
}

// wfWanted reports whether the move runs: with sync = ask only when the user
// confirmed it, otherwise unless they declined.
func wfWanted(in PlanInput, mv *WorkflowMove) bool {
	if in.Choices.NoMoveLabel {
		return false
	}
	return in.Manifest.Labels.Workflow.SyncMode() != "ask" || in.Choices.MoveLabel
}

// workflowText is what the move does, e.g. "set workflow::in progress on g/t#3".
func workflowText(mv *WorkflowMove) string {
	if mv.Add == "" {
		return fmt.Sprintf("remove %s from %s", strings.Join(mv.Remove, ", "), mv.Ref)
	}
	text := fmt.Sprintf("set %s on %s", mv.Add, mv.Ref)
	if len(mv.Remove) > 0 {
		text += ", remove " + strings.Join(mv.Remove, ", ")
	}
	return text
}

func workflowStep(in PlanInput, mv *WorkflowMove) Step {
	text := workflowText(mv)
	if workflowDone(in, mv) {
		text = fmt.Sprintf("%s already carries %s", mv.Ref, mv.Add)
	}
	return Step{Kind: StepWorkflow, Done: workflowDone(in, mv), Write: true, Text: text}
}

func branchText(tp TargetPlan, exists bool) string {
	if exists {
		return fmt.Sprintf("branch %s exists", tp.Branch)
	}
	return fmt.Sprintf("create branch %s from %s", tp.Branch, tp.Base)
}

func mrStep(tp TargetPlan) Step {
	f := tp.Found
	if f.MR != nil {
		return Step{Kind: StepMR, Repo: tp.Target.Name, Done: true, Text: fmt.Sprintf("MR %s exists", f.MR.Ref)}
	}
	kind := "MR"
	if tp.MR.Draft {
		kind = "draft MR"
	}
	text := fmt.Sprintf("open %s %s → %s", kind, tp.Branch, tp.MR.Target)
	if tp.MR.Template != "" {
		text += " from template " + tp.MR.Template
	}
	return Step{Kind: StepMR, Repo: tp.Target.Name, Write: true, Text: text}
}

func worktreeStep(in PlanInput, tp TargetPlan, exists bool) Step {
	if tp.Target.Tracker {
		return Step{Kind: StepWorktree, Repo: tp.Target.Name, Done: true, Text: "tracker project: no local tree"}
	}
	branch := tp.Branch
	if in.Choices.Mode == ModeBrowse {
		branch = tp.Base
	}
	dir := filepath.Join(in.Root, in.Manifest.Name, tp.Target.Name, git.BranchDir(branch))
	if exists {
		return Step{Kind: StepWorktree, Repo: tp.Target.Name, Done: true, Path: dir, Text: "worktree " + dir + " exists"}
	}
	return Step{Kind: StepWorktree, Repo: tp.Target.Name, Path: dir, Text: "fetch, add worktree " + dir + " tracking origin/" + branch}
}

func iterationName(it forge.Iteration) string {
	if it.Title != "" {
		return it.Title
	}
	return it.Start.Format("2 Jan") + "–" + it.Due.Format("2 Jan")
}

// contains reports whether list has want; an empty list means the default
// (relates_to plus description), which is dflt.
func contains(list []string, want string, dflt bool) bool {
	if len(list) == 0 {
		return dflt
	}
	return slices.Contains(list, want)
}
