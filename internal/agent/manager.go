package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitmsg"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/git"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/review"
	"github.com/coredev-uk/ade/internal/workspace"
)

// ErrCancelled is returned when a run was cancelled (from this or another
// process) while a call was in flight.
var ErrCancelled = errors.New("run cancelled")

// Manager drives runs through their state machine.
type Manager struct {
	Cfg   *config.Config
	Store *Store
	Audit *audit.Log
	Bus   *events.Bus // optional
	// AI supplies the pre-flight (period caps, the price estimate); optional.
	AI *ai.Service
	// Reviews receives an address run's drafted replies; optional.
	Reviews *review.Store
	// GitFor returns the git client authorised for the run's remote.
	GitFor func(ctx context.Context, r Run) (git.Client, error)
	// Open builds the agent driver; tests swap it. Default ForProfile.
	Open func(p config.AIProfile, override string) (Driver, error)
	Now  func() time.Time
}

// NewManager wires a Manager with production defaults.
func NewManager(cfg *config.Config, st *Store, al *audit.Log, bus *events.Bus) *Manager {
	return &Manager{Cfg: cfg, Store: st, Audit: al, Bus: bus, Open: ForProfile, Now: time.Now}
}

// Request starts a run.
type Request struct {
	Kind    Kind
	Target  Target
	Context Context
	Profile string // empty: the active profile
	Driver  string // empty: the profile's agent
	// MaxUSD lowers the cap below budget.agent_run_usd; 0 keeps it.
	MaxUSD float64
	// Assume skips the pre-flight confirmation (--yes).
	Assume bool
	// DryRun stops after the pre-flight: nothing is created or spent.
	DryRun  bool
	Confirm func(ai.Preflight) (bool, error)
}

func (m *Manager) role(k Kind) ai.Role {
	if k == KindPlan {
		return ai.RolePlan
	}
	return ai.RoleImplement
}

// New checks everything that can be checked before spending (the worktree,
// the driver, the price, the caps) and creates the run, queued. Nothing has
// been sent to a model yet.
func (m *Manager) New(ctx context.Context, req Request) (Run, ai.Preflight, error) {
	var pf ai.Preflight
	if m.Cfg.AI.Mode == "off" {
		return Run{}, pf, ai.ErrDisabled
	}
	name, p, err := m.Cfg.Profile(req.Profile)
	if err != nil {
		return Run{}, pf, err
	}
	role := m.role(req.Kind)
	model, err := p.Model(string(role))
	if err != nil {
		return Run{}, pf, fmt.Errorf("profile %s: %w", name, err)
	}
	drv, err := m.Open(p, req.Driver)
	if err != nil {
		return Run{}, pf, err
	}
	if err := Available(drv); err != nil {
		return Run{}, pf, fmt.Errorf("agent driver unavailable: %w", err)
	}
	t := req.Target
	if err := checkWorktree(ctx, t.Worktree, t.Branch); err != nil {
		return Run{}, pf, err
	}
	capUSD := m.capFor(req.MaxUSD)
	price := priceOf(p, model)
	if drv.NeedsPrice() && capUSD > 0 && price == nil {
		return Run{}, pf, fmt.Errorf("%s reports tokens, not cost, and no price is known for %q: add [ai.profiles.%s.prices.%q] so the run cap can be enforced", drv.Name(), model, name, model)
	}
	var prompt string
	switch req.Kind {
	case KindPlan:
		prompt = PlanPrompt(m.withCommitPrefix(ctx, t), req.Context)
	case KindAddress:
		if t.MR == nil {
			return Run{}, pf, errors.New("address comments needs the item's merge request: start work creates it")
		}
		if len(req.Context.Threads) == 0 {
			return Run{}, pf, fmt.Errorf("%s has no unresolved threads", t.MR)
		}
		prompt = AddressPrompt(m.withCommitPrefix(ctx, t), req.Context)
	default:
		return Run{}, pf, fmt.Errorf("unknown run kind %q", req.Kind)
	}

	feature := "agent-" + string(req.Kind)
	if m.AI != nil {
		pf, _, err = m.AI.Preview(ctx, ai.Job{Feature: feature, Role: role, Profile: req.Profile, Prompt: prompt, Forge: t.Forge, Ref: t.Tracker, Notes: req.Context.Notes != ""})
		if err != nil {
			return Run{}, pf, err
		}
	}
	if pf.Refuse == "" && capUSD > 0 && pf.Priced && pf.EstimateUSD > capUSD {
		pf.Refuse = fmt.Sprintf("the first call alone is estimated at $%.4f, over the run cap of $%.4f", pf.EstimateUSD, capUSD)
	}
	if pf.Refuse != "" {
		stop := &ai.StopError{Source: "local", Message: pf.Refuse}
		m.recordStop(t.Tracker.String(), feature, name, model, stop)
		return Run{}, pf, stop
	}
	if len(pf.Confirm) > 0 && !req.Assume {
		if req.Confirm == nil {
			return Run{}, pf, &ai.ErrNeedsConfirm{Preflight: pf}
		}
		ok, err := req.Confirm(pf)
		if err != nil {
			return Run{}, pf, err
		}
		if !ok {
			return Run{}, pf, ai.ErrCancelled
		}
	}

	if req.DryRun {
		return Run{}, pf, nil
	}
	now := m.Now()
	r := Run{
		ID: newID(now, rand.Uint32()), Kind: req.Kind, State: StateQueued,
		Forge: t.Forge, Project: t.Project, Tracker: t.Tracker, Child: t.Child, Repo: t.Repo,
		Worktree: t.Worktree, Branch: t.Branch, MR: t.MR, AddDirs: t.AddDirs,
		Profile: name, Driver: drv.Name(), Model: model, CapUSD: capUSD,
		Created: now, Updated: now,
	}
	if err := m.Store.Save(r); err != nil {
		return Run{}, pf, err
	}
	if err := req.Context.write(m.Store.Dir(r)); err != nil {
		return Run{}, pf, err
	}
	m.emit(r, "queued", events.Info, true, "", "")
	return r, pf, nil
}

