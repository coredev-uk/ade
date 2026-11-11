package workspace

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/host"
	"github.com/coredev-uk/ade/internal/host/zellij"
)

// Host names for OpenResult.Host.
const (
	HostZellij = "zellij"
	HostVSCode = "vscode"
	HostPlain  = "plain"
)

// ErrNoSession means the zellij session to open a tab in is not running.
var ErrNoSession = errors.New("zellij session is not running")

type openConfig struct {
	layout, workspace, title, session, baseRef string
	trees                                      TreeOptions
}

// OpenOption tunes Open.
type OpenOption func(*openConfig)

// WithLayout picks the layout (code, agent, review, manual); the default is
// the workspace's `layout` in the manifest, else code.
func WithLayout(name string) OpenOption { return func(c *openConfig) { c.layout = name } }

// WithWorkspace picks the manifest [workspaces] entry; default "default".
func WithWorkspace(name string) OpenOption { return func(c *openConfig) { c.workspace = name } }

// WithTitle names the tab (after "<repo>/"); default is the worktree directory name.
func WithTitle(title string) OpenOption { return func(c *openConfig) { c.title = title } }

// WithPins pins required repos to a branch or worktree path (`--with api=branch`).
func WithPins(pins map[string]string) OpenOption {
	return func(c *openConfig) { c.trees.Pins = pins }
}

// WithItemTrees names the work item's own worktrees in required repos.
func WithItemTrees(trees map[string]string) OpenOption {
	return func(c *openConfig) { c.trees.ItemTrees = trees }
}

// WithSession targets another zellij session than `ade`. Tests use it with
// throwaway `ade-test-*` sessions; nothing else should.
func WithSession(name string) OpenOption { return func(c *openConfig) { c.session = name } }

// WithBaseRef sets what the review layout diffs against; default origin/<default branch>.
func WithBaseRef(ref string) OpenOption { return func(c *openConfig) { c.baseRef = ref } }

// OpenResult says what Open did.
type OpenResult struct {
	Host       string `json:"host"`
	Session    string `json:"session,omitempty"`
	Tab        string `json:"tab"`
	Layout     string `json:"layout"`
	Created    bool   `json:"created"` // false: the tab already existed and was focused
	LayoutFile string `json:"layout_file,omitempty"`
	TasksFile  string `json:"tasks_file,omitempty"`
	// Hint is what to do by hand when the host cannot open it (plain, or code missing).
	Hint string `json:"hint,omitempty"`
}

// Open opens the workspace of a worktree in the user's host: a tab in the
// `ade` zellij session (reused when it exists), a VS Code window with
// generated tasks, or a `cd` hint on a plain terminal. It starts no
// processes itself: tab panes run `ade task run`, which brings up the run
// graph and shares instances with other workspaces.
func Open(ctx context.Context, a *app.App, project, repo, worktree string, opts ...OpenOption) (OpenResult, error) {
	p, err := prepare(ctx, a, project, repo, worktree, opts)
	if err != nil {
		return OpenResult{}, err
	}
	res, loc := p.res, p.loc
	hostKind, err := chooseHost(a.Config.Host.Mode, os.Getenv)
	if err != nil {
		return OpenResult{}, err
	}
	res.Host = hostKind
	switch hostKind {
	case HostZellij:
		err = openZellij(ctx, a, p, &res)
	case HostVSCode:
		err = openVSCode(ctx, p, &res)
	default:
		res.Hint = fmt.Sprintf("cd %s && ade ws up --no-zellij", shellQuote(loc.Tree))
	}
	entry := audit.Entry{
		Action: "workspace.open", Target: loc.Tree, Project: project, Outcome: "ok",
		Params: map[string]any{"host": hostKind, "layout": res.Layout, "tab": res.Tab},
	}
	if err != nil {
		entry.Outcome, entry.Error = "error", err.Error()
	}
	if aerr := a.Audit.Append(entry); aerr != nil && err == nil {
		err = aerr
	}
	return res, err
}

