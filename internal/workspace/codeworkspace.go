package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/doctor"
	"github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/records"
)

// A multi-root VS Code workspace (PLAN 6.7) shows every repo of the project in
// one window: the work item's own worktree where it has one, the repo's
// baseline otherwise. The file lives in the item's records directory, outside
// every git tree, so it never lands in a commit.

// Where a code-workspace folder comes from.
const (
	FolderItem     = "item"     // a worktree of the work item (or the worktree it was generated for)
	FolderBaseline = "baseline" // the repo's main/ worktree
)

const (
	codeWorkspaceExt = ".code-workspace"
	// folderNameMax bounds the tree name shown in a folder's label.
	folderNameMax = 24
	// slugMax bounds the title part of the file name.
	slugMax = 40
)

// CodeFolder is one root of the generated workspace.
type CodeFolder struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source string `json:"source"`
	Repo   string `json:"repo"`
}

// CodeInput says what to generate.
type CodeInput struct {
	Root      string           // projects root
	Manifest  *config.Manifest // the project
	Item      *ItemRef         // the work item; nil: no item, every other repo is a baseline
	Loc       *Location        // anchor worktree; nil: the item's own (or its repo's baseline)
	Dir       string           // directory the file is written to
	Workspace string           // [workspaces.<name>] whose run group becomes the tasks; default "default"
	Ade       string           // the ade executable the tasks run
}

// CodeResult is what was written.
type CodeResult struct {
	Path    string       `json:"path"`
	Folders []CodeFolder `json:"folders"`
	Tasks   int          `json:"tasks"`
}

