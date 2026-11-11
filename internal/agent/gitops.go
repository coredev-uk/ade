package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/coredev-uk/ade/internal/git"
)

// FileChange is one file of a diff summary.
type FileChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // A | M | D | R...
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// DiffSummary is what the agent changed in the worktree since the run began:
// committed or not, new files included.
type DiffSummary struct {
	Base  string       `json:"base"`
	Files []FileChange `json:"files"`
	Stat  string       `json:"stat"`
	// Patch is the full unified diff.
	Patch string `json:"patch,omitempty"`
}

// Empty reports no change.
func (d DiffSummary) Empty() bool { return len(d.Files) == 0 }

// Added and Removed total the lines.
func (d DiffSummary) Added() (n int) {
	for _, f := range d.Files {
		n += f.Added
	}
	return
}

func (d DiffSummary) Removed() (n int) {
	for _, f := range d.Files {
		n += f.Removed
	}
	return
}

// gitIn runs git in dir with extra environment; stdout only.
func gitIn(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return git.Client{Env: env}.Run(ctx, dir, args...)
}

// worktreeDiff diffs the working tree against base through a temporary copy
// of the index, so untracked files show up and the user's staging area is
// never touched.
func worktreeDiff(ctx context.Context, dir, base string, patch bool) (DiffSummary, error) {
	idx, err := gitIn(ctx, dir, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return DiffSummary{}, err
	}
	idxPath := strings.TrimSpace(idx)
	if !filepath.IsAbs(idxPath) {
		idxPath = filepath.Join(dir, idxPath)
	}
	tmp, err := os.CreateTemp("", "ade-agent-index-*")
	if err != nil {
		return DiffSummary{}, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if b, err := os.ReadFile(idxPath); err == nil {
		if err := os.WriteFile(tmp.Name(), b, 0o600); err != nil {
			return DiffSummary{}, err
		}
	}
	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	if _, err := gitIn(ctx, dir, env, "add", "-A"); err != nil {
		return DiffSummary{}, err
	}
	d := DiffSummary{Base: base}
	names, err := gitIn(ctx, dir, env, "diff", "--cached", "--no-ext-diff", "--name-status", base)
	if err != nil {
		return d, err
	}
	nums, err := gitIn(ctx, dir, env, "diff", "--cached", "--no-ext-diff", "--numstat", base)
	if err != nil {
		return d, err
	}
	counts := map[string][2]int{}
	for ln := range strings.SplitSeq(strings.TrimSpace(nums), "\n") {
		f := strings.Split(ln, "\t")
		if len(f) != 3 {
			continue
		}
		var a, r int
		fmt.Sscanf(f[0], "%d", &a) //nolint:errcheck // "-" for binary files stays 0
		fmt.Sscanf(f[1], "%d", &r) //nolint:errcheck
		counts[f[2]] = [2]int{a, r}
	}
	for ln := range strings.SplitSeq(strings.TrimSpace(names), "\n") {
		f := strings.Split(ln, "\t")
		if len(f) < 2 {
			continue
		}
		path := f[len(f)-1]
		c := counts[path]
		d.Files = append(d.Files, FileChange{Path: path, Status: string(f[0][0]), Added: c[0], Removed: c[1]})
	}
	if d.Stat, err = gitIn(ctx, dir, env, "diff", "--cached", "--no-ext-diff", "--no-color", "--stat", base); err != nil {
		return d, err
	}
	if patch {
		if d.Patch, err = gitIn(ctx, dir, env, "diff", "--cached", "--no-ext-diff", "--no-color", "--no-textconv", base); err != nil {
			return d, err
		}
	}
	return d, nil
}

func (d DiffSummary) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Diff summary\n\nBase `%s`: %d files, +%d −%d\n\n", short(d.Base), len(d.Files), d.Added(), d.Removed())
	for _, f := range d.Files {
		fmt.Fprintf(&b, "- %s %s (+%d −%d)\n", f.Status, f.Path, f.Added, f.Removed)
	}
	fmt.Fprintf(&b, "\n```\n%s```\n", d.Stat)
	return b.String()
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// head is the worktree's HEAD commit.
func head(ctx context.Context, dir string) (string, error) {
	out, err := gitIn(ctx, dir, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// checkWorktree insists the tree is a linked git worktree on the branch start
// work made for the item: an agent never works in a bare repo, in a baseline
// `main/` tree or in a checkout somebody else is using.
func checkWorktree(ctx context.Context, dir, branch string) error {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return fmt.Errorf("%s is not a git worktree (start work creates one: ade start)", dir)
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a repository checkout, not a start-work worktree", dir)
	}
	cur, err := gitIn(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if cur = strings.TrimSpace(cur); branch != "" && cur != branch {
		return fmt.Errorf("worktree %s is on %q, not the item's branch %q", dir, cur, branch)
	}
	return nil
}

// Author is the synthetic commit author of an agent: the configured git
// user, marked as the agent's.
type Author struct{ Name, Email string }

// AuthorFromGit derives the author from git config user.name / user.email,
// falling back to a neutral identity when none is set.
func AuthorFromGit(ctx context.Context, dir string) Author {
	get := func(key string) string {
		out, err := gitIn(ctx, dir, nil, "config", "--get", key)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	name, email := get("user.name"), get("user.email")
	a := Author{Name: "ade agent", Email: "ade-agent@localhost"}
	if name != "" {
		a.Name = name + " (ade agent)"
	}
	if email != "" {
		a.Email = email
	}
	return a
}

// commitAll stages everything and commits it as author, keeping the user as
// committer. It returns the new SHA, or "" when there was nothing to commit.
func commitAll(ctx context.Context, dir string, a Author, message string) (string, error) {
	if _, err := gitIn(ctx, dir, nil, "add", "-A"); err != nil {
		return "", err
	}
	if out, err := gitIn(ctx, dir, nil, "status", "--porcelain"); err != nil {
		return "", err
	} else if strings.TrimSpace(out) == "" {
		return "", nil
	}
	env := []string{"GIT_AUTHOR_NAME=" + a.Name, "GIT_AUTHOR_EMAIL=" + a.Email}
	// A committer identity the machine may not have: reuse the author's email
	// and the configured name so commit never fails on "Please tell me who you are".
	if _, err := gitIn(ctx, dir, nil, "config", "--get", "user.name"); err != nil {
		env = append(env, "GIT_COMMITTER_NAME="+a.Name, "GIT_COMMITTER_EMAIL="+a.Email)
	}
	// Signing is off: the author is synthetic, and a signing key may need an
	// agent or a pinentry that a push from a script or a dialog cannot reach.
	if _, err := gitIn(ctx, dir, env, "-c", "commit.gpgsign=false", "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return head(ctx, dir)
}

// pushBranch pushes HEAD to origin/<branch> with the client's credentials.
func pushBranch(ctx context.Context, c git.Client, dir, branch string) error {
	_, err := c.Run(ctx, dir, "push", "origin", "HEAD:refs/heads/"+branch)
	return err
}

// LazygitCmd is lazygit on the worktree. Inside zellij it opens a floating
// pane (the run list stays usable); elsewhere it takes over the terminal.
func LazygitCmd(worktree string, inZellij bool) *exec.Cmd {
	if inZellij {
		return exec.Command("zellij", "run", "--floating", "--close-on-exit", "--name", "ade agent review", "--cwd", worktree, "--", "lazygit")
	}
	cmd := exec.Command("lazygit", "-p", worktree)
	cmd.Dir = worktree
	return cmd
}

var errNoChanges = errors.New("the agent made no changes")