// RenderLayout returns the zellij KDL Open would use for the worktree.
func RenderLayout(ctx context.Context, a *app.App, project, repo, worktree string, opts ...OpenOption) (string, error) {
	p, err := prepare(ctx, a, project, repo, worktree, opts)
	if err != nil {
		return "", err
	}
	spec := layoutSpec(ctx, p)
	return BuildLayout(spec)
}

// prepared is everything Open and RenderLayout derive before touching a host.
type prepared struct {
	loc     Location
	plan    *Plan // nil when the manifest defines no such workspace
	cfg     openConfig
	res     OpenResult // Tab and Layout filled in
	ade     string
	runArgs []string // extra arguments of every generated `ade task run`
}

func prepare(ctx context.Context, a *app.App, project, repo, worktree string, opts []OpenOption) (*prepared, error) {
	var cfg openConfig
	for _, o := range opts {
		o(&cfg)
	}
	loc, err := Locate(a.Root, worktree)
	if err != nil {
		return nil, err
	}
	if loc.Project != project || loc.Repo != repo {
		return nil, fmt.Errorf("%s belongs to %s/%s, not %s/%s", worktree, loc.Project, loc.Repo, project, repo)
	}
	ws := cfg.workspace
	if ws == "" {
		ws = DefaultWorkspace
	}
	wsDef, hasWorkspace := loc.Manifest.Workspaces[ws]
	layout := cfg.layout
	if layout == "" {
		layout = wsDef.Layout
	}
	if layout == "" {
		layout = LayoutCode
	}
	if !ValidLayout(layout) {
		return nil, fmt.Errorf("unknown layout %q (want code, agent, review or manual)", layout)
	}
	p := &prepared{loc: loc, cfg: cfg}
	if hasWorkspace {
		mgr, err := NewManager(a)
		if err != nil {
			return nil, err
		}
		if p.plan, err = mgr.Plan(ctx, loc, PlanOptions{Workspace: ws, Trees: cfg.trees}); err != nil {
			return nil, err
		}
	}
	title := cfg.title
	if title == "" {
		title = filepath.Base(loc.Tree)
	}
	p.res = OpenResult{Tab: repo + "/" + title, Layout: layout}
	if p.ade, err = os.Executable(); err != nil {
		return nil, fmt.Errorf("locate ade executable: %w", err)
	}
	p.runArgs = appendTreeArgs(nil, cfg.trees)
	return p, nil
}

// chooseHost maps config [host].mode and the environment to a host.
func chooseHost(mode string, getenv func(string) string) (string, error) {
	_, zellijErr := exec.LookPath("zellij")
	switch mode {
	case host.ModeVSCode:
		return HostVSCode, nil
	case host.ModePlain:
		return HostPlain, nil
	case host.ModeZellij:
		if zellijErr != nil {
			return "", errors.New("host.mode is zellij but zellij is not on PATH")
		}
		return HostZellij, nil
	}
	switch {
	case getenv("TERM_PROGRAM") == "vscode" && getenv("ZELLIJ") == "":
		return HostVSCode, nil
	case zellijErr == nil:
		return HostZellij, nil
	}
	return HostPlain, nil
}

// appendTreeArgs renders tree options as `ade task run` arguments.
func appendTreeArgs(args []string, t TreeOptions) []string {
	for _, k := range slices.Sorted(maps.Keys(t.Pins)) {
		args = append(args, "--with", k+"="+t.Pins[k])
	}
	for _, k := range slices.Sorted(maps.Keys(t.ItemTrees)) {
		args = append(args, "--item-tree", k+"="+t.ItemTrees[k])
	}
	return args
}

// --- zellij ----------------------------------------------------------------

// zellijRunner runs zellij and returns stdout; injectable for tests.
type zellijRunner func(ctx context.Context, args ...string) (string, error)