// CodeWorkspace writes <Dir>/<name>.code-workspace and returns what it holds.
// The file is ADE's: folders, settings keys it sets and ADE's tasks are
// regenerated, while other top-level keys (extensions, launch) and the user's
// other settings and tasks survive.
func (m *Manager) CodeWorkspace(ctx context.Context, in CodeInput) (CodeResult, error) {
	mf := in.Manifest
	projectDir := filepath.Join(in.Root, mf.Name)

	var (
		anchor    Location
		itemTrees map[string]string
	)
	if in.Item != nil {
		t, err := TargetForItem(ctx, in.Root, mf, in.Item.Item)
		if err != nil {
			return CodeResult{}, err
		}
		itemTrees = t.ItemTrees
		if in.Loc != nil {
			anchor = *in.Loc
		} else if anchor, err = Locate(in.Root, t.Worktree); err != nil {
			return CodeResult{}, err
		}
	} else {
		if in.Loc == nil {
			return CodeResult{}, errors.New("a code workspace needs a work item or a worktree")
		}
		anchor = *in.Loc
	}

	folders, err := codeFolders(ctx, projectDir, mf, anchor, itemTrees)
	if err != nil {
		return CodeResult{}, err
	}

	var tasks []VSTask
	if len(mf.Workspaces) > 0 {
		plan, err := m.Plan(ctx, anchor, PlanOptions{Workspace: in.Workspace, Trees: TreeOptions{ItemTrees: itemTrees}})
		if err != nil {
			return CodeResult{}, err
		}
		tasks = BuildTasks(plan, in.Ade, appendTreeArgs(nil, TreeOptions{ItemTrees: itemTrees}))
	}

	name := codeFileName(ctx, in.Item, anchor)
	path := filepath.Join(in.Dir, name+codeWorkspaceExt)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return CodeResult{}, err
	}
	data, err := MergeCodeWorkspace(existing, folders, tasks)
	if err != nil {
		return CodeResult{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := os.MkdirAll(in.Dir, 0o755); err != nil {
		return CodeResult{}, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return CodeResult{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return CodeResult{}, err
	}
	return CodeResult{Path: path, Folders: folders, Tasks: len(tasks)}, nil
}

// codeFolders lists every repo of the project, the anchor's repo first and the
// rest in manifest order (sorted names).
func codeFolders(ctx context.Context, projectDir string, mf *config.Manifest, anchor Location, itemTrees map[string]string) ([]CodeFolder, error) {
	repos := mf.RepoNames()
	if i := slices.Index(repos, anchor.Repo); i > 0 {
		repos = slices.Insert(slices.Delete(repos, i, i+1), 0, anchor.Repo)
	}
	var out []CodeFolder
	for _, repo := range repos {
		base, baseErr := baseline(ctx, projectDir, mf, repo)
		f := CodeFolder{Repo: repo}
		switch {
		case repo == anchor.Repo:
			f.Path = anchor.Tree
			f.Source = FolderItem
			if baseErr == nil && filepath.Clean(base) == filepath.Clean(f.Path) {
				f.Source = FolderBaseline
			}
		case itemTrees[repo] != "":
			f.Path, f.Source = itemTrees[repo], FolderItem
		case baseErr != nil:
			return nil, baseErr
		default:
			f.Path, f.Source = base, FolderBaseline
		}
		f.Name = repo + " (" + truncateName(filepath.Base(f.Path), folderNameMax) + ")"
		out = append(out, f)
	}
	return out, nil
}

// truncateName cuts s to at most n runes, ending in … when it was cut.
func truncateName(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// codeFileName is the stable file name (without extension): the parent issue's
// iid and the slug of its title, or project and branch when no item is known.
func codeFileName(ctx context.Context, item *ItemRef, anchor Location) string {
	if item != nil {
		name := fmt.Sprint(item.Item.Ref.IID)
		if s := fileSlug(item.Item.Issue.Title, slugMax); s != "" {
			name += "-" + s
		}
		return name
	}
	branch := BranchOf(ctx, anchor)
	if branch == "" {
		branch = filepath.Base(anchor.Tree)
	}
	return fileSlug(anchor.Project+"-"+branch, 2*slugMax)
}

// fileSlug lowercases s into [a-z0-9-] words of at most max bytes, cut at a
// word boundary when there is one; "workspace" when nothing is left.
func fileSlug(s string, max int) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > max {
		out = out[:max]
		if i := strings.LastIndexByte(out, '-'); i > max/2 {
			out = out[:i]
		}
		out = strings.TrimRight(out, "-")
	}
	return out
}

// codeSettings are the workspace settings ADE sets: bare repos and their
// worktree metadata stay out of the explorer, search and file watcher; git
// only looks for repositories in the folders themselves (the project's repo
// directories hold a bare clone, not a checkout, so parents never apply); and
// the terminal hands Ctrl+P/B/J to zellij and the shell, as `ade doctor
// --vscode` recommends.
func codeSettings() map[string]any {
	return map[string]any{
		"files.exclude":                           map[string]bool{"**/.bare": true},
		"search.exclude":                          map[string]bool{"**/.bare": true},
		"files.watcherExclude":                    map[string]bool{"**/.bare/**": true},
		"git.autoRepositoryDetection":             "subFolders",
		"git.openRepositoryInParentFolders":       "never",
		"terminal.integrated.commandsToSkipShell": doctor.VSCodeSettings["terminal.integrated.commandsToSkipShell"],
	}
}

type codeFolderJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// MergeCodeWorkspace returns the .code-workspace content for folders and
// tasks on top of an existing file (nil for none). A file that is not plain
// JSON (VS Code allows comments) is an error rather than being overwritten.
func MergeCodeWorkspace(existing []byte, folders []CodeFolder, tasks []VSTask) ([]byte, error) {
	rest := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &rest); err != nil {
			return nil, fmt.Errorf("existing workspace file is not plain JSON (comments?): %w", err)
		}
	}
	roots := make([]codeFolderJSON, len(folders))
	for i, f := range folders {
		roots[i] = codeFolderJSON{Name: f.Name, Path: f.Path}
	}
	if err := setRaw(rest, "folders", roots); err != nil {
		return nil, err
	}

	settings := map[string]json.RawMessage{}
	if raw, ok := rest["settings"]; ok {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, fmt.Errorf("existing workspace file: settings: %w", err)
		}
	}
	for k, v := range codeSettings() {
		if err := setRaw(settings, k, v); err != nil {
			return nil, err
		}
	}
	if err := setRaw(rest, "settings", settings); err != nil {
		return nil, err
	}

	merged, err := MergeTasks(rest["tasks"], tasks)
	if err != nil {
		return nil, fmt.Errorf("existing workspace file: tasks: %w", err)
	}
	rest["tasks"] = merged

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rest); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func setRaw(m map[string]json.RawMessage, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m[key] = b
	return nil
}