// capFor is the cap of a new run: budget.agent_run_usd, lowered by a flag.
func (m *Manager) capFor(flag float64) float64 {
	c := m.Cfg.Budget.AgentRunUSD
	if flag > 0 && (c <= 0 || flag < c) {
		c = flag
	}
	return c
}

func priceOf(p config.AIProfile, model string) *config.Price {
	if pr, ok := p.Prices[model]; ok {
		return &pr
	}
	if _, bare, ok := strings.Cut(model, "/"); ok {
		if pr, ok := p.Prices[bare]; ok {
			return &pr
		}
	}
	for k, pr := range p.Prices {
		if strings.HasSuffix(k, "/"+model) {
			return &pr
		}
	}
	return nil
}

// target rebuilds the Target from a stored run.
func (r Run) target() Target {
	return Target{Forge: r.Forge, Project: r.Project, Tracker: r.Tracker, Child: r.Child, Repo: r.Repo, Worktree: r.Worktree, Branch: r.Branch, MR: r.MR, AddDirs: r.AddDirs}
}

// targetOf is the run's Target with the commit prefix for its prompt.
func (m *Manager) targetOf(ctx context.Context, r Run) Target {
	return m.withCommitPrefix(ctx, r.target())
}

// withCommitPrefix sets t.CommitPrefix the way a commit will get it.
func (m *Manager) withCommitPrefix(ctx context.Context, t Target) Target {
	t.CommitPrefix, t.PrefixKnown = m.commitPrefix(ctx, Run{Project: t.Project, Tracker: t.Tracker, Child: t.Child, Worktree: t.Worktree})
	return t
}

