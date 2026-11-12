package config

import (
	"fmt"
	"maps"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/commitmsg"
)

// Severity ranks an Issue. Errors make a config unusable; warnings are advice.
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
)

// Issue is one validation finding. Path locates it in the TOML document, e.g.
// "repos.web.tasks.dev.requires[0]".
type Issue struct {
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`
	Msg      string   `json:"msg"`
}

func (i Issue) String() string {
	if i.Path == "" {
		return fmt.Sprintf("%s: %s", i.Severity, i.Msg)
	}
	return fmt.Sprintf("%s: %s: %s", i.Severity, i.Path, i.Msg)
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	return slices.ContainsFunc(issues, func(i Issue) bool { return i.Severity == SeverityError })
}

var (
	// projectName becomes a directory name and the prefix of qualified refs.
	projectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// identName names repos, tasks and services. Dots are excluded because
	// they separate the parts of a node id.
	identName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	template  = regexp.MustCompile(`(\$?)\{([^{}]*)\}`)
	retainFmt = regexp.MustCompile(`^(forever|\d+d)$`)
)

const servicesPrefix = "services"

type issues struct{ list []Issue }

func (s *issues) errorf(path, format string, args ...any) {
	s.list = append(s.list, Issue{SeverityError, path, fmt.Sprintf(format, args...)})
}

func (s *issues) warnf(path, format string, args ...any) {
	s.list = append(s.list, Issue{SeverityWarn, path, fmt.Sprintf(format, args...)})
}

func oneOf(v string, allowed ...string) bool { return slices.Contains(allowed, v) }

// Validate checks the manifest on its own. Qualified references to other
// manifests ("<manifest>/<repo>.<task>") are syntax-checked only; ValidateAll
// resolves them. cfg may be nil to skip checks against the user config.
func (m *Manifest) Validate(cfg *Config) []Issue {
	return newManifestValidator(m, cfg, nil).run()
}

// ValidateAll validates every manifest, resolving qualified cross-manifest
// references and detecting duplicate project names. The result is keyed by
// Manifest.Source (falling back to Name) and has an entry for every input.
func ValidateAll(ms []*Manifest, cfg *Config) map[string][]Issue {
	byName := make(map[string]*Manifest, len(ms))
	for _, m := range ms {
		if _, dup := byName[m.Name]; !dup && m.Name != "" {
			byName[m.Name] = m
		}
	}
	out := make(map[string][]Issue, len(ms))
	for i, m := range ms {
		key := m.Source
		if key == "" {
			key = m.Name
		}
		if key == "" {
			key = "manifest#" + strconv.Itoa(i)
		}
		list := newManifestValidator(m, cfg, byName).run()
		if first, dup := byName[m.Name]; dup && first != m {
			list = append(list, Issue{
				SeverityError, "name",
				fmt.Sprintf("project name %q is already defined by %s", m.Name, first.Source),
			})
		}
		out[key] = append(out[key], list...)
	}
	return out
}

// node is one vertex of the run graph.
type node struct {
	id   string // "<repo>.<task>" or "services.<name>"
	repo string // owning repo; empty for services
	path string // TOML path of the node's table
	task Task
}

type edge struct {
	to  string
	idx int // index into the requires list, for error paths
}

type manifestValidator struct {
	issues
	m      *Manifest
	cfg    *Config
	others map[string]*Manifest // all manifests by name; nil when validating alone

	nodes   map[string]node
	nodeIDs []string
	edges   map[string][]edge
	qual    map[string][]string // qualified requires declared directly by a node
}

func newManifestValidator(m *Manifest, cfg *Config, others map[string]*Manifest) *manifestValidator {
	return &manifestValidator{m: m, cfg: cfg, others: others}
}

func (v *manifestValidator) run() []Issue {
	v.checkHeader()
	v.checkIterations()
	v.checkRepos()
	v.collectNodes()
	v.checkNodes()
	v.checkGroups()
	v.checkConventions()
	v.checkDotfiles()
	for _, k := range v.m.Undecoded {
		v.warnf(k, "unknown key")
	}
	return v.list
}

func (v *manifestValidator) checkHeader() {
	m := v.m
	switch {
	case m.Name == "":
		v.errorf("name", "required")
	case !projectName.MatchString(m.Name):
		v.errorf("name", "%q is not path-safe: use letters, digits, '.', '_' or '-', starting with a letter or digit", m.Name)
	}
	switch {
	case m.Forge == "" && v.needsForge():
		v.errorf("forge", "required: name a forge from the user config (needed by the tracker and by repos without url)")
	case m.Forge != "" && v.cfg != nil:
		if _, ok := v.cfg.Forges[m.Forge]; !ok {
			v.errorf("forge", "forge %q is not defined in the user config (have: %s)", m.Forge, joinOrNone(slices.Sorted(maps.Keys(v.cfg.Forges))))
		}
	}
}

func (v *manifestValidator) checkIterations() {
	it := v.m.Tracker.Iterations
	if it.Start != "" {
		if _, err := time.Parse("2006-01-02", it.Start); err != nil {
			v.errorf("tracker.iterations.start", "%q is not a date: want YYYY-MM-DD", it.Start)
		}
	}
	if it.Length != "" {
		if _, err := ParseSprintLength(it.Length); err != nil {
			v.errorf("tracker.iterations.length", "%v", err)
		}
	}
	if it.Overlap < 0 {
		v.errorf("tracker.iterations.overlap", "must not be negative")
	}
}

// needsForge reports whether anything in the manifest resolves through a
// forge: the tracker, or a repo cloned from forge path rather than explicit url.
func (v *manifestValidator) needsForge() bool {
	if v.m.Tracker.Project != "" {
		return true
	}
	for _, r := range v.m.Repos {
		if r.URL == "" {
			return true
		}
	}
	return false
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

// --- checkout graph -------------------------------------------------------

func (v *manifestValidator) checkRepos() {
	edges := map[string][]edge{}
	repoNames := v.m.RepoNames()
	for _, name := range repoNames {
		r := v.m.Repos[name]
		p := "repos." + name
		switch {
		case name == servicesPrefix:
			v.errorf(p, "%q is reserved for services", servicesPrefix)
		case !identName.MatchString(name):
			v.errorf(p, "invalid repo name %q: use letters, digits, '_' or '-'", name)
		}
		if r.Path == "" && r.URL == "" {
			v.errorf(p, "needs path or url")
		}
		if !oneOf(r.Devshell, "", "auto", "direnv", "nix", "none") {
			v.errorf(p+".devshell", "unknown value %q: want auto, direnv, nix or none", r.Devshell)
		}
		for i, cmd := range r.Hooks.Install {
			if strings.TrimSpace(cmd) == "" {
				v.errorf(fmt.Sprintf("%s.hooks.install[%d]", p, i), "empty command")
			}
		}
		for i, req := range r.Requires {
			rp := fmt.Sprintf("%s.requires[%d]", p, i)
			if local, ok := v.resolveRepo(rp, req); ok {
				edges[name] = append(edges[name], edge{local, i})
			}
		}
	}
	for _, c := range findCycles(repoNames, edges) {
		v.errorf(fmt.Sprintf("repos.%s.requires[%d]", c.from, c.idx), "checkout dependency cycle: %s", strings.Join(c.path, " → "))
	}
}

// splitQualified separates "<manifest>/<rest>"; manifest is empty for local refs.
func splitQualified(s string) (manifest, rest string) {
	if i := strings.Index(s, "/"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// resolveRepo checks a repo reference and returns the local repo name. Qualified
// refs yield ok=false: they add no local edge.
func (v *manifestValidator) resolveRepo(path, ref string) (local string, ok bool) {
	manifest, name := splitQualified(ref)
	if manifest == "" {
		if _, exists := v.m.Repos[name]; !exists {
			v.errorf(path, "unknown repo %q", ref)
			return "", false
		}
		return name, true
	}
	if v.others != nil {
		target, exists := v.others[manifest]
		switch {
		case !exists:
			v.errorf(path, "unknown manifest %q in %q", manifest, ref)
		default:
			if _, has := target.Repos[name]; !has {
				v.errorf(path, "manifest %q has no repo %q", manifest, name)
			}
		}
	}
	return "", false
}

// checkoutClosure returns repo plus every repo it transitively requires.
func (v *manifestValidator) checkoutClosure(repo string) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(r string) {
		if seen[r] {
			return
		}
		seen[r] = true
		for _, req := range v.m.Repos[r].Requires {
			if m, name := splitQualified(req); m == "" {
				walk(name)
			}
		}
	}
	walk(repo)
	return seen
}

// --- run graph -------------------------------------------------------------

func (v *manifestValidator) collectNodes() {
	v.nodes = map[string]node{}
	for _, rn := range v.m.RepoNames() {
		for _, tn := range slices.Sorted(maps.Keys(v.m.Repos[rn].Tasks)) {
			v.addNode(node{id: rn + "." + tn, repo: rn, path: fmt.Sprintf("repos.%s.tasks.%s", rn, tn), task: v.m.Repos[rn].Tasks[tn]}, tn)
		}
	}
	for _, sn := range slices.Sorted(maps.Keys(v.m.Services)) {
		v.addNode(node{id: servicesPrefix + "." + sn, path: servicesPrefix + "." + sn, task: v.m.Services[sn]}, sn)
	}
}

func (v *manifestValidator) addNode(n node, name string) {
	if !identName.MatchString(name) {
		v.errorf(n.path, "invalid name %q: use letters, digits, '_' or '-'", name)
	}
	v.nodes[n.id] = n
	v.nodeIDs = append(v.nodeIDs, n.id)
}

// resolveNode checks a node reference and returns the local node id.
func (v *manifestValidator) resolveNode(path, ref string) (local string, ok bool) {
	manifest, id := splitQualified(ref)
	repo, task, wellFormed := strings.Cut(id, ".")
	if !wellFormed || repo == "" || task == "" || strings.Contains(task, ".") {
		v.errorf(path, "malformed reference %q: want <repo>.<task> or services.<name>", ref)
		return "", false
	}
	if manifest == "" {
		if _, exists := v.nodes[id]; !exists {
			v.errorf(path, "unknown reference %q", ref)
			return "", false
		}
		return id, true
	}
	if v.others != nil {
		target, exists := v.others[manifest]
		if !exists {
			v.errorf(path, "unknown manifest %q in %q", manifest, ref)
		} else if !target.hasNode(id) {
			v.errorf(path, "manifest %q has no node %q", manifest, id)
		}
	}
	return "", false
}

func (m *Manifest) hasNode(id string) bool {
	owner, name, _ := strings.Cut(id, ".")
	if owner == servicesPrefix {
		_, ok := m.Services[name]
		return ok
	}
	_, ok := m.Repos[owner].Tasks[name]
	return ok
}

func (v *manifestValidator) checkNodes() {
	v.edges = map[string][]edge{}
	v.qual = map[string][]string{}
	for _, id := range v.nodeIDs {
		n := v.nodes[id]
		for i, req := range n.task.Requires {
			path := fmt.Sprintf("%s.requires[%d]", n.path, i)
			if local, ok := v.resolveNode(path, req); ok {
				v.edges[id] = append(v.edges[id], edge{local, i})
			} else if m, _ := splitQualified(req); m != "" {
				v.qual[id] = append(v.qual[id], req)
			}
		}
		v.checkTask(n)
	}
	for _, c := range findCycles(v.nodeIDs, v.edges) {
		v.errorf(fmt.Sprintf("%s.requires[%d]", v.nodes[c.from].path, c.idx), "dependency cycle: %s", strings.Join(c.path, " → "))
	}
	for _, id := range v.nodeIDs {
		v.checkEnv(v.nodes[id])
	}
	v.checkPortClashes()
}

func (v *manifestValidator) checkTask(n node) {
	t := n.task
	if !oneOf(t.Kind, "", "service", "oneshot") {
		v.errorf(n.path+".kind", "unknown kind %q: want service or oneshot", t.Kind)
	}
	var probes []string
	for _, p := range []struct{ name, val string }{{"http", t.Ready.HTTP}, {"tcp", t.Ready.TCP}, {"log", t.Ready.Log}} {
		if p.val != "" {
			probes = append(probes, p.name)
		}
	}
	switch {
	case len(probes) > 1:
		v.errorf(n.path+".ready", "set at most one of http, tcp, log (got %s)", strings.Join(probes, ", "))
	case len(probes) == 1 && t.Kind == "oneshot":
		v.errorf(n.path+".ready."+probes[0], "oneshot tasks are ready when they exit 0; remove ready.%s", probes[0])
	}
	if t.Ready.Log != "" {
		if _, err := regexp.Compile(t.Ready.Log); err != nil {
			v.errorf(n.path+".ready.log", "invalid regular expression: %v", err)
		}
	}
	if t.Cmd == "" {
		v.errorf(n.path+".cmd", "required")
	}
}

// reach returns every node id transitively required by id (excluding id itself
// unless it sits on a cycle), plus the qualified refs declared along the way.
func (v *manifestValidator) reach(id string) (nodes map[string]bool, qualified map[string]bool) {
	nodes, qualified = map[string]bool{}, map[string]bool{}
	var walk func(string)
	walk = func(u string) {
		for _, q := range v.qual[u] {
			qualified[q] = true
		}
		for _, e := range v.edges[u] {
			if !nodes[e.to] {
				nodes[e.to] = true
				walk(e.to)
			}
		}
	}
	walk(id)
	return nodes, qualified
}

func hasPort(t Task) bool { return t.Port.Auto || t.Port.Fixed > 0 }

func (v *manifestValidator) checkEnv(n node) {
	if len(n.task.Env) == 0 {
		return
	}
	closure, qualified := v.reach(n.id)
	closure[n.id] = true
	repos := v.repoScope(n, closure)
	for _, key := range slices.Sorted(maps.Keys(n.task.Env)) {
		path := n.path + ".env." + key
		for _, match := range template.FindAllStringSubmatch(n.task.Env[key], -1) {
			if match[1] == "$" { // ${VAR}: shell syntax, not ours
				continue
			}
			v.checkTemplate(path, match[2], n, closure, qualified, repos)
		}
	}
}

// repoScope lists the repos whose directories a node may reference: its own,
// those its repo checks out, and those owning nodes it waits for.
func (v *manifestValidator) repoScope(n node, closure map[string]bool) map[string]bool {
	scope := map[string]bool{}
	if n.repo != "" {
		scope = v.checkoutClosure(n.repo)
	}
	for id := range closure {
		if r := v.nodes[id].repo; r != "" {
			scope[r] = true
		}
	}
	return scope
}

func (v *manifestValidator) checkTemplate(path, ref string, n node, closure, qualified, repos map[string]bool) {
	switch ref {
	case "dir", "state":
		return
	case "port":
		if !hasPort(n.task) {
			v.errorf(path, "{port} used but this node declares no port")
		}
		return
	}
	if strings.Contains(ref, "/") {
		target := strings.TrimSuffix(strings.TrimSuffix(ref, ".port"), ".dir")
		if !qualified[target] {
			v.errorf(path, "{%s} references a node outside this node's requires closure", ref)
		}
		return
	}
	parts := strings.Split(ref, ".")
	switch {
	case len(parts) == 2 && parts[1] == "dir" && parts[0] != servicesPrefix:
		if _, ok := v.m.Repos[parts[0]]; !ok {
			v.errorf(path, "{%s}: unknown repo %q", ref, parts[0])
		} else if !repos[parts[0]] {
			v.errorf(path, "{%s} references a repo that is neither this node's repo, one it requires, nor owns a required node", ref)
		}
	case len(parts) == 3 && parts[2] == "port":
		id := parts[0] + "." + parts[1]
		target, ok := v.nodes[id]
		switch {
		case !ok:
			v.errorf(path, "{%s}: unknown reference %q", ref, id)
		case id != n.id && !closure[id]:
			v.errorf(path, "{%s} references %s, which is outside this node's requires closure", ref, id)
		case !hasPort(target.task):
			v.errorf(path, "{%s}: %s declares no port", ref, id)
		}
	default:
		v.errorf(path, "unknown template variable {%s}", ref)
	}
}

func (v *manifestValidator) checkPortClashes() {
	owner := map[int]string{}
	for _, id := range v.nodeIDs {
		n := v.nodes[id]
		port := n.task.Port.Fixed
		if port == 0 {
			continue
		}
		if first, taken := owner[port]; taken {
			v.errorf(n.path+".port", "fixed port %d is also used by %s", port, first)
			continue
		}
		owner[port] = id
	}
}

// --- groups, workspaces, conventions ---------------------------------------

func (v *manifestValidator) checkGroups() {
	for _, g := range slices.Sorted(maps.Keys(v.m.Groups)) {
		for i, ref := range v.m.Groups[g] {
			v.resolveNode(fmt.Sprintf("groups.%s[%d]", g, i), ref)
		}
	}
	for _, w := range slices.Sorted(maps.Keys(v.m.Workspaces)) {
		run := v.m.Workspaces[w].Run
		if _, ok := v.m.Groups[run]; run != "" && !ok {
			v.errorf("workspaces."+w+".run", "unknown group %q", run)
		}
		if s := v.m.Workspaces[w].Services; !oneOf(s, "", ServicesBackground, ServicesManual) {
			v.errorf("workspaces."+w+".services", "unknown value %q: want background or manual", s)
		}
	}
}

func (v *manifestValidator) checkConventions() {
	m := v.m
	for _, f := range []struct{ path, pattern string }{
		{"labels.type.pattern", m.Labels.Type.Pattern},
		{"labels.app.pattern", m.Labels.App.Pattern},
		{"labels.workflow.pattern", m.Labels.Workflow.Pattern},
	} {
		if f.pattern != "" && !strings.Contains(f.pattern, "{value}") {
			v.errorf(f.path, "pattern %q must contain {value}", f.pattern)
		}
	}
	if s := m.Labels.Workflow.Sync; !oneOf(s, "", "auto", "ask", "off") {
		v.errorf("labels.workflow.sync", "unknown value %q: want auto, ask or off", s)
	}
	if s := m.Conventions.ChildIssue; !oneOf(s, "", "auto", "always", "never") {
		v.errorf("conventions.child_issue", "unknown value %q: want auto, always or never", s)
	}
	if err := commitmsg.Validate(m.Conventions.CommitPrefix); err != nil {
		v.errorf("conventions.commit_prefix", "%v", err)
	}
	if s := m.Conventions.MR.Create; !oneOf(s, "", "ask", "always", "never") {
		v.errorf("conventions.mr.create", "unknown value %q: want ask, always or never", s)
	}
	if s := m.Tracker.Iterations.Source; !oneOf(s, "", "auto", "gitlab-iterations", "milestones", "labels", "github-project", "cadence") {
		v.errorf("tracker.iterations.source", "unknown source %q: want auto, gitlab-iterations, milestones, labels, github-project or cadence", s)
	}
	switch w := m.Tracker.Weight; {
	case !oneOf(w.Source, "", "auto", "gitlab", "label", "github-field", "none"):
		v.errorf("tracker.weight.source", "unknown source %q: want auto, gitlab, label, github-field or none", w.Source)
	case w.Source == "label" && !strings.Contains(w.Pattern, "{value}"):
		v.errorf("tracker.weight.pattern", "label weights need a pattern containing {value}, e.g. \"weight::{value}\"")
	case w.Source == "github-field" && w.Field == "":
		v.errorf("tracker.weight.field", "github-field weights need the project field name, e.g. \"Estimate\"")
	}
}

func (v *manifestValidator) checkDotfiles() {
	d := v.m.Dotfiles
	if d == (Dotfiles{}) {
		return
	}
	switch {
	case d.Repo == "":
		v.errorf("dotfiles.repo", "required when [dotfiles] has other keys: the git URL of the team dotfiles")
	case strings.HasPrefix(d.Repo, "-"):
		v.errorf("dotfiles.repo", "%q must be a git URL, not an option", d.Repo)
	}
	if strings.HasPrefix(d.Branch, "-") {
		v.errorf("dotfiles.branch", "%q must be a branch name, not an option", d.Branch)
	}
	if d.Install != "" {
		if clean := path.Clean(d.Install); path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			v.errorf("dotfiles.install", "%q must be a path inside the dotfiles repo", d.Install)
		}
	}
}

// --- cycles ---------------------------------------------------------------

type cycle struct {
	path []string // a, b, c, a
	from string   // node holding the edge that closes the cycle
	idx  int      // that edge's index in from's requires list
}

// findCycles runs a deterministic DFS over ids (in the given order) and returns
// each back edge as a cycle.
func findCycles(ids []string, edges map[string][]edge) []cycle {
	const (
		white = iota
		gray
		black
	)
	state := map[string]int{}
	var stack []string
	var found []cycle
	var visit func(string)
	visit = func(u string) {
		state[u] = gray
		stack = append(stack, u)
		for _, e := range edges[u] {
			switch state[e.to] {
			case gray:
				start := slices.Index(stack, e.to)
				path := append(slices.Clone(stack[start:]), e.to)
				found = append(found, cycle{path: path, from: u, idx: e.idx})
			case white:
				visit(e.to)
			}
		}
		stack = stack[:len(stack)-1]
		state[u] = black
	}
	for _, id := range ids {
		if state[id] == white {
			visit(id)
		}
	}
	return found
}

// --- user config ------------------------------------------------------------

// Validate checks the user config. Missing files are not an issue here;
// callers decide how to report a config that does not exist.
func (c *Config) Validate() []Issue {
	var s issues
	for _, name := range slices.Sorted(maps.Keys(c.Forges)) {
		validateForge(&s, "forges."+name, c.Forges[name])
	}
	for i, p := range c.Projects {
		if strings.TrimSpace(p.Source) == "" {
			s.errorf(fmt.Sprintf("projects[%d].source", i), "required")
		}
	}
	if f := c.Update.FeatureBranches; !oneOf(f, "ff-if-clean", "notify-only") {
		s.errorf("update.feature_branches", "unknown value %q: want ff-if-clean or notify-only", f)
	}
	if c.Update.Interval.Duration < 0 {
		s.errorf("update.interval", "must not be negative")
	}
	if c.Update.Concurrency < 1 {
		s.errorf("update.concurrency", "must be at least 1")
	}
	if !retainFmt.MatchString(c.Records.AuditRetain) {
		s.errorf("records.audit_retain", "%q: want \"forever\" or a day count like \"365d\"", c.Records.AuditRetain)
	}
	if !oneOf(c.Host.Mode, "auto", "zellij", "vscode", "plain") {
		s.errorf("host.mode", "unknown value %q: want auto, zellij, vscode or plain", c.Host.Mode)
	}
	if !oneOf(c.Host.Editor, "auto", "nvim", "ade-nvim", "code", "$EDITOR") {
		s.errorf("host.editor", "unknown value %q: want auto, nvim, ade-nvim, code or $EDITOR", c.Host.Editor)
	}
	if !oneOf(c.Host.Glyphs, "unicode", "nerd", "ascii") {
		s.errorf("host.glyphs", "unknown value %q: want unicode, nerd or ascii", c.Host.Glyphs)
	}
	if !oneOf(c.Host.Layout, "", "code", "agent", "manual") {
		s.errorf("host.layout", "unknown value %q: want code, agent or manual", c.Host.Layout)
	}
	if strings.HasPrefix(c.Dotfiles.Repo, "-") {
		s.errorf("dotfiles.repo", "%q must be a git URL, not an option", c.Dotfiles.Repo)
	}
	if strings.HasPrefix(c.Dotfiles.Branch, "-") {
		s.errorf("dotfiles.branch", "%q must be a branch name, not an option", c.Dotfiles.Branch)
	}
	validateAI(&s, c)
	for _, k := range c.Undecoded {
		s.warnf(k, "unknown key")
	}
	return s.list
}

func validateForge(s *issues, path string, f Forge) {
	if !oneOf(f.Kind, "gitlab", "github") {
		s.errorf(path+".kind", "unknown kind %q: want gitlab or github", f.Kind)
	}
	if !oneOf(f.GitProtocol, "ssh", "https") {
		s.errorf(path+".git_protocol", "unknown value %q: want ssh or https", f.GitProtocol)
	}
	if u, err := url.Parse(f.URL); f.URL != "" && (err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
		s.errorf(path+".url", "%q is not an http(s) URL", f.URL)
	}
	if f.TokenCmd == "" && f.TokenEnv == "" {
		s.warnf(path, "no token_cmd or token_env: API calls will be unauthenticated and fail")
	}
	if f.CAFile != "" {
		if _, err := os.Stat(f.CAFile); err != nil {
			s.warnf(path+".ca_file", "%s is not readable: %v", f.CAFile, err)
		}
	}
	if f.SSHPort < 0 || f.SSHPort > 65535 {
		s.errorf(path+".ssh_port", "%d is not a port", f.SSHPort)
	}
	if f.MaxConcurrency < 0 {
		s.errorf(path+".max_concurrency", "%d: want a positive number", f.MaxConcurrency)
	}
}
