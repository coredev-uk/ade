// Package prompt answers `ade prompt`: which ticket the current directory's
// worktree belongs to and how many items wait on you, for a shell prompt. It
// reads local files and the cache only (no network, no git process, no
// migrations, no audit write) so it stays within a few milliseconds.
package prompt

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/store"
)

// Cache states reported in Info.Cache.
const (
	CacheOK      = "ok"
	CacheMissing = "missing" // no cache.db yet: ade has not synced on this machine
	CacheError   = "error"   // cache.db exists but could not be read
)

// Info is what the prompt shows. Outside an ADE tree only InTree=false is set.
type Info struct {
	InTree  bool   `json:"in_tree"`
	Project string `json:"project,omitempty"`
	Repo    string `json:"repo,omitempty"`
	// Worktree is the worktree root; empty at the repo or project level.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// Parent is the tracker issue's iid from the worktree's ade.parent; 0 for
	// a baseline tree or one ADE did not make.
	Parent int `json:"parent,omitempty"`
	// Prefix is the expanded commit prefix (ade.commitPrefix), e.g. GL-412.
	Prefix string `json:"prefix,omitempty"`
	// Ticket is Prefix, else "#<Parent>", else empty.
	Ticket string `json:"ticket,omitempty"`
	// Cache says whether Attention could be read; Attention is nil unless it is "ok".
	Cache     string               `json:"cache,omitempty"`
	Attention *forgesync.Attention `json:"attention,omitempty"`
}

// Options steers Read.
type Options struct {
	// Root is the resolved projects root (paths.root).
	Root string
	// CacheDB is the path of cache.db.
	CacheDB string
	// Path is the directory to describe, normally the working directory.
	Path string
	// AllProjects counts attention across every project; otherwise only the
	// directory's own.
	AllProjects bool
}

// Read describes o.Path. A directory outside an ADE tree, or one that cannot
// be resolved, is Info{InTree: false}; an unreadable cache only leaves
// Attention nil (see Info.Cache). It never fails.
func Read(o Options) Info {
	project, repo, tree, ok := locate(o.Root, o.Path)
	if !ok {
		return Info{}
	}
	info := Info{InTree: true, Project: project, Repo: repo, Worktree: tree}
	if tree != "" {
		info.Branch, info.Parent, info.Prefix = readWorktree(tree)
		switch {
		case info.Prefix != "":
			info.Ticket = info.Prefix
		case info.Parent > 0:
			info.Ticket = "#" + strconv.Itoa(info.Parent)
		}
	}
	scope := project
	if o.AllProjects {
		scope = ""
	}
	info.Cache, info.Attention = readAttention(o.CacheDB, scope)
	return info
}

// locate maps path to <root>/<project>/<repo>[/<worktree>]: a repo counts only
// when it has the bare clone ADE makes (<repo>/.bare). tree is the worktree
// root and is empty when path is at the repo or project level.
func locate(root, path string) (project, repo, tree string, ok bool) {
	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", "", false
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return "", "", "", false
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return "", "", "", false
	}
	repoDir := filepath.Join(absRoot, parts[0], parts[1])
	if fi, err := os.Stat(filepath.Join(repoDir, ".bare")); err != nil || !fi.IsDir() {
		return "", "", "", false
	}
	if len(parts) >= 3 && parts[2] != ".bare" {
		tree = filepath.Join(repoDir, parts[2])
	}
	return parts[0], parts[1], tree, true
}

// readWorktree reads the branch and the ade.* keys of a worktree from its git
// files. A bare clone's linked worktree has a .git file pointing at
// <bare>/worktrees/<name>, where HEAD and config.worktree live.
func readWorktree(tree string) (branch string, parent int, prefix string) {
	gitDir := resolveGitDir(tree)
	if gitDir == "" {
		return "", 0, ""
	}
	if head, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); ok {
			branch = ref
		}
	}
	// config.worktree is where `git config --worktree` writes; a plain config
	// is read second for a tree made without extensions.worktreeConfig.
	for _, name := range []string{"config.worktree", "config"} {
		data, err := os.ReadFile(filepath.Join(gitDir, name))
		if err != nil {
			continue
		}
		kv := gitConfigSection(data, "ade")
		if parent == 0 {
			parent, _ = strconv.Atoi(kv["parent"])
		}
		if prefix == "" {
			prefix = kv["commitprefix"]
		}
	}
	return branch, max(parent, 0), prefix
}

