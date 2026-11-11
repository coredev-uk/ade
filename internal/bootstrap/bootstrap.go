// Package bootstrap generates a team manifest (*.ade.toml) by inspecting an
// existing forge group, so a team lead does not write it by hand. It reads
// repositories, labels, iterations, issue weights, merge request templates
// and each repo's root files, and only states what it found: whatever the
// forge cannot settle is left as a comment.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/startwork"
)

// Forge is what bootstrapping reads and (for publishing) writes.
type Forge interface {
	forge.Forge
	forge.IssueWriter
	forge.Discoverer
	forge.FileWriter
}

// Chooser asks the person at the terminal to settle an ambiguity. A nil
// Chooser means unattended: ambiguities that have a sensible default take it,
// the others are errors naming the flag that resolves them.
type Chooser interface {
	Choose(question string, options []string, def string) (string, error)
}

// Options are the answers of `ade project bootstrap`.
type Options struct {
	ForgeName string // the manifest's forge: a key into the user config
	Group     string // GitLab group, or GitHub organisation or user
	Tracker   string // tracker repository (path or unique name); detected when empty
	Name      string // project name; the group's last segment when empty
	Match     string // glob on repository names (or paths) to include; all when empty
	Now       func() time.Time
	Chooser   Chooser
}

// issueWindow is how far back the issues that teach label conventions reach:
// what the team does now, not what it did years ago.
const issueWindow = 365 * 24 * time.Hour

