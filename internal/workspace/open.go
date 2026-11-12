package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/devshell"
	"github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/host"
	"github.com/coredev-uk/ade/internal/host/zellij"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/records"
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
	item                                       *ItemRef
	items                                      []ItemRef
	devshell                                   devshell.Finder
}

// OpenOption tunes Open.
type OpenOption func(*openConfig)

// WithLayout picks the layout (code, agent, review, manual); the default is
// host.layout of the user config, else the workspace's `layout` in the
// manifest, else code.
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

// WithDevshellFinder replaces how the worktree's dev shell is detected (PATH
// and the file system by default); tests use it.
func WithDevshellFinder(f devshell.Finder) OpenOption { return func(c *openConfig) { c.devshell = f } }

// WithItem names the work item the worktree belongs to: its tab then shows the
// item's overview, the agent layout briefs omp on it, and the item's worktrees in
// the other repos feed the run graph.
func WithItem(it ItemRef) OpenOption { return func(c *openConfig) { c.item = &it } }

// WithItems offers the cached work items; Open finds the one that owns the
// worktree's branch (WithItem wins when both are given).
func WithItems(items []ItemRef) OpenOption { return func(c *openConfig) { c.items = items } }

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
	// Warnings are problems that did not stop the workspace opening (e.g. the
	// worktree's git config could not be written).
	Warnings []string `json:"warnings,omitempty"`
}

// Open opens the workspace of a worktree in the user's host: a tab in the
// `ade` zellij session (reused when it exists), a VS Code window with
// generated tasks, or a `cd` hint on a plain terminal. It starts no
// processes itself. The panes of the tab's strip run `ade task run`, which
// brings up the run graph and shares instances with other workspaces; they
// wait for Enter. With services = "background" the services tab's panes run
// as soon as that tab opens.
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
	if p.item != nil {
		// The worktree tells commit-msg hooks and the user's own git what the ticket is.
		target := commitprefix.TargetBranch(ctx, p.item.Item, loc.Manifest.Repos[loc.Repo].Path, loc.Tree)
		cfgErr := commitprefix.WriteWorktreeConfig(ctx, loc.Tree, p.item.Item.Ref.IID, commitprefix.ExpandPrefix(loc.Manifest, p.item.Item, loc.Repo, BranchOf(ctx, loc)), target)
		if cfgErr != nil {
			res.Warnings = append(res.Warnings, "worktree git config: "+cfgErr.Error())
		}
	}
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

// RenderServicesLayout returns the zellij KDL of the worktree's services tab;
// ok is false when the workspace would open none (services = "manual", or
// nothing to install or start).
func RenderServicesLayout(ctx context.Context, a *app.App, project, repo, worktree string, opts ...OpenOption) (kdl string, ok bool, err error) {
	p, err := prepare(ctx, a, project, repo, worktree, opts)
	if err != nil {
		return "", false, err
	}
	spec := layoutSpec(ctx, p)
	if !spec.HasServicesTab() {
		return "", false, nil
	}
	kdl, err = BuildServicesLayout(spec)
	return kdl, err == nil, err
}

// prepared is everything Open and RenderLayout derive before touching a host.
type prepared struct {
	loc     Location
	plan    *Plan // nil when the manifest defines no such workspace
	cfg     openConfig
	res     OpenResult // Tab and Layout filled in
	ade     string
	editor  editor.Editor // host.editor resolved against the environment
	runArgs []string      // extra arguments of every generated `ade task run`
	item    *ItemRef      // the work item owning the worktree's branch, when there is one
	agent   *agentFiles   // the omp skill; set only for the agent layout of an item
	// background says the run group's services start in a tab of their own;
	// install that the workspace's repos declare install commands to run first.
	background, install bool
	// agentUnset are the write-token variables the agent pane must not inherit.
	agentUnset []string
}

// agentFiles is the skill and overlay the agent layout's omp loads.
type agentFiles struct {
	dir   string // where WriteAgentFiles puts them
	input SkillInput
}

func (f agentFiles) overlay() string { return filepath.Join(f.dir, "omp.yml") }

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
		layout = DefaultLayout(a.Config.Host.Layout, wsDef.Layout)
	}
	if !ValidLayout(layout) {
		return nil, fmt.Errorf("unknown layout %q (want code, agent, review or manual)", layout)
	}
	p := &prepared{loc: loc, editor: editor.Resolve(a.Config.Host.Editor, editor.FromOS()), agentUnset: a.Config.AgentHiddenEnv()}
	if cfg.item != nil {
		p.item = cfg.item
	} else if it, ok := ItemFor(ctx, cfg.items, loc); ok {
		p.item = &it
	}
	if p.item != nil && len(cfg.trees.ItemTrees) == 0 {
		// The item's own worktrees feed the run graph (web calls the item's api, not the baseline's).
		cfg.trees.ItemTrees, _ = ItemTrees(ctx, a.Root, loc.Manifest, p.item.Item)
	}
	p.cfg = cfg
	// A review tab shows a diff: it must not bring up the run graph (services,
	// ports, required repos) for code that is only being read.
	if hasWorkspace && layout != LayoutReview {
		mgr, err := NewManager(a)
		if err != nil {
			return nil, err
		}
		if p.plan, err = mgr.Plan(ctx, loc, PlanOptions{Workspace: ws, Trees: cfg.trees}); err != nil {
			return nil, err
		}
		p.background = wsDef.ServicesMode() == config.ServicesBackground
		p.install = len(mgr.InstallTargets(loc, p.plan, true)) > 0
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
	if p.item != nil && layout == LayoutAgent {
		p.agent = agentFor(ctx, a, p)
	}
	return p, nil
}

