package startwork

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
)

// Forge is what start work needs of a forge: the Phase 1 reads and the
// Phase 3 writes.
type Forge interface {
	forge.Forge
	forge.IssueWriter
}

// GitFunc returns a git client authorised for the origin of the bare clone at
// bare (project.Service.GitClient). A nil GitFunc runs git without credentials.
type GitFunc func(ctx context.Context, forgeName, bare string) (git.Client, error)

// WorkspaceRequest asks the host to open a workspace tab on a worktree.
type WorkspaceRequest struct {
	Project   string
	Repo      string
	Worktree  string
	Title     string
	ItemTrees map[string]string // repo → worktree of this work item in every target
	// Layout is the tab layout ("" for the default).
	Layout string
	// Item is the work item as it stands after start work: the cache has not
	// seen the new child issue and MR yet, so they are added here. Forge is the
	// config forge it lives on.
	Item  domain.WorkItem
	Forge string
}

// Env carries what start work acts through.
type Env struct {
	App   *app.App
	Forge Forge
	Git   GitFunc
	// Workspace opens the zellij tab (or VS Code window) once the worktree
	// exists. Nil skips the step with a note.
	Workspace func(ctx context.Context, req WorkspaceRequest) error
	// Actor is the audit actor ("user" by default).
	Actor string
}

func (e *Env) gitClient(ctx context.Context, m *config.Manifest, bare string) (git.Client, error) {
	if e.Git == nil {
		return git.Client{}, nil
	}
	return e.Git(ctx, m.Forge, bare)
}

func (e *Env) bare(m *config.Manifest, repo string) string {
	return filepath.Join(e.App.Root, m.Name, repo, ".bare")
}

// LoadChildBody reads conventions.child_body (relative to a local manifest).
// An absent setting or an unreadable source (remote manifest) yields "" and a
// note, so the default body is used.
func LoadChildBody(m *config.Manifest) (body, note string) {
	rel := m.Conventions.ChildBody
	if rel == "" {
		return "", ""
	}
	src := m.Source
	if src == "" || strings.Contains(src, "://") || strings.HasPrefix(src, "git+") {
		return "", fmt.Sprintf("conventions.child_body %q is not readable for a remote manifest: using the default child body", rel)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(src), rel))
	if err != nil {
		return "", fmt.Sprintf("conventions.child_body: %v: using the default child body", err)
	}
	return string(b), ""
}

// Discover looks for what already exists for each chosen repo: the child
// (linked, or named by a "Parent:" line), its branch, its MR, the local
// worktree and the MR template. This is what makes a re-run a no-op. Browse
// reads nothing from the forge.
func (e *Env) Discover(ctx context.Context, item domain.WorkItem, m *config.Manifest, ch Choices) (map[string]Found, error) {
	out := map[string]Found{}
	if ch.OnlyChild {
		ch.Child, ch.Mode = ChildAlways, ModeBranch
	}
	for _, name := range ch.Repos {
		if _, done := out[name]; done {
			continue
		}
		t, err := resolveTarget(m, name)
		if err != nil {
			return nil, err
		}
		f, err := e.discoverTarget(ctx, item, m, ch, t)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = f
	}
	return out, nil
}

func (e *Env) discoverTarget(ctx context.Context, item domain.WorkItem, m *config.Manifest, ch Choices, t Target) (Found, error) {
	var f Found
	bare := e.bare(m, t.Name)
	f.Cloned = !t.Tracker && exists(filepath.Join(bare, "HEAD"))
	if ch.Mode == ModeBrowse {
		f.DefaultBranch = e.localDefault(ctx, m, t, bare, f.Cloned)
		f.Worktree = e.treeOf(ctx, bare, f.Cloned, f.DefaultBranch)
		return f, nil
	}
	def, err := e.Forge.DefaultBranch(ctx, t.Project)
	if err != nil {
		return f, fmt.Errorf("default branch of %s: %w", t.Project, err)
	}
	f.DefaultBranch = def

	work := item.Ref
	if useChild(m, ch, t) {
		child, linked, warn, err := e.findChild(ctx, item.Issue, t.Project)
		if err != nil {
			return f, err
		}
		if warn != "" {
			f.Warnings = append(f.Warnings, warn)
		}
		if child != nil {
			f.Child, f.Linked, work = child, linked, child.Ref
		}
	}
	if err := e.findBranchAndMR(ctx, item, m, ch, t, work, &f); err != nil {
		return f, err
	}
	f.Worktree = e.treeOf(ctx, bare, f.Cloned, f.Branch)
	if ch.Mode == ModeDraft && f.MR == nil {
		if err := e.findTemplate(ctx, m, ch, t, &f); err != nil {
			return f, err
		}
		e.findReviewers(ctx, m, t, &f)
	}
	return f, nil
}