// FindItem returns the cached work item that ref names: its tracker issue, one
// of its child issues, or one of its MRs.
func FindItem(items []ItemRef, ref forge.Ref) (ItemRef, bool) {
	same := func(r forge.Ref) bool {
		return r.IID == ref.IID && r.Kind == ref.Kind && strings.EqualFold(r.Project, ref.Project)
	}
	for _, it := range items {
		if same(it.Item.Ref) {
			return it, true
		}
	}
	for _, it := range items {
		for _, c := range it.Item.Children {
			if same(c.Ref) {
				return it, true
			}
		}
		for _, mr := range it.Item.MRs {
			if same(mr.Ref) {
				return it, true
			}
		}
	}
	return ItemRef{}, false
}

// CodeRequest is one `ade vscode workspace` / Home `v`.
type CodeRequest struct {
	Item      *ItemRef // the work item, when known
	Worktree  string   // anchor worktree: the plan runs from it (with an item, one of its trees)
	Workspace string
	Open      bool
}

// CodeOutcome is a generated workspace and whether VS Code was started.
type CodeOutcome struct {
	CodeResult
	Opened bool   `json:"opened"`
	Hint   string `json:"hint,omitempty"` // why VS Code was not started
}

// CodeWorkspaceFor generates the workspace file for req in the records
// directory (the item's, or <records>/workspaces/<project> without an item),
// optionally opens it, and audits the action.
func CodeWorkspaceFor(ctx context.Context, a *app.App, req CodeRequest) (CodeOutcome, error) {
	out, projectName, target, err := codeWorkspaceFor(ctx, a, req)
	entry := audit.Entry{
		Action: "workspace.vscode", Target: target, Project: projectName, Outcome: "ok",
		Params: map[string]any{"file": out.Path, "folders": len(out.Folders), "opened": out.Opened},
	}
	if err != nil {
		entry.Outcome, entry.Error = "error", err.Error()
	}
	if a.Audit != nil {
		if aerr := a.Audit.Append(entry); aerr != nil && err == nil {
			err = aerr
		}
	}
	return out, err
}

func codeWorkspaceFor(ctx context.Context, a *app.App, req CodeRequest) (out CodeOutcome, projectName, target string, err error) {
	var (
		loc  *Location
		mf   *config.Manifest
		root = a.Root
		dir  string
	)
	switch {
	case req.Item != nil:
		projectName, target = req.Item.Project, req.Item.Item.Ref.String()
		if mf, err = project.LoadManifest(filepath.Join(root, projectName)); err != nil {
			return out, projectName, target, fmt.Errorf("project %s: %w (run `ade project add`)", projectName, err)
		}
		dir = records.New(a.RecordsDir).ItemDir(req.Item.Forge, req.Item.Item.Ref)
		if req.Worktree != "" { // generated from a worktree of the item: it anchors the plan
			l, lerr := Locate(root, req.Worktree)
			if lerr != nil {
				return out, projectName, target, lerr
			}
			loc = &l
		}
	case req.Worktree != "":
		l, lerr := Locate(root, req.Worktree)
		if lerr != nil {
			return out, "", req.Worktree, lerr
		}
		loc, mf, projectName, target = &l, l.Manifest, l.Project, l.Tree
		dir = filepath.Join(a.RecordsDir, "workspaces", projectName)
	default:
		return out, "", "", errors.New("a code workspace needs a work item or a worktree")
	}
	mgr, err := NewManager(a)
	if err != nil {
		return out, projectName, target, err
	}
	ade, err := os.Executable()
	if err != nil {
		return out, projectName, target, fmt.Errorf("locate ade executable: %w", err)
	}
	res, err := mgr.CodeWorkspace(ctx, CodeInput{
		Root: root, Manifest: mf, Item: req.Item, Loc: loc, Dir: dir, Workspace: req.Workspace, Ade: ade,
	})
	if err != nil {
		return out, projectName, target, err
	}
	out.CodeResult = res
	if req.Open {
		out.Opened, out.Hint, err = OpenCodeWorkspace(ctx, res.Path)
	}
	return out, projectName, target, err
}

// OpenCodeWorkspace opens file in VS Code through the editor helper. Without
// `code` on PATH nothing is started and hint says how to open it by hand; that
// is not an error.
func OpenCodeWorkspace(ctx context.Context, file string) (opened bool, hint string, err error) {
	ed := editor.Resolve(editor.SettingCode, editor.FromOS())
	if ed.Kind != editor.Code {
		return false, "code is not on PATH: open " + file + " in VS Code", nil
	}
	argv := ed.ArgvDir(file)
	if err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run(); err != nil {
		return false, "", fmt.Errorf("code: %w", err)
	}
	return true, "", nil
}