// Plan runs the read-only planning call: queued (or awaiting approval, for a
// re-plan) → planning → awaiting-approval. The plan lands in plan.md.
func (m *Manager) Plan(ctx context.Context, id string) (Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.Kind != KindPlan {
		return r, fmt.Errorf("run %s is an %s run: it has no plan", r.ID, r.Kind)
	}
	if err := m.begin(&r, StatePlanning); err != nil {
		return r, err
	}
	c, err := readContext(m.Store.Dir(r))
	if err != nil {
		return m.fail(r, fmt.Errorf("read context: %w", err))
	}
	res, err := m.call(ctx, &r, KindPlan, PlanPrompt(m.targetOf(ctx, r), c))
	if err != nil {
		return m.settle(r, err)
	}
	if err := m.outcome(res); err != nil {
		return m.settle(r, err)
	}
	if res.Progress.Text == "" {
		return m.fail(r, errors.New("the agent returned no plan"))
	}
	if err := writeFile(filepath.Join(m.Store.Dir(r), FilePlan), []byte(res.Progress.Text+"\n")); err != nil {
		return m.fail(r, err)
	}
	r.Approved, r.PlanSHA, r.Error = false, "", ""
	if err := m.move(&r, StateAwaiting); err != nil {
		return r, err
	}
	return r, nil
}

// Plan returns the run's plan text.
func (m *Manager) PlanText(r Run) (string, error) {
	b, err := os.ReadFile(filepath.Join(m.Store.Dir(r), FilePlan))
	if err != nil {
		return "", fmt.Errorf("run %s has no plan yet", r.ID)
	}
	return string(b), nil
}

// Approve records the user's yes to the plan as it is on disk now: editing
// plan.md first is allowed, editing it after approval is not.
func (m *Manager) Approve(id string) (Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.State != StateAwaiting {
		return r, fmt.Errorf("run %s is %s: only a plan awaiting approval can be approved", r.ID, r.State)
	}
	b, err := os.ReadFile(filepath.Join(m.Store.Dir(r), FilePlan))
	if err != nil {
		return r, fmt.Errorf("run %s has no plan: plan it first", r.ID)
	}
	r.Approved, r.ApprovedAt, r.PlanSHA, r.Updated = true, m.Now(), sha(b), m.Now()
	if err := m.Store.Save(r); err != nil {
		return r, err
	}
	m.audit(audit.Entry{
		Action: "agent.plan.approve", Actor: "user", Target: r.Item(), Item: r.Item(),
		Params: map[string]any{"run": r.ID, "plan_sha": r.PlanSHA},
	})
	m.emit(r, "approved", events.Info, false, "", "")
	return r, nil
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:8]) }

// Implement executes the approved plan in the worktree: awaiting-approval →
// running → review. The tree must be clean, so the diff is the agent's alone.
func (m *Manager) Implement(ctx context.Context, id string) (Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return r, err
	}
	switch {
	case r.Kind != KindPlan:
		return r, fmt.Errorf("run %s is an %s run: use address", r.ID, r.Kind)
	case r.State != StateAwaiting || !r.Approved:
		return r, fmt.Errorf("run %s is %s%s: approve the plan first (ade agent approve %s)", r.ID, r.State, map[bool]string{false: ", not approved"}[r.Approved], r.ID)
	}
	b, err := os.ReadFile(filepath.Join(m.Store.Dir(r), FilePlan))
	if err != nil || sha(b) != r.PlanSHA {
		return r, fmt.Errorf("plan.md changed after approval: approve run %s again", r.ID)
	}
	if err := m.prepareWrite(ctx, &r); err != nil {
		return r, err
	}
	if err := m.begin(&r, StateRunning); err != nil {
		return r, err
	}
	c, err := readContext(m.Store.Dir(r))
	if err != nil {
		return m.fail(r, fmt.Errorf("read context: %w", err))
	}
	res, err := m.call(ctx, &r, KindRun, RunPrompt(m.targetOf(ctx, r), c, string(b)))
	if err != nil {
		return m.settle(r, err)
	}
	if err := m.outcome(res); err != nil {
		return m.settle(r, err)
	}
	return m.toReview(ctx, r, true)
}