// findReviewers reads who conventions.mr.reviewer_group stands for. A group
// that cannot be read is a warning, not an error: the MR opens without
// reviewers rather than not at all.
func (e *Env) findReviewers(ctx context.Context, m *config.Manifest, t Target, f *Found) {
	group := m.Conventions.MR.ReviewerGroup
	if group == "" {
		return
	}
	reader, ok := e.Forge.(forge.ReviewerGroupReader)
	if !ok {
		// A forge that requests a team by name (GitHub) needs the team in the
		// repository's own organisation.
		org, _, _ := strings.Cut(group, "/")
		owner, _, _ := strings.Cut(t.Project, "/")
		if !strings.Contains(group, "/") || !strings.EqualFold(org, owner) {
			f.Warnings = append(f.Warnings, fmt.Sprintf("%s: reviewer group %s is not a team of %s: the MR is opened without reviewers", t.Project, group, owner))
			return
		}
		f.ReviewerTeam = true
		return
	}
	members, err := reader.ReviewerGroupMembers(ctx, group)
	switch {
	case err != nil:
		f.Warnings = append(f.Warnings, fmt.Sprintf("%s: reviewer group %s could not be read (%v): the MR is opened without reviewers", t.Project, group, err))
	case len(members) == 0:
		f.Warnings = append(f.Warnings, fmt.Sprintf("%s: reviewer group %s has no active members: the MR is opened without reviewers", t.Project, group))
	default:
		f.ReviewerMembers = members
	}
}

func (e *Env) localDefault(ctx context.Context, m *config.Manifest, t Target, bare string, cloned bool) string {
	if cloned {
		if def, err := (git.Client{}).DefaultBranch(ctx, bare); err == nil && def != "" {
			return def
		}
	}
	if d := m.Repos[t.Name].DefaultBranch; d != "" {
		return d
	}
	return "main"
}

// treeOf returns the path of the worktree checked out on branch, if any.
func (e *Env) treeOf(ctx context.Context, bare string, cloned bool, branch string) string {
	if !cloned || branch == "" {
		return ""
	}
	trees, err := (git.Client{}).Worktrees(ctx, bare)
	if err != nil {
		return ""
	}
	for _, tr := range trees {
		if tr.Branch == branch {
			return tr.Path
		}
	}
	return ""
}

// findChild finds the open child of tracker in project: first through its
// links (any relates_to/sub-issue link into the project) and the forge's
// hierarchy, then through a "Parent:" line in the project's open issues, which
// catches a child whose link write never happened. linked says the chosen
// child carries a child link.
func (e *Env) findChild(ctx context.Context, tracker forge.Issue, project string) (child *forge.Issue, linked bool, warn string, err error) {
	links, err := e.Forge.Links(ctx, tracker.Ref)
	if err != nil {
		return nil, false, "", fmt.Errorf("links of %s: %w", tracker.Ref, err)
	}
	var refs []forge.Ref
	isLinked := map[forge.Ref]bool{}
	for _, l := range links {
		if domain.IsChildLink(l) && l.Ref.Project == project {
			refs = append(refs, l.Ref)
			isLinked[l.Ref] = true
		}
	}
	if pr, ok := e.Forge.(forge.Parenter); ok {
		// Where the instance has no hierarchy this is an error to ignore.
		if kids, herr := pr.Children(ctx, tracker.Ref); herr == nil {
			for _, k := range kids {
				if k.Project == project && !slices.Contains(refs, k) {
					refs = append(refs, k)
				}
			}
		} else if ctx.Err() != nil {
			return nil, false, "", ctx.Err()
		}
	}
	var found []forge.Issue
	for _, r := range refs {
		is, err := e.Forge.Issue(ctx, r)
		if err != nil {
			return nil, false, "", err
		}
		if is.State == "opened" {
			found = append(found, is)
		}
	}
	if len(found) == 0 {
		open, err := e.Forge.ListIssues(ctx, forge.IssueQuery{Project: project})
		if err != nil {
			return nil, false, "", fmt.Errorf("issues of %s: %w", project, err)
		}
		for _, is := range open {
			if slices.Contains(domain.ParentRefs(is), tracker.Ref) {
				found = append(found, is)
			}
		}
	}
	if len(found) == 0 {
		return nil, false, "", nil
	}
	slices.SortFunc(found, func(a, b forge.Issue) int { return a.Ref.IID - b.Ref.IID })
	if len(found) > 1 {
		warn = fmt.Sprintf("%s has %d open children in %s: using %s", tracker.Ref, len(found), project, found[0].Ref)
	}
	return &found[0], isLinked[found[0].Ref], warn, nil
}