var (
	identName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	unsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
	unsafeName  = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// Discover inspects the group and returns the draft manifest. It reads from
// the forge and writes nothing.
func Discover(ctx context.Context, f Forge, o Options) (*Draft, error) {
	if o.ForgeName == "" || o.Group == "" {
		return nil, errors.New("bootstrap needs a forge name and a group")
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	d := &Draft{Group: o.Group}

	repos, err := d.listRepos(ctx, f, o)
	if err != nil {
		return nil, err
	}
	tracker, issues, err := d.chooseTracker(ctx, f, o, repos, now())
	if err != nil {
		return nil, err
	}
	d.Tracker = tracker

	defined, err := f.Labels(ctx, tracker.Path)
	if err != nil {
		return nil, fmt.Errorf("labels of %s: %w", tracker.Path, err)
	}
	used, weighted := labelUse(issues)
	facets := DetectFacets(defined, used)

	m := &d.Manifest
	m.Name = o.Name
	if m.Name == "" {
		m.Name = unsafeName.ReplaceAllString(path.Base(o.Group), "-")
	}
	m.Forge = o.ForgeName
	m.Tracker.Project = tracker.Path
	d.fillLabels(facets, used)
	d.TrackerWhy = d.trackerWhy(o, issues)

	if err := d.fillPlanning(ctx, f, o, facets, weighted); err != nil {
		return nil, err
	}
	var code []forge.Repo
	for _, r := range repos {
		if r.Path != tracker.Path {
			code = append(code, r)
		}
	}
	d.fillRepos(ctx, f, tracker, code, facets)
	m.Conventions.ChildIssue = "auto"
	d.TOML = d.Render()
	return d, nil
}

// Check parses the rendered text back and validates it as a user's config
// would: the same path every manifest takes. cfg may be nil to skip the
// checks against the user config.
func (d *Draft) Check(cfg *config.Config) ([]config.Issue, error) {
	m, err := config.ParseManifest([]byte(d.TOML), "generated manifest")
	if err != nil {
		return nil, err
	}
	return m.Validate(cfg), nil
}

func (d *Draft) listRepos(ctx context.Context, f Forge, o Options) ([]forge.Repo, error) {
	all, err := f.Repos(ctx, o.Group)
	if err != nil {
		return nil, fmt.Errorf("repositories of %s: %w", o.Group, err)
	}
	slices.SortFunc(all, func(a, b forge.Repo) int { return strings.Compare(a.Path, b.Path) })
	var live []forge.Repo
	for _, r := range all {
		if r.Archived {
			d.Skipped = append(d.Skipped, r.Path+" (archived)")
			continue
		}
		live = append(live, r)
	}
	if o.Match == "" {
		return d.requireRepos(live, o)
	}
	if _, err := path.Match(o.Match, ""); err != nil {
		return nil, fmt.Errorf("--match %q: %w", o.Match, err)
	}
	var matched []forge.Repo
	unmatched := 0
	for _, r := range live {
		if ok, _ := path.Match(o.Match, r.Name); ok {
			matched = append(matched, r)
		} else if ok, _ := path.Match(o.Match, r.Path); ok {
			matched = append(matched, r)
		} else {
			unmatched++
		}
	}
	if unmatched > 0 {
		d.Skipped = append(d.Skipped, fmt.Sprintf("%s not matching --match %s", plural(unmatched, "repository", "repositories"), o.Match))
	}
	return d.requireRepos(matched, o)
}

func (d *Draft) requireRepos(repos []forge.Repo, o Options) ([]forge.Repo, error) {
	if len(repos) == 0 {
		return nil, fmt.Errorf("%s has no repositories to include (archived ones are skipped%s)", o.Group, matchHint(o.Match))
	}
	return repos, nil
}

func matchHint(match string) string {
	if match == "" {
		return ""
	}
	return ", and --match " + match + " keeps none"
}

// chooseTracker resolves --tracker, or scores every repo by the issues that
// carry app-like labels and asks when that does not settle it. It returns
// the tracker's recent issues, which the rest of the discovery reads.
func (d *Draft) chooseTracker(ctx context.Context, f Forge, o Options, repos []forge.Repo, now time.Time) (forge.Repo, []forge.Issue, error) {
	list := func(r forge.Repo) ([]forge.Issue, error) {
		is, err := f.ListIssues(ctx, forge.IssueQuery{Project: r.Path, State: "all", UpdatedAfter: now.Add(-issueWindow)})
		if err != nil {
			return nil, fmt.Errorf("issues of %s: %w", r.Path, err)
		}
		return is, nil
	}
	if o.Tracker != "" {
		r, err := findRepo(repos, o.Tracker)
		if err != nil {
			return forge.Repo{}, nil, err
		}
		is, err := list(r)
		return r, is, err
	}

	cands := make([]Candidate, len(repos))
	issues := make([][]forge.Issue, len(repos))
	for i, r := range repos {
		is, err := list(r)
		if err != nil {
			return forge.Repo{}, nil, err
		}
		issues[i] = is
		cands[i] = Candidate{Repo: r, NameHit: trackerName.MatchString(r.Name)}
		for _, is := range is {
			if slices.ContainsFunc(is.Labels, IsAppLabel) {
				cands[i].AppIssues++
			}
		}
	}
	win, tied := PickTracker(cands)
	if win >= 0 {
		return repos[win], issues[win], nil
	}
	if len(tied) == 0 {
		for i := range repos {
			tied = append(tied, i)
		}
	}
	options := make([]string, len(tied))
	for i, idx := range tied {
		options[i] = fmt.Sprintf("%s  (%d issues with app labels)", repos[idx].Path, cands[idx].AppIssues)
	}
	if o.Chooser == nil {
		return forge.Repo{}, nil, fmt.Errorf("cannot tell which repository is the tracker: pass --tracker (candidates: %s)", strings.Join(paths(repos, tied), ", "))
	}
	ans, err := o.Chooser.Choose("Which repository holds the planning issues (the tracker)?", options, options[0])
	if err != nil {
		return forge.Repo{}, nil, err
	}
	i := slices.Index(options, ans)
	if i < 0 {
		return forge.Repo{}, nil, fmt.Errorf("tracker %q is not one of the candidates", ans)
	}
	return repos[tied[i]], issues[tied[i]], nil
}

func paths(repos []forge.Repo, idx []int) []string {
	out := make([]string, len(idx))
	for i, x := range idx {
		out[i] = repos[x].Path
	}
	return out
}

func (d *Draft) trackerWhy(o Options, issues []forge.Issue) string {
	if o.Tracker != "" {
		return "given with --tracker"
	}
	n := 0
	for _, is := range issues {
		if slices.ContainsFunc(is.Labels, IsAppLabel) {
			n++
		}
	}
	switch {
	case n > 0 && trackerName.MatchString(d.Tracker.Name):
		return fmt.Sprintf("named like a tracker; %d of its recent issues carry app labels", n)
	case n > 0:
		return fmt.Sprintf("%d of its recent issues carry app labels, more than any other repo", n)
	}
	return "named like a tracker"
}

// labelUse counts, per label, the issues carrying it, and reports whether any
// issue has a weight field value.
func labelUse(issues []forge.Issue) (used map[string]int, weighted bool) {
	used = map[string]int{}
	for _, is := range issues {
		for _, l := range is.Labels {
			used[l]++
		}
		weighted = weighted || is.Weight != nil
	}
	return used, weighted
}

func (d *Draft) fillLabels(f Facets, used map[string]int) {
	l := &d.Manifest.Labels
	d.TypeWhy = "no type labels found (e.g. type::bug, kind/feature): add the pattern your team uses"
	d.AppWhy = "no app labels found (e.g. app::api, area/web): add the pattern your team uses"
	if f.Type != nil {
		l.Type.Pattern = f.Type.Pattern
		d.TypeWhy = facetWhy(f.Type)
	}
	if f.App != nil {
		l.App.Pattern = f.App.Pattern
		d.AppWhy = facetWhy(f.App)
	}
	if f.Workflow == nil {
		return
	}
	wf := MapWorkflow(f.Workflow.Values, workflowUse(f.Workflow, used))
	l.Workflow = config.WorkflowFacet{Pattern: f.Workflow.Pattern, Stage: wf.Stage, Sync: "auto", Done: wf.Done}
	d.WorkflowWhy = append(d.WorkflowWhy, "values found: "+strings.Join(f.Workflow.Values, ", ")+".")
	if wf.Blocked != "" {
		d.WorkflowWhy = append(d.WorkflowWhy, fmt.Sprintf("%q is left unmapped: ADE reads it as blocked.", wf.Blocked))
	}
	if len(wf.Unmapped) > 0 {
		d.WorkflowWhy = append(d.WorkflowWhy, "not mapped to a stage (no name match): "+strings.Join(wf.Unmapped, ", ")+".")
	}
	if len(wf.Extra) > 0 {
		d.WorkflowWhy = append(d.WorkflowWhy, "also fit a stage already taken by a busier value: "+strings.Join(wf.Extra, ", ")+".")
	}
	if len(wf.Stage) == 0 {
		d.WorkflowWhy = append(d.WorkflowWhy, "no value looks like in progress, in review or done: add stage = { ... } by hand.")
	}
}

// workflowUse is the issue count of each workflow value.
func workflowUse(f *Facet, used map[string]int) map[string]int {
	out := map[string]int{}
	for _, v := range f.Values {
		out[v] = used[strings.TrimSuffix(f.Pattern, "{value}")+v]
	}
	return out
}

func facetWhy(f *Facet) string {
	return fmt.Sprintf("values: %s; on %d recent issues", strings.Join(capValues(f.Values, 8), ", "), f.Issues)
}

func capValues(vs []string, n int) []string {
	if len(vs) <= n {
		return vs
	}
	return append(slices.Clone(vs[:n]), fmt.Sprintf("... %d more", len(vs)-n))
}

// fillPlanning chooses the iteration and weight sources from what the forge
// offers and what the tracker's issues carry.
func (d *Draft) fillPlanning(ctx context.Context, f Forge, o Options, facets Facets, weighted bool) error {
	var caps forge.Caps
	if f.Kind() == "gitlab" {
		c, err := f.Capabilities(ctx, o.Group)
		switch {
		case errors.Is(err, forge.ErrNotFound):
			d.Notes = append(d.Notes, o.Group+" is not a group: iterations and weights are read as on a free account")
		case err != nil:
			return fmt.Errorf("capabilities of %s: %w", o.Group, err)
		default:
			caps = c
		}
	}

	done := false
	if caps.Iterations {
		var err error
		if done, err = d.gitlabIterations(ctx, f, o); err != nil {
			return err
		}
	}
	if !done {
		if err := d.milestoneIterations(ctx, f, o, caps.Iterations); err != nil {
			return err
		}
	}
	d.fillWeight(f.Kind(), caps, facets, weighted)
	return nil
}

// milestoneIterations uses milestones as sprints: a GitLab group's, or the
// tracker repo's on GitHub (milestones are per repo there).
func (d *Draft) milestoneIterations(ctx context.Context, f Forge, o Options, licensed bool) error {
	scope := o.Group
	if f.Kind() == "github" {
		scope = d.Tracker.Path
	}
	ms, err := f.Milestones(ctx, scope)
	if err != nil && !errors.Is(err, forge.ErrNotFound) {
		return fmt.Errorf("milestones of %s: %w", scope, err)
	}
	if len(ms) == 0 {
		d.Manifest.Tracker.Iterations = config.IterationsSource{Source: "auto"}
		d.IterationsWhy = []string{"no iterations or milestones found yet: auto uses whichever appears first."}
		return nil
	}
	d.Manifest.Tracker.Iterations = config.IterationsSource{Source: "milestones"}
	d.IterationsWhy = []string{milestoneWhy(ms, licensed)}
	return nil
}

// fillWeight picks the weight source: GitLab's field when issues use it,
// else weight labels, else none.
func (d *Draft) fillWeight(kind string, caps forge.Caps, facets Facets, weighted bool) {
	tr := &d.Manifest.Tracker
	switch {
	case caps.Weight && weighted:
		tr.Weight = config.WeightSource{Source: "gitlab"}
		d.WeightWhy = []string{"the issues carry GitLab's weight field."}
	case facets.Weight != nil:
		tr.Weight = config.WeightSource{Source: "label", Pattern: facets.Weight.Pattern}
		d.WeightWhy = []string{fmt.Sprintf("weights are labels (%s); values: %s.", facets.Weight.Pattern, strings.Join(facets.Weight.Values, ", "))}
		if caps.Weight {
			d.WeightWhy = append(d.WeightWhy, "the weight field is licensed but no recent issue uses it.")
		}
	default:
		tr.Weight = config.WeightSource{Source: "none"}
		d.WeightWhy = []string{"no weight field in use and no weight::N labels found: issues show as unweighted."}
		if kind == "github" {
			d.WeightWhy = append(d.WeightWhy, `GitHub projects can carry a number field: weight = { source = "github-field", field = "Estimate" }`)
		}
	}
}

func milestoneWhy(ms []forge.Milestone, licensed bool) string {
	last := ms[len(ms)-1]
	why := fmt.Sprintf("%d milestones used as sprints (latest %q", len(ms), last.Title)
	if !last.Start.IsZero() && !last.Due.IsZero() {
		why += fmt.Sprintf(", %s to %s", last.Start, last.Due)
	}
	why += ")."
	if !licensed {
		why += " No iterations on this forge."
	}
	return why
}

// gitlabIterations sets the iteration source from GitLab iterations and their
// cadences. It reports false when the group licenses iterations but has none.
func (d *Draft) gitlabIterations(ctx context.Context, f Forge, o Options) (bool, error) {
	all, err := f.Iterations(ctx, o.Group, "")
	if errors.Is(err, forge.ErrUnavailable) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("iterations of %s: %w", o.Group, err)
	}
	if len(all) == 0 {
		return false, nil
	}
	cadences, err := f.Cadences(ctx, o.Group)
	if err != nil && !errors.Is(err, forge.ErrUnavailable) {
		return false, fmt.Errorf("cadences of %s: %w", o.Group, err)
	}
	src := config.IterationsSource{Source: "gitlab-iterations"}
	why := []string{fmt.Sprintf("%d iterations found.", len(all))}
	switch len(cadences) {
	case 0:
	case 1:
		why = append(why, cadenceNote(cadences[0]))
	default:
		chosen, others, err := d.pickCadence(ctx, f, o, cadences)
		if err != nil {
			return false, err
		}
		src.Cadence = chosen.Title
		d.Cadence = chosen.Title
		why = append(why, cadenceNote(chosen), fmt.Sprintf("the group has %d cadences: this one is used, not %s.", len(cadences), strings.Join(others, ", ")))
	}
	d.Manifest.Tracker.Iterations = src
	d.IterationsWhy = why
	return true, nil
}

func cadenceNote(c forge.Cadence) string {
	s := fmt.Sprintf("cadence %q", c.Title)
	if c.Weeks > 0 {
		s += fmt.Sprintf(": %d-week sprints", c.Weeks)
	}
	if !c.Start.IsZero() {
		s += fmt.Sprintf(" from %s", c.Start)
	}
	return s + "."
}

// pickCadence chooses among several cadences: the one with the most
// iterations by default, or the person's answer.
func (d *Draft) pickCadence(ctx context.Context, f Forge, o Options, cadences []forge.Cadence) (chosen forge.Cadence, others []string, err error) {
	counts := make([]int, len(cadences))
	best := 0
	for i, c := range cadences {
		its, err := f.Iterations(ctx, o.Group, c.Title)
		if err != nil {
			return chosen, nil, fmt.Errorf("iterations of cadence %q: %w", c.Title, err)
		}
		counts[i] = len(its)
		if counts[i] > counts[best] {
			best = i
		}
	}
	pick := best
	if o.Chooser != nil {
		options := make([]string, len(cadences))
		for i, c := range cadences {
			options[i] = fmt.Sprintf("%s  (%d iterations)", c.Title, counts[i])
		}
		ans, err := o.Chooser.Choose("Which iteration cadence are the sprints?", options, options[best])
		if err != nil {
			return chosen, nil, err
		}
		if pick = slices.Index(options, ans); pick < 0 {
			return chosen, nil, fmt.Errorf("cadence %q is not one of the options", ans)
		}
	}
	for i, c := range cadences {
		if i != pick {
			others = append(others, fmt.Sprintf("%q", c.Title))
		}
	}
	return cadences[pick], others, nil
}

// fillRepos reads each code repo's root and templates and adds it to the
// manifest. A repo that cannot be read still gets its [repos] table; the
// problem is a note.
func (d *Draft) fillRepos(ctx context.Context, f Forge, tracker forge.Repo, code []forge.Repo, facets Facets) {
	all := append([]forge.Repo{tracker}, code...)
	keys, prefix := repoKeys(all)
	d.KeyPrefix = prefix

	var appValues []string
	if facets.App != nil {
		appValues = facets.App.Values
	}
	names := make([]string, len(code))
	for i, r := range code {
		names[i] = keys[r.Path]
	}
	apps, matched := MatchApps(names, appValues)

	m := &d.Manifest
	m.Repos = map[string]config.Repo{}
	for _, r := range code {
		key := keys[r.Path]
		rf := RepoFinding{Key: key, Path: r.Path, DefaultBranch: r.DefaultBranch, Apps: apps[key], AppMatched: matched[key]}
		d.inspectRepo(ctx, f, &rf)
		d.Repos = append(d.Repos, rf)

		repo := config.Repo{Path: r.Path, App: rf.Apps}
		if len(rf.Detected.Tasks) > 0 {
			repo.Tasks = map[string]config.Task{}
			for _, t := range rf.Detected.Tasks {
				repo.Tasks[t.Name] = t.Task
			}
		}
		m.Repos[key] = repo
	}
	slices.SortFunc(d.Repos, func(a, b RepoFinding) int { return strings.Compare(a.Key, b.Key) })

	var dev []string
	for _, r := range d.Repos {
		for _, t := range r.Detected.Dev {
			dev = append(dev, r.Key+"."+t)
		}
	}
	if len(dev) > 0 {
		m.Groups = map[string][]string{"dev": dev}
		m.Workspaces = map[string]config.Workspace{"default": {Layout: "code", Run: "dev"}}
	}
}

// inspectRepo reads the root files and MR templates of one repo.
func (d *Draft) inspectRepo(ctx context.Context, f Forge, rf *RepoFinding) {
	note := func(format string, args ...any) {
		d.Notes = append(d.Notes, rf.Key+": "+fmt.Sprintf(format, args...))
	}
	branch := rf.DefaultBranch
	if branch == "" {
		b, err := f.DefaultBranch(ctx, rf.Path)
		if err != nil {
			note("no default branch (empty repository?): nothing detected")
			return
		}
		branch = b
		rf.DefaultBranch = b
	}
	listing, err := f.ListFiles(ctx, rf.Path, branch, "")
	if err != nil {
		note("cannot list the root: %v", err)
	}
	present := presentRoot(listing)
	content := map[string]string{}
	for _, name := range wantedContent(present) {
		b, err := f.ReadFile(ctx, rf.Path, branch, name)
		if err != nil {
			note("cannot read %s: %v", name, err)
			continue
		}
		content[name] = string(b)
	}
	rf.Detected = DetectTasks(present, content)

	names, _, err := startwork.TemplateNames(ctx, f, rf.Path, branch)
	if err != nil {
		note("cannot list MR templates: %v", err)
	}
	slices.Sort(names)
	rf.Templates = names
	if len(names) > 0 {
		d.Notes = append(d.Notes, fmt.Sprintf("%s: MR template(s) %s", rf.Key, strings.Join(names, ", ")))
	}
}

// repoKeys names the repos for [repos.<key>]: the repo name made safe, minus
// a prefix every included repo shares ("ade-sandbox-"). Equal names get their
// parent group in front.
func repoKeys(repos []forge.Repo) (keys map[string]string, prefix string) {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}
	prefix = sharedPrefix(names)
	keys = map[string]string{}
	taken := map[string]bool{}
	for _, r := range repos {
		k := safeKey(strings.TrimPrefix(r.Name, prefix))
		if taken[k] {
			k = safeKey(path.Base(path.Dir(r.Path)) + "-" + k)
		}
		for n := 2; taken[k]; n++ {
			k = fmt.Sprintf("%s-%d", safeKey(strings.TrimPrefix(r.Name, prefix)), n)
		}
		taken[k], keys[r.Path] = true, k
	}
	return keys, prefix
}

// sharedPrefix returns the longest prefix of every name that ends in "-" and
// leaves each name something after it; "" when there are fewer than two names.
func sharedPrefix(names []string) string {
	if len(names) < 2 {
		return ""
	}
	p := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, p) {
			p = p[:len(p)-1]
		}
	}
	i := strings.LastIndex(p, "-")
	if i < 0 {
		return ""
	}
	p = p[:i+1]
	for _, n := range names {
		if rest := strings.TrimPrefix(n, p); rest == "" || !identName.MatchString(rest) {
			return ""
		}
	}
	return p
}

func safeKey(s string) string {
	k := strings.Trim(unsafeChars.ReplaceAllString(s, "-"), "-_")
	if k == "" || k == "services" {
		k += "repo"
	}
	return k
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