// Address takes the MR's unresolved threads (from the run's context), lets
// the agent change code, and drafts a reply for each into the review drafts:
// pending, never posted without triage and submit.
func (m *Manager) Address(ctx context.Context, id string) (Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.Kind != KindAddress {
		return r, fmt.Errorf("run %s is a %s run: use plan, approve and run", r.ID, r.Kind)
	}
	if err := m.prepareWrite(ctx, &r); err != nil {
		return r, err
	}
	if err := m.begin(&r, StateRunning); err != nil {
		return r, err
	}
	c, err := readContext(m.Store.Dir(r))
	if err != nil {
		return m.fail(r, fmt.Errorf("read context: %w", err))
	}
	res, err := m.call(ctx, &r, KindAddress, AddressPrompt(m.targetOf(ctx, r), c))
	if err != nil {
		return m.settle(r, err)
	}
	if err := m.outcome(res); err != nil {
		return m.settle(r, err)
	}
	n, err := m.draftReplies(&r, c, res.Progress.Text)
	if err != nil {
		return m.fail(r, err)
	}
	r.Drafts = n
	return m.toReview(ctx, r, n == 0)
}

// prepareWrite checks the tree is a start-work worktree and clean, and
// records where the agent started from.
func (m *Manager) prepareWrite(ctx context.Context, r *Run) error {
	if err := checkWorktree(ctx, r.Worktree, r.Branch); err != nil {
		return err
	}
	if r.BaseSHA != "" {
		return nil // a further pass: the first pass's changes are the run's own
	}
	out, err := gitIn(ctx, r.Worktree, nil, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("worktree %s has uncommitted changes: commit or stash them first, so the diff is only the agent's", r.Worktree)
	}
	if r.BaseSHA, err = head(ctx, r.Worktree); err != nil {
		return err
	}
	return nil
}

// toReview writes the diff summary and moves running → review. A run that
// changed nothing fails when requireChange is set.
func (m *Manager) toReview(ctx context.Context, r Run, requireChange bool) (Run, error) {
	d, err := worktreeDiff(ctx, r.Worktree, r.BaseSHA, false)
	if err != nil {
		return m.fail(r, fmt.Errorf("diff: %w", err))
	}
	if err := writeFile(filepath.Join(m.Store.Dir(r), FileDiff), []byte(d.markdown())); err != nil {
		return m.fail(r, err)
	}
	if d.Empty() && requireChange {
		return m.fail(r, errNoChanges)
	}
	r.Error = ""
	if err := m.move(&r, StateReview); err != nil {
		return r, err
	}
	return r, nil
}

// draftReplies turns the agent's reply block into pending agent drafts.
func (m *Manager) draftReplies(r *Run, c Context, text string) (int, error) {
	rs, err := ParseReplies(text)
	if err != nil {
		return 0, err
	}
	if len(rs) == 0 || m.Reviews == nil || r.MR == nil {
		return 0, nil
	}
	byID := map[string]Thread{}
	for _, th := range c.Threads {
		byID[th.ID] = th
	}
	n := 0
	for _, rp := range rs {
		th, ok := byID[rp.Thread]
		if !ok {
			continue // a reply to a thread the agent invented
		}
		d := review.Draft{Body: rp.Body, Source: review.SourceAgent, State: review.StatePending}
		if pos := th.Position; pos != nil && pos.Path != "" {
			d.Path, d.OldPath = pos.Path, pos.OldPath
			switch {
			case pos.NewLine > 0:
				d.Line, d.Side = pos.NewLine, review.SideNew
			case pos.OldLine > 0:
				d.Line, d.Side = pos.OldLine, review.SideOld
			default:
				d.Path = ""
			}
		}
		if d.Path == "" {
			d.Body = "Re: " + firstLine(th.Notes[0].Body) + "\n\n" + d.Body
		}
		d, err := review.Normalize(d, m.Now())
		if err != nil {
			return n, err
		}
		if err := m.Reviews.Put(*r.MR, d); err != nil {
			return n, err
		}
		n++
	}
	m.audit(audit.Entry{
		Actor: "agent:" + r.ID, Action: "agent.drafts", Target: r.MR.String(), Item: r.Item(),
		Params: map[string]any{"run": r.ID, "drafts": n},
	})
	return n, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// Diff summarises what the agent changed; patch adds the full unified diff.
func (m *Manager) Diff(ctx context.Context, id string, patch bool) (DiffSummary, Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return DiffSummary{}, r, err
	}
	if r.BaseSHA == "" {
		return DiffSummary{}, r, fmt.Errorf("run %s has not changed anything yet (state %s)", r.ID, r.State)
	}
	d, err := worktreeDiff(ctx, r.Worktree, r.BaseSHA, patch)
	return d, r, err
}