// findBranchAndMR fills Branch and MR. A child names its branch by number
// (the slug may have changed with the title); without a child only the exact
// computed name counts, since another issue of that number could own a prefix.
func (e *Env) findBranchAndMR(ctx context.Context, item domain.WorkItem, m *config.Manifest, ch Choices, t Target, work forge.Ref, f *Found) error {
	withChild := useChild(m, ch, t)
	if pin := ch.Branches[t.Name]; pin != "" {
		return e.findPinnedBranch(ctx, t, pin, f)
	}
	switch {
	case withChild && f.Child != nil:
		bs, err := e.Forge.Branches(ctx, t.Project, fmt.Sprintf("%d-", f.Child.Ref.IID))
		if err != nil {
			return err
		}
		slices.Sort(bs)
		if len(bs) > 0 {
			f.Branch = bs[0]
		}
		if len(bs) > 1 {
			// Several attempts at the child: resume the one whose MR is open.
			if err := e.preferOpenBranch(ctx, t, work, bs, f); err != nil {
				return err
			}
		}
	case !withChild:
		title := childSpec(PlanInput{Manifest: m, Item: item}, t).Title
		name := BranchName(m, title, item, item.Ref.IID)
		bs, err := e.Forge.Branches(ctx, t.Project, name)
		if err != nil {
			return err
		}
		if slices.Contains(bs, name) {
			f.Branch = name
		}
	}
	if f.Branch != "" {
		mrs, err := e.Forge.ListMRs(ctx, forge.MRQuery{Project: t.Project, SourceBranch: f.Branch, State: "all"})
		if err != nil {
			return err
		}
		f.MR = pickMR(mrs)
	}
	if f.MR == nil && (f.Child != nil || !withChild) {
		// The branch may be gone (merged and deleted) while its MR remains.
		mrs, err := e.Forge.MRsForIssue(ctx, work)
		if err != nil {
			return err
		}
		mrs = slices.DeleteFunc(mrs, func(mr forge.MR) bool { return mr.Ref.Project != t.Project })
		if f.MR = pickMR(mrs); f.MR != nil && f.Branch == "" {
			f.Branch = f.MR.SourceBranch
		}
	}
	return nil
}

// preferOpenBranch picks, among the child's branches bs, the one with an open
// MR (the least advanced when several are open), so an issue that has had
// several MRs resumes the live one, not whichever branch sorts first.
func (e *Env) preferOpenBranch(ctx context.Context, t Target, work forge.Ref, bs []string, f *Found) error {
	mrs, err := e.Forge.MRsForIssue(ctx, work)
	if err != nil {
		return err
	}
	mrs = slices.DeleteFunc(mrs, func(mr forge.MR) bool {
		return mr.Ref.Project != t.Project || mr.State != "opened" || !slices.Contains(bs, mr.SourceBranch)
	})
	wi := domain.WorkItem{MRs: mrs}
	if mr := wi.FocusMR(); mr != nil {
		f.Branch, f.MR = mr.SourceBranch, mr
	}
	return nil
}

// findPinnedBranch looks up the branch Choices.Branches names, by exact name,
// and its MR. No other branch or MR of the issue stands in for it: the pin
// asks for a separate set of changes.
func (e *Env) findPinnedBranch(ctx context.Context, t Target, name string, f *Found) error {
	bs, err := e.Forge.Branches(ctx, t.Project, name)
	if err != nil {
		return err
	}
	if !slices.Contains(bs, name) {
		return nil
	}
	f.Branch = name
	mrs, err := e.Forge.ListMRs(ctx, forge.MRQuery{Project: t.Project, SourceBranch: name, State: "all"})
	if err != nil {
		return err
	}
	f.MR = pickMR(mrs)
	return nil
}