func resolveGitDir(tree string) string {
	dot := filepath.Join(tree, ".git")
	fi, err := os.Stat(dot)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return dot
	}
	data, err := os.ReadFile(dot)
	if err != nil {
		return ""
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(tree, dir)
	}
	return dir
}

// gitConfigSection returns the keys (lower-cased) of the first-level
// [section] blocks in a git config file. It reads what `git config` writes:
// `key = value`, with a value optionally in double quotes.
func gitConfigSection(data []byte, section string) map[string]string {
	out := map[string]string{}
	in := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[':
			name, _, _ := strings.Cut(strings.Trim(strings.SplitN(line, "]", 2)[0], "["), " ")
			in = strings.EqualFold(name, section) && !strings.Contains(line, `"`)
		case in:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			out[strings.ToLower(strings.TrimSpace(k))] = unquoteGitValue(strings.TrimSpace(v))
		}
	}
	return out
}

func unquoteGitValue(v string) string {
	if len(v) < 2 || v[0] != '"' {
		return v
	}
	var b strings.Builder
	for i := 1; i < len(v); i++ {
		switch c := v[i]; {
		case c == '"':
			return b.String()
		case c == '\\' && i+1 < len(v):
			i++
			switch v[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(v[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func readAttention(dbPath, project string) (string, *forgesync.Attention) {
	st, err := store.OpenReadOnly(dbPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return CacheMissing, nil
		}
		return CacheError, nil
	}
	defer st.Close()
	return attentionFrom(st.DB, project)
}

func attentionFrom(db *sql.DB, project string) (string, *forgesync.Attention) {
	a, err := forgesync.CountAttention(db, project)
	if err != nil {
		// A cache from before the first sync has no tables, and one written
		// by an older ade lacks the attention column until any newer ade
		// command opens it (migrations are the writers' job, not the prompt's).
		if msg := err.Error(); strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column") {
			return CacheMissing, nil
		}
		return CacheError, nil
	}
	return CacheOK, &a
}

// DefaultFormat is what `ade prompt` prints without --format.
const DefaultFormat = "{ticket} {attention}"

// Placeholders are the names Render accepts.
var Placeholders = []string{
	"ticket", "attention", "attention_n", "items", "reviews", "project", "repo", "branch", "worktree",
}

// CheckFormat reports the first placeholder of format Render would not know.
func CheckFormat(format string) error {
	for rest := format; ; {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			return nil
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return fmt.Errorf("unclosed { in format %q", format)
		}
		name := rest[i+1 : i+j]
		known := false
		for _, p := range Placeholders {
			known = known || p == name
		}
		if !known {
			return fmt.Errorf("unknown placeholder {%s}: want one of %s", name, "{"+strings.Join(Placeholders, "}, {")+"}")
		}
		rest = rest[i+j+1:]
	}
}

// Render fills format from info. {attention} is "!N" and {attention_n} is N,
// both empty when nothing waits unless showZero. A tree outside ADE renders
// as the empty string, and runs of blanks left by empty values collapse, so a
// segment never leaves stray spaces in the prompt.
func Render(format string, info Info, showZero bool) string {
	if !info.InTree {
		return ""
	}
	n := strconv.Itoa
	var attention, attentionN, items, reviews string
	if a := info.Attention; a != nil {
		items, reviews = n(a.Items), n(a.Reviews)
		if a.Total > 0 || showZero {
			attentionN, attention = n(a.Total), "!"+n(a.Total)
		}
	}
	r := strings.NewReplacer(
		"{ticket}", info.Ticket, "{attention}", attention, "{attention_n}", attentionN,
		"{items}", items, "{reviews}", reviews, "{project}", info.Project, "{repo}", info.Repo,
		"{branch}", info.Branch, "{worktree}", info.Worktree,
	)
	return strings.Join(strings.Fields(r.Replace(format)), " ")
}