// PushOpts are the user's words for the commit.
type PushOpts struct {
	Message string // empty: the issue title and the run id
}

// Push is the user's approval of the diff made real: commit with the
// synthetic author, push the branch, and the MR (which tracks the branch)
// updates by itself. review → pushed.
func (m *Manager) Push(ctx context.Context, id string, o PushOpts) (Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.State != StateReview {
		return r, fmt.Errorf("run %s is %s: only a run in review can be pushed", r.ID, r.State)
	}
	if err := checkWorktree(ctx, r.Worktree, r.Branch); err != nil {
		return r, err
	}
	d, err := worktreeDiff(ctx, r.Worktree, r.BaseSHA, false)
	if err != nil {
		return r, err
	}
	if d.Empty() {
		return r, errNoChanges
	}
	msg := o.Message
	if msg == "" {
		msg = m.commitMessage(r)
	}
	// Only the MR's first commit carries the prefix.
	if commitprefix.FirstCommit(ctx, r.Worktree) {
		prefix, _ := m.commitPrefix(ctx, r)
		msg = commitmsg.Apply(prefix, msg)
	}
	author := AuthorFromGit(ctx, r.Worktree)
	sha, err := commitAll(ctx, r.Worktree, author, msg)
	if err != nil {
		return r, m.pushFailed(r, "commit", err)
	}
	if sha != "" {
		r.Commits = append(r.Commits, sha)
		if err := m.Store.Save(r); err != nil {
			return r, err
		}
		m.audit(audit.Entry{
			Action: "git.commit", Target: r.Worktree, Item: r.Item(),
			Params: map[string]any{"run": r.ID, "sha": sha, "author": author.Name},
		})
	}
	gc := git.Client{}
	if m.GitFor != nil {
		if gc, err = m.GitFor(ctx, r); err != nil {
			return r, m.pushFailed(r, "credentials", err)
		}
	}
	perr := pushBranch(ctx, gc, r.Worktree, r.Branch)
	e := audit.Entry{Action: "git.push", Target: r.Worktree, Item: r.Item(), Params: map[string]any{"run": r.ID, "branch": r.Branch}}
	if perr != nil {
		e.Outcome, e.Error = "error", perr.Error()
	}
	m.audit(e)
	if perr != nil {
		return r, m.pushFailed(r, "push", perr)
	}
	r.Error = ""
	if err := m.move(&r, StatePushed); err != nil {
		return r, err
	}
	return r, nil
}

// commitPrefix is the commit prefix of the run's item: the worktree's git
// config first (written at start work and workspace open), else the project
// manifest's template expanded for the run's issues. known is false when that
// cannot be told (no readable manifest, an invalid template, or a {child}
// template without the child), so a prompt does not claim there is none.
func (m *Manager) commitPrefix(ctx context.Context, r Run) (prefix string, known bool) {
	if p := commitprefix.Read(ctx, r.Worktree); p != "" {
		return p, true
	}
	if m.Cfg == nil || r.Project == "" {
		return "", false
	}
	root, err := m.Cfg.RootDir()
	if err != nil {
		return "", false
	}
	mf, err := project.LoadManifest(filepath.Join(root, r.Project))
	if err != nil {
		return "", false
	}
	tmpl := mf.Conventions.CommitPrefix
	if tmpl == "" {
		return "", true
	}
	child := 0
	if r.Child != nil {
		child = r.Child.IID
	}
	if child == 0 && strings.Contains(tmpl, commitmsg.PlaceholderChild) {
		return "", false
	}
	prefix = commitprefix.ForIssues(mf, r.Tracker.IID, child)
	return prefix, prefix != ""
}

func (m *Manager) commitMessage(r Run) string {
	c, _ := readContext(m.Store.Dir(r))
	title := c.Issue.Title
	if title == "" {
		title = "Agent changes for " + r.Item()
	}
	if r.Kind == KindAddress {
		title = "Address review comments on " + r.MR.String()
	}
	return fmt.Sprintf("%s\n\nWritten by ade agent run %s (%s, %s) and reviewed before pushing.\n", title, r.ID, r.Driver, r.Model)
}