// agentFor gathers what the implement skill says about the item.
func agentFor(ctx context.Context, a *app.App, p *prepared) *agentFiles {
	loc, it := p.loc, *p.item
	branch := BranchOf(ctx, loc)
	in := SkillInput{
		Item: it, Manifest: loc.Manifest, Repo: loc.Repo, Branch: branch, Worktree: loc.Tree,
		OtherTrees:   map[string]string{},
		CommitPrefix: commitprefix.ExpandPrefix(loc.Manifest, it.Item, loc.Repo, branch),
	}
	for repo, tree := range p.cfg.trees.ItemTrees {
		if repo != loc.Repo {
			in.OtherTrees[repo] = tree
		}
	}
	in.AgentsMD, in.AgentsPath = agentsDoc(loc.Tree)
	in.ProjectGuidance = project.ReadAgents(loc.ProjectDir)
	if rec, err := records.New(a.RecordsDir).Read(it.Forge, it.Item.Ref); err == nil {
		in.Notes = rec.Notes
	}
	return &agentFiles{dir: filepath.Join(a.Paths.CacheDir, "ws", "agent", slugOf(loc.Tree)), input: in}
}

// DefaultLayout is the layout a tab gets when nothing asks for one: your own
// host.layout beats the manifest workspace's layout, and code is the fallback.
func DefaultLayout(userLayout, manifestLayout string) string {
	switch {
	case userLayout != "":
		return userLayout
	case manifestLayout != "":
		return manifestLayout
	}
	return LayoutCode
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
		session = currentSession(os.Getenv)
	}
	res.Session = session
	if p.agent != nil {
		// Regenerated on every open, also when the tab exists: the skill stays current.
		if _, err := WriteAgentFiles(p.agent.dir, p.agent.input); err != nil {
			return fmt.Errorf("write the omp skill: %w", err)
		}
	}
	spec := layoutSpec(ctx, p)
	kdl, err := BuildLayout(spec)
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
	var services tabLayout
	if spec.HasServicesTab() {
		svc, err := BuildServicesLayout(spec)
		if err != nil {
			return err
		}
		services = tabLayout{name: spec.ServicesTab(), file: filepath.Join(dir, slugOf(p.loc.Tree+"#"+spec.ServicesTab())+".kdl")}
		if err := os.WriteFile(services.file, []byte(svc), 0o600); err != nil {
			return err
		}
	}
	created, servicesErr, err := openTab(ctx, runZellij, session, res.Tab, file, services)
	res.Created = created
	if servicesErr != nil {
		res.Warnings = append(res.Warnings, "services tab: "+servicesErr.Error())
	}
	return err
}

// currentSession is the session tabs open in by default: the one the caller is
// running inside (the dashboard may live in any zellij session, e.g. a user's own),
// else the `ade` session the launcher creates.
func currentSession(getenv func(string) string) string {
	if getenv("ZELLIJ") != "" {
		if name := getenv("ZELLIJ_SESSION_NAME"); name != "" {
			return name
		}
	}
	return host.SessionName
}

// FocusTab focuses the tab of a running zellij session or creates it from
// layoutFile (zellij focuses a new tab). created says which happened. The tab
// is expected to run one command that stays up (the dashboard): when its pane
// has exited, the tab is replaced by a fresh one, because zellij offers no way
// to run the pane again from outside.
func FocusTab(ctx context.Context, session, tab, layoutFile string) (created bool, err error) {
	return focusTab(ctx, runZellij, session, tab, layoutFile)
}