func runZellij(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "zellij", args...)
	// The session is always named explicitly: an inherited $ZELLIJ_SESSION_NAME
	// (we may be running in some other session) must never redirect the command.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ZELLIJ") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return string(out), fmt.Errorf("zellij %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return string(out), fmt.Errorf("zellij %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func openZellij(ctx context.Context, a *app.App, p *prepared, res *OpenResult) error {
	session := p.cfg.session
	if session == "" {
		session = host.SessionName
	}
	res.Session = session
	kdl, err := BuildLayout(layoutSpec(ctx, p))
	if err != nil {
		return err
	}
	dir := filepath.Join(a.Paths.CacheDir, "ws", "layouts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, slugOf(p.loc.Tree+"#"+res.Tab)+".kdl")
	if err := os.WriteFile(file, []byte(kdl), 0o600); err != nil {
		return err
	}
	res.LayoutFile = file
	created, err := openTab(ctx, runZellij, session, res.Tab, file)
	res.Created = created
	return err
}

// openTab focuses the tab when it exists, otherwise creates it from layoutFile.
func openTab(ctx context.Context, run zellijRunner, session, tab, layoutFile string) (created bool, err error) {
	listing, _ := run(ctx, "list-sessions", "--no-formatting") // no server yet: no sessions
	if zellij.SessionState(listing, session) != zellij.Live {
		return false, fmt.Errorf("%w: %q (start it with `ade`)", ErrNoSession, session)
	}
	names, err := run(ctx, "--session", session, "action", "query-tab-names")
	if err != nil {
		return false, err
	}
	for name := range strings.SplitSeq(names, "\n") {
		if name == tab {
			_, err := run(ctx, "--session", session, "action", "go-to-tab-name", tab)
			return false, err
		}
	}
	_, err = run(ctx, "--session", session, "action", "new-tab", "--layout", layoutFile, "--name", tab)
	return err == nil, err
}

// layoutSpec assembles the layout input: tools found on PATH, a pane per
// run-graph node.
func layoutSpec(ctx context.Context, p *prepared) LayoutSpec {
	spec := LayoutSpec{Layout: p.res.Layout, Tab: p.res.Tab, Dir: p.loc.Tree, Tools: DetectTools(p.ade), BaseRef: p.cfg.baseRef}
	if spec.BaseRef == "" && spec.Layout == LayoutReview {
		tr := newTreeResolver(p.loc, TreeOptions{})
		if base, err := tr.git.DefaultBranch(ctx, filepath.Join(p.loc.RepoDir, ".bare")); err == nil {
			spec.BaseRef = "origin/" + base
		}
	}
	if p.plan == nil {
		return spec
	}
	inRoots := map[string]bool{}
	for _, r := range p.plan.Roots {
		inRoots[r] = true
	}
	for _, in := range p.plan.Instances {
		if in.Node.Oneshot() && !inRoots[in.Node.ID] {
			continue // runs as part of its dependents' `task run`
		}
		pane := Pane{Name: in.Node.ID, Args: append([]string{"task", "run", in.Node.ID, "--worktree", p.loc.Tree}, p.runArgs...)}
		if in.Node.Repo == "" || in.Tree.Path != p.loc.Tree {
			spec.Services = append(spec.Services, pane)
		} else {
			spec.Tasks = append(spec.Tasks, pane)
		}
	}
	return spec
}

// DetectTools looks up the programs the layouts use on PATH.
func DetectTools(ade string) Tools {
	find := func(name string) string {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return ""
	}
	t := Tools{Ade: ade, Lazygit: find("lazygit"), Delta: find("delta"), Omp: find("omp")}
	if ed := os.Getenv("EDITOR"); ed != "" {
		t.Editor = ed
	} else {
		t.Editor = find("nvim")
	}
	return t
}

// --- VS Code ---------------------------------------------------------------

func openVSCode(ctx context.Context, p *prepared, res *OpenResult) error {
	if p.plan != nil {
		path, err := WriteTasks(p.loc.Tree, BuildTasks(p.plan, p.ade, p.runArgs))
		if err != nil {
			return err
		}
		res.TasksFile = path
	}
	code, err := exec.LookPath("code")
	if err != nil {
		res.Hint = "code is not on PATH: open " + p.loc.Tree + " in VS Code"
		return nil
	}
	if err := exec.CommandContext(ctx, code, "--new-window", p.loc.Tree).Run(); err != nil {
		return fmt.Errorf("code: %w", err)
	}
	res.Created = true
	return nil
}