// pushFailed keeps the run in review (nothing is lost, push can be retried)
// and tells the user.
func (m *Manager) pushFailed(r Run, what string, err error) error {
	err = fmt.Errorf("%s: %w", what, err)
	r.Error, r.Updated = err.Error(), m.Now()
	_ = m.Store.Save(r)
	m.emit(r, "push-failed", events.Error, false, "agent push failed: "+err.Error(), err.Error())
	return err
}

// Cancel stops a run: its process is killed and it becomes cancelled. A run
// that is already finished stays as it is.
func (m *Manager) Cancel(id string) (Run, error) {
	r, err := m.Store.Get(id)
	if err != nil {
		return r, err
	}
	if r.State.Terminal() {
		return r, fmt.Errorf("run %s is already %s", r.ID, r.State)
	}
	// The state goes first: the process's owner checks it the moment the
	// kill lands, and must find the cancellation, not a failure.
	pid := r.PID
	r.PID = 0
	if err := m.move(&r, StateCancelled); err != nil {
		return r, err
	}
	if err := killGroup(pid); err != nil {
		return r, fmt.Errorf("stop pid %d: %w", pid, err)
	}
	return r, nil
}

// --- state and events ---------------------------------------------------------

func (m *Manager) begin(r *Run, to State) error {
	if err := r.move(to, m.Now()); err != nil {
		return err
	}
	r.Error = ""
	if err := m.Store.Save(*r); err != nil {
		return err
	}
	m.emit(*r, string(to), events.Info, true, "", "")
	return nil
}

// move transitions, saves and announces.
func (m *Manager) move(r *Run, to State) error {
	if err := r.move(to, m.Now()); err != nil {
		return err
	}
	if err := m.Store.Save(*r); err != nil {
		return err
	}
	switch to {
	case StateAwaiting:
		m.emit(*r, "awaiting-approval", events.Info, false, "", "")
	case StateReview:
		m.emit(*r, "review", events.Success, false, "", "")
	case StatePushed:
		m.emit(*r, "pushed", events.Success, false, "", "")
	case StateCancelled:
		m.emit(*r, "cancelled", events.Info, false, "", "")
	}
	return nil
}

// fail records the failure and raises the pop-up.
func (m *Manager) fail(r Run, err error) (Run, error) {
	r.PID = 0
	if r.State.Terminal() {
		return r, err
	}
	r.Error = err.Error()
	if mv := r.move(StateFailed, m.Now()); mv != nil {
		return r, err
	}
	_ = m.Store.Save(r)
	m.emit(r, "failed", events.Error, false, fmt.Sprintf("agent run %s failed: %s", r.ID, err), err.Error())
	return r, err
}

// settle ends a run after a call error: a cancelled run stays cancelled,
// anything else fails.
func (m *Manager) settle(r Run, err error) (Run, error) {
	if errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if cur, gerr := m.Store.Get(r.ID); gerr == nil && cur.State == StateCancelled {
			return cur, ErrCancelled
		}
		r.PID = 0
		if mv := m.move(&r, StateCancelled); mv == nil {
			return r, ErrCancelled
		}
		return r, err
	}
	return m.fail(r, err)
}

// Event kinds the spec (PLAN 6.8) names for agent runs. The other states
// announce themselves as agent.run.<state> (queued, planning, running, approved,
// pushed, cancelled, push-failed): progress, mostly transient.
const (
	EventPlanReady = "agent.plan.ready" // the plan waits for approval
	EventRunEnd    = "agent.run.end"    // the run finished and its diff waits for review
	EventRunFailed = "agent.run.failed"
)

// eventKind maps a state word to its event kind.
func eventKind(what string) string {
	switch what {
	case "awaiting-approval":
		return EventPlanReady
	case "review":
		return EventRunEnd
	}
	return "agent.run." + what
}