// pickMR prefers an open MR, then a merged one; a closed MR does not block a
// new one.
func pickMR(mrs []forge.MR) *forge.MR {
	for _, state := range []string{"opened", "merged"} {
		for _, mr := range mrs {
			if mr.State == state {
				return &mr
			}
		}
	}
	return nil
}

// GitHub keeps the pull request template in one well-known file, and further
// named ones in a directory. The single file is offered as "Default" so the
// selection below treats both forges alike.
const (
	gitHubTemplateFile = ".github/pull_request_template.md"
	gitHubTemplateDir  = ".github/PULL_REQUEST_TEMPLATE"
)

// TemplateNames lists the MR templates project offers at ref, by name, and
// maps a name to its file path. GitLab keeps them in a directory; GitHub in
// one well-known file (named "Default") and a directory.
func TemplateNames(ctx context.Context, fg Forge, project, ref string) (names []string, pathOf func(string) string, err error) {
	if fg.Kind() != "github" {
		files, err := fg.ListFiles(ctx, project, ref, TemplateDir)
		for _, p := range files {
			if strings.HasSuffix(p, ".md") {
				names = append(names, strings.TrimSuffix(path.Base(p), ".md"))
			}
		}
		return names, func(n string) string { return TemplateDir + "/" + n + ".md" }, err
	}
	files, err := fg.ListFiles(ctx, project, ref, gitHubTemplateDir)
	if err != nil {
		return nil, nil, err
	}
	single := false
	if _, err := fg.ReadFile(ctx, project, ref, gitHubTemplateFile); err == nil {
		single = true
		names = append(names, "Default")
	} else if !errors.Is(err, forge.ErrNotFound) {
		return nil, nil, err
	}
	for _, p := range files {
		if strings.HasSuffix(p, ".md") {
			names = append(names, strings.TrimSuffix(path.Base(p), ".md"))
		}
	}
	return names, func(n string) string {
		if single && n == "Default" {
			return gitHubTemplateFile
		}
		return gitHubTemplateDir + "/" + n + ".md"
	}, nil
}

// findTemplate reads the chosen MR template from the repo's default branch.
func (e *Env) findTemplate(ctx context.Context, m *config.Manifest, ch Choices, t Target, f *Found) error {
	pick, err := PickTemplate(ctx, e.Forge, m, t.Project, f.DefaultBranch, ch.Template)
	if err != nil {
		return err
	}
	f.Templates = pick.Names
	f.Warnings = append(f.Warnings, pick.Warnings...)
	f.TemplateName, f.TemplateBody = pick.Name, pick.Body
	return nil
}

// Template is the MR template PickTemplate chose; Name and Body are empty when
// none applies.
type Template struct {
	Names    []string // every template the repo offers, sorted
	Name     string
	Body     string
	Warnings []string
}

// PickTemplate reads the MR template of project from ref: want (a flag), else
// conventions.mr.template, else "Default", else the repo's only template.
func PickTemplate(ctx context.Context, fg Forge, m *config.Manifest, project, ref, want string) (Template, error) {
	var out Template
	names, pathOf, err := TemplateNames(ctx, fg, project, ref)
	if err != nil {
		return out, fmt.Errorf("templates of %s: %w", project, err)
	}
	out.Names = append(out.Names, names...)
	slices.Sort(out.Names)
	name, explicit := want, want != ""
	if name == "" {
		name = m.Conventions.MR.Template
		explicit = name != ""
	}
	if name == "" {
		name = "Default"
	}
	if !slices.Contains(out.Names, name) {
		switch {
		case explicit:
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: MR template %q not found (have: %s)", project, name, strings.Join(out.Names, ", ")))
			return out, nil
		case len(out.Names) == 1:
			name = out.Names[0]
		case len(out.Names) > 1:
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: several MR templates (%s): choose one with --template", project, strings.Join(out.Names, ", ")))
			return out, nil
		default:
			return out, nil
		}
	}
	body, err := fg.ReadFile(ctx, project, ref, pathOf(name))
	if errors.Is(err, forge.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("template %s: %w", name, err)
	}
	out.Name, out.Body = name, string(body)
	return out, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