func focusTab(ctx context.Context, run zellijRunner, session, tab, layoutFile string) (created bool, err error) {
	if stale := exitedTabs(ctx, run, session, tab); len(stale) > 0 {
		// The new tab goes first: closing the session's last tab would end the session.
		if _, err := run(ctx, "--session", session, "action", "new-tab", "--layout", layoutFile, "--name", tab); err != nil {
			return false, err
		}
		for _, id := range stale {
			if _, err := run(ctx, "--session", session, "action", "close-tab-by-id", strconv.Itoa(id)); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	created, _, err = openTab(ctx, run, session, tab, layoutFile, tabLayout{})
	return created, err
}

// exitedTabs returns the ids of the tabs named tab that hold a command pane
// whose command has exited. A zellij that cannot list panes as JSON yields none.
func exitedTabs(ctx context.Context, run zellijRunner, session, tab string) []int {
	out, err := run(ctx, "--session", session, "action", "list-panes", "--json", "--tab", "--state")
	if err != nil {
		return nil
	}
	var panes []struct {
		IsPlugin bool   `json:"is_plugin"`
		Exited   bool   `json:"exited"`
		TabID    int    `json:"tab_id"`
		TabName  string `json:"tab_name"`
	}
	if json.Unmarshal([]byte(out), &panes) != nil {
		return nil
	}
	var ids []int
	for _, p := range panes {
		if p.TabName == tab && !p.IsPlugin && p.Exited && !slices.Contains(ids, p.TabID) {
			ids = append(ids, p.TabID)
		}
	}
	return ids
}

// tabLayout is a tab to create from a layout file; the zero value is none.
type tabLayout struct{ name, file string }

// openTab focuses the tab when it exists, otherwise creates it from layoutFile.
// A new tab also gets the background tab created before it (unless that one
// exists): zellij focuses every tab it creates, so the tab the user asked for
// goes last and ends up focused. A failure of the background tab is returned as
// backgroundErr and does not stop the main tab.
func openTab(ctx context.Context, run zellijRunner, session, tab, layoutFile string, background tabLayout) (created bool, backgroundErr, err error) {
	listing, _ := run(ctx, "list-sessions", "--no-formatting") // no server yet: no sessions
	if zellij.SessionState(listing, session) != zellij.Live {
		return false, nil, fmt.Errorf("%w: %q (start it with `ade`)", ErrNoSession, session)
	}
	names, err := run(ctx, "--session", session, "action", "query-tab-names")
	if err != nil {
		return false, nil, err
	}
	have := map[string]bool{}
	for name := range strings.SplitSeq(names, "\n") {
		have[name] = true
	}
	if have[tab] {
		_, err := run(ctx, "--session", session, "action", "go-to-tab-name", tab)
		return false, nil, err
	}
	if background.name != "" && !have[background.name] {
		_, backgroundErr = run(ctx, "--session", session, "action", "new-tab", "--layout", background.file, "--name", background.name)
	}
	_, err = run(ctx, "--session", session, "action", "new-tab", "--layout", layoutFile, "--name", tab)
	return err == nil, backgroundErr, err
}

// layoutSpec assembles the layout input: tools found on PATH, a pane per
// run-graph node.
func layoutSpec(ctx context.Context, p *prepared) LayoutSpec {
	spec := LayoutSpec{Layout: p.res.Layout, Tab: p.res.Tab, Dir: p.loc.Tree, Tools: DetectTools(p.ade, p.editor, p.loc.Tree), BaseRef: p.cfg.baseRef}
	spec.Devshell = p.cfg.devshell.WithProject(
		p.cfg.devshell.Detect(p.loc.Tree, p.loc.Manifest.Repos[p.loc.Repo].Devshell), p.loc.ProjectDir, p.loc.Manifest.Project)
	if p.item != nil {
		spec.Overview = []string{"item", p.item.Item.Ref.String(), "--watch"}
	}
	// Every agent pane drops the write token, with or without a work item.
	spec.AgentUnset = p.agentUnset
	if p.agent != nil {
		spec.Agent = AgentArgs(p.agent.overlay(), *p.item)
	}
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
		if p.background || p.install {
			// Waits for the install (in manual mode: for `ade ws install` or `ade ws up`); a
			// service of the background tab also waits for what it requires.
			pane.Args = append(pane.Args, "--after-install")
		}
		if in.Node.Repo == "" || in.Tree.Path != p.loc.Tree {
			if p.background {
				pane.Args = append(pane.Args, "--wait-requires")
			}
			spec.Services = append(spec.Services, pane)
		} else {
			spec.Tasks = append(spec.Tasks, pane)
		}
	}
	spec.BackgroundServices = p.background
	if p.background && p.install {
		spec.Install = Pane{Name: "install", Args: append([]string{"ws", "install", p.loc.Tree, "--workspace", p.plan.Workspace}, p.runArgs...)}
	}
	if len(spec.Services)+len(spec.Tasks) > 0 {
		spec.Up = Pane{Name: "services", Args: append([]string{"ws", "up", p.loc.Tree, "--no-zellij"}, p.runArgs...)}
	}
	return spec
}

// DetectTools looks up the programs the layouts use on PATH; the editor is the
// resolved host.editor and opens dir.
func DetectTools(ade string, ed editor.Editor, dir string) Tools {
	find := func(name string) string {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return ""
	}
	t := Tools{Ade: ade, Lazygit: find("lazygit"), Delta: find("delta"), Omp: find("omp")}
	t.Editor, t.EditorArgs = ed.PaneCommand(dir)
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
	env := editor.FromOS()
	ed := editor.Resolve(editor.SettingCode, env)
	if ed.Kind != editor.Code {
		res.Hint = env.NoCodeHint(p.loc.Tree)
		return nil
	}
	cmd := exec.CommandContext(ctx, ed.Cmd, "--new-window", p.loc.Tree)
	cmd.Env = ed.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("code: %w", err)
	}
	res.Created = true
	return nil
}