func (m *Manager) emit(r Run, what string, sev events.Severity, transient bool, title, detail string) {
	if m.Bus == nil {
		return
	}
	if title == "" {
		words := map[string]string{
			"queued": "queued", "planning": "planning", "running": "running", "approved": "plan approved",
			"awaiting-approval": "plan needs your approval", "review": "ready for review",
			"pushed": "pushed", "cancelled": "cancelled",
		}
		title = fmt.Sprintf("agent %s: %s", r.Item(), firstNonEmpty(words[what], what))
	}
	e := events.New(eventKind(what), sev, title)
	e.Project, e.Subject, e.Detail, e.Transient = r.Project, r.Item(), detail, transient
	m.Bus.Publish(e)
}

func (m *Manager) audit(e audit.Entry) { _ = m.Audit.Append(e) }

func (m *Manager) recordStop(item, feature, profile, model string, stop *ai.StopError) {
	m.audit(audit.Entry{
		Action: ledger.StopAction, Target: item, Item: item, Outcome: "error", Error: stop.Message,
		Params: map[string]any{"feature": feature, "profile": profile, "model": model, "source": stop.Source},
	})
	if m.Bus != nil {
		e := events.New(ledger.StopAction, events.Error, "AI budget reached: "+stop.Message)
		e.Subject = item
		m.Bus.Publish(e)
	}
}

// --- one call -----------------------------------------------------------------

// call runs one agent process for r and accounts for it: transcript, running
// cost, the hard stop, audit start/end, cost.json. It does not change r's
// state.
func (m *Manager) call(ctx context.Context, r *Run, kind Kind, prompt string) (callResult, error) {
	name, p, err := m.Cfg.Profile(r.Profile)
	if err != nil {
		return callResult{}, err
	}
	drv, err := m.Open(p, r.Driver)
	if err != nil {
		return callResult{}, err
	}
	if err := Available(drv); err != nil {
		return callResult{}, fmt.Errorf("agent driver unavailable: %w", err)
	}
	spec := Spec{Kind: kind, Model: r.Model, Dir: r.Worktree, AddDirs: r.AddDirs, Prompt: prompt, Price: priceOf(p, r.Model)}
	if r.CapUSD > 0 {
		if spec.MaxCostUSD = r.Remaining(); spec.MaxCostUSD <= 0 {
			return callResult{}, &ai.StopError{Source: "local", Message: fmt.Sprintf("run %s has used its cap of $%.2f", r.ID, r.CapUSD)}
		}
	}
	dir := m.Store.Dir(*r)
	if err := writeFile(filepath.Join(dir, "prompt-"+string(kind)+".md"), []byte(prompt)); err != nil {
		return callResult{}, err
	}
	now := m.Now()
	header := fmt.Sprintf(`{"type":"ade.call","kind":%q,"driver":%q,"model":%q,"run":%q,"started":%q}`, kind, drv.Name(), r.Model, r.ID, now.UTC().Format(time.RFC3339))
	tf, err := openTranscript(dir, header)
	if err != nil {
		return callResult{}, err
	}
	defer tf.Close()

	r.Calls = append(r.Calls, Call{Kind: kind, Driver: drv.Name(), Model: r.Model, Started: now})
	idx := len(r.Calls) - 1
	spentBefore := r.CostUSD
	m.audit(audit.Entry{
		Actor: "agent:" + r.ID, Action: "agent.run.start", Target: r.Item(), Item: r.Item(), Project: r.Project,
		Params: map[string]any{"run": r.ID, "call": string(kind), "driver": drv.Name(), "model": r.Model, "cap_usd": r.CapUSD, "worktree": r.Worktree},
	})

	cmd := drv.Command(spec)
	cmd.Env = append(cmd.Env, workspace.AgentRunEnv+"=1") // only the child: ADE's own push stays unmarked
	hooks := procHooks{
		transcript: tf, capUSD: r.CapUSD, spentBefore: spentBefore,
		onStart: func(pid int) { r.PID = pid; m.saveLive(r) },
		onProgress: func(pr Progress) {
			r.CostUSD = spentBefore + pr.CostUSD
			r.Calls[idx].CostUSD, r.Calls[idx].Turns = pr.CostUSD, pr.Turns
			r.Updated = m.Now()
			m.saveLive(r)
		},
	}
	res := runProcess(ctx, cmd, drv.Parser(spec), hooks, drv.Name() == "omp")
	// An external `ade agent cancel` kills the process and marks the run.
	if m.cancelled(r.ID) {
		res.Err = ErrCancelled
	}

	pr := res.Progress
	c := &r.Calls[idx]
	c.Ended, c.Exit, c.CostUSD, c.Turns = m.Now(), res.Exit, pr.CostUSD, pr.Turns
	c.TokensIn, c.TokensOut, c.StoppedBy = pr.In, pr.Out, res.StoppedBy
	c.Source = firstNonEmpty(pr.Source, CostUnknown)
	r.CostUSD, r.PID = spentBefore+pr.CostUSD, 0
	m.saveLive(r)
	_ = m.Store.writeCost(*r)
	m.recordCall(*r, *c, name, res)
	return res, res.Err
}

// cancelled reports that the stored run was cancelled (by another process).
func (m *Manager) cancelled(id string) bool {
	cur, err := m.Store.exact(id)
	return err == nil && cur.State == StateCancelled
}

// saveLive persists a running call's progress without overwriting a
// cancellation made meanwhile: the cancellation wins.
func (m *Manager) saveLive(r *Run) {
	if m.cancelled(r.ID) {
		r.State = StateCancelled
	}
	_ = m.Store.Save(*r)
}

// recordCall writes the audit entries of a finished call: metadata and cost
// only, never the transcript.
func (m *Manager) recordCall(r Run, c Call, profile string, res callResult) {
	outcome, errText := "ok", ""
	switch {
	case res.StoppedBy != "":
		outcome, errText = "error", "stopped at the cost cap"
	case res.Err != nil:
		outcome, errText = "error", res.Err.Error()
	case res.Exit != 0 || res.Progress.Failed != "":
		outcome, errText = "error", firstNonEmpty(res.Progress.Failed, fmt.Sprintf("exit %d", res.Exit))
	}
	params := map[string]any{"run": r.ID, "call": string(c.Kind), "exit": c.Exit, "turns": c.Turns, "cost_usd": c.CostUSD, "run_cost_usd": r.CostUSD, "stopped_by": c.StoppedBy}
	m.audit(audit.Entry{
		Actor: "agent:" + r.ID, Action: "agent.run.end", Target: r.Item(), Item: r.Item(), Project: r.Project,
		Params: params, Outcome: outcome, Error: errText,
	})
	m.audit(audit.Entry{
		Actor: "agent:" + r.ID, Action: "ai.agent-" + string(r.Kind), Target: r.Item(), Item: r.Item(), Project: r.Project,
		Params: map[string]any{"run": r.ID, "call": string(c.Kind)}, Outcome: outcome, Error: errText,
		AI: &audit.AIInfo{
			Profile: profile, Driver: c.Driver, Model: firstNonEmpty(res.Progress.Model, c.Model),
			TokensIn: c.TokensIn, TokensOut: c.TokensOut, TokensCache: res.Progress.Cache,
			CostUSD: c.CostUSD, CostSource: c.Source,
		},
	})
	if c.StoppedBy != "" {
		m.recordStop(r.Item(), "agent-"+string(c.Kind), profile, c.Model,
			&ai.StopError{Source: "local", Message: fmt.Sprintf("run %s stopped at its $%.2f cap (spent $%.4f)", r.ID, r.CapUSD, r.CostUSD)})
	}
}

// outcome turns a finished process into an error, or nil when it succeeded.
func (m *Manager) outcome(res callResult) error {
	switch {
	case res.StoppedBy != "":
		return &ai.StopError{Source: "local", Message: fmt.Sprintf("cost cap reached (spent $%.4f)", res.Progress.CostUSD)}
	case res.Exit != 0:
		return fmt.Errorf("agent exited %d: %s", res.Exit, firstNonEmpty(res.Progress.Failed, res.Stderr))
	case res.Progress.Failed != "":
		return errors.New(res.Progress.Failed)
	}
	return nil
}
