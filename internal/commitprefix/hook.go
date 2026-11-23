package commitprefix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/coredev-uk/ade/internal/git"
)

// hookName is the git hook that prefixes a commit message; hooksDirName is the
// directory under the worktree's git dir that ADE owns and points the
// worktree's core.hooksPath at.
const (
	hookName     = "commit-msg"
	hooksDirName = "ade-hooks"
	hooksPathKey = "core.hooksPath"
	// Marker opens the second line of every file ADE writes into its hooks
	// directory, so that removal and reinstall touch only those.
	marker = "# ade-managed:"
)

// hookScript is the commit-msg hook. It reads the worktree's own git config
// (ade.commitPrefix, ade.target), so the file is the same for every worktree
// and survives the prefix changing. It prefixes only the MR's first commit
// (no commits ahead of ade.target), never twice, and leaves merges, reverts,
// squashed merges and autosquash messages alone.
const hookScript = `#!/bin/sh
` + marker + ` commit-prefix hook (ade ws hook install; ade ws hook remove deletes it)
# commit-msg: prepend the worktree's ade.commitPrefix to the first commit of the MR.
msg=$1
prefix=$(git config --get ade.commitPrefix) || exit 0
[ -n "$prefix" ] || exit 0
git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1 && exit 0
cc=$(git config --get core.commentChar 2>/dev/null)
case "$cc" in ""|auto) cc='#' ;; esac
# the first line of the message proper: not blank, not a comment
first=$(CC=$cc awk 'substr($0,1,1)!=ENVIRON["CC"] && $0 !~ /^[ \t]*$/ {print; exit}' "$msg")
[ -n "$first" ] || exit 0
case "$first" in
  "$prefix"|"$prefix "*|"$prefix:"*) exit 0 ;;                                       # already prefixed
  Merge\ *|Squashed\ commit*|fixup!\ *|squash!\ *|amend!\ *|Revert\ *) exit 0 ;;     # merges, autosquash, reverts
esac
# any commit already ahead of the MR target means this is not the first one
target=$(git config --get ade.target)
if [ -n "$target" ]; then
  for ref in "$target" "origin/$target"; do
    if ahead=$(git rev-list --count "$ref..HEAD" 2>/dev/null); then
      [ "$ahead" = 0 ] || exit 0
      break
    fi
  done
fi
tmp="$msg.ade"
CC=$cc PREFIX=$prefix awk '!done && substr($0,1,1)!=ENVIRON["CC"] && $0 !~ /^[ \t]*$/ {print ENVIRON["PREFIX"] " " $0; done=1; next} {print}' "$msg" >"$tmp" \
  && cat "$tmp" >"$msg"
rm -f "$tmp"
`

// ErrNoPrefix is returned when the worktree has no commit prefix to apply.
var ErrNoPrefix = errors.New("no commit prefix is recorded for this worktree (ade.commitPrefix is unset): the manifest has no conventions.commit_prefix, or ADE did not make the worktree")

// ForeignHookError reports a commit-msg hook that is not ADE's. ADE never
// replaces or wraps it.
type ForeignHookError struct{ Path string }

func (e *ForeignHookError) Error() string {
	return "a commit-msg hook of your own is already in place at " + e.Path + ": left alone (add the prefix there, or remove it and run `ade ws hook install` again)"
}

// HookPlan is what installing the hook into one worktree does, worked out
// without touching anything.
type HookPlan struct {
	Worktree string `json:"worktree"`
	// Dir is the ADE-owned hooks directory the worktree's core.hooksPath is set
	// to (under the worktree's git dir, so it goes with the worktree).
	Dir string `json:"dir"`
	// Prev is the hooks directory git used before ADE's took over; every other
	// hook in it keeps running through a forwarding stub in Dir (Forward).
	Prev    string   `json:"prev,omitempty"`
	Forward []string `json:"forward,omitempty"`
	// Foreign is the path of a commit-msg hook that is not ADE's; the install
	// is refused while it exists.
	Foreign string `json:"foreign,omitempty"`
	// Installed: the worktree's core.hooksPath is Dir and the hook there is
	// current. Stale: ADE's hook is in Dir but differs from this version's.
	Installed bool `json:"installed"`
	Stale     bool `json:"stale,omitempty"`
	// Present: something of ADE's is in place (the worktree's core.hooksPath is
	// Dir, or the hook file is there), whatever its version.
	Present bool `json:"present,omitempty"`
}

// PlanHook works out the install for the worktree at dir (any path inside it).
func PlanHook(ctx context.Context, dir string) (HookPlan, error) {
	g := git.Client{}
	top, err := g.Run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return HookPlan{}, fmt.Errorf("%s is not inside a worktree: %s", dir, git.Reason(err))
	}
	p := HookPlan{Worktree: strings.TrimSpace(top)}
	gitDir, err := g.Run(ctx, p.Worktree, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return HookPlan{}, err
	}
	p.Dir = filepath.Join(strings.TrimSpace(gitDir), hooksDirName)

	cur, _ := g.Run(ctx, p.Worktree, "config", "--worktree", "--get", hooksPathKey)
	ours := resolveHooksPath(p.Worktree, strings.TrimSpace(cur)) == p.Dir
	p.Present = ours || managed(filepath.Join(p.Dir, hookName))
	if ours {
		body, _ := os.ReadFile(filepath.Join(p.Dir, hookName))
		p.Installed = string(body) == hookScript
		p.Stale = len(body) > 0 && !p.Installed && bytes.Contains(body, []byte(marker))
	}
	if p.Prev, err = previousHooksDir(ctx, g, p.Worktree, ours); err != nil {
		return HookPlan{}, err
	}
	// A commit-msg file of the user's in ADE's own directory counts too.
	for _, foreign := range []string{filepath.Join(p.Prev, hookName), filepath.Join(p.Dir, hookName)} {
		if isHookFile(foreign) && !managed(foreign) {
			p.Foreign = foreign
			return p, nil
		}
	}
	for _, n := range forwardable(p.Prev) {
		if own := filepath.Join(p.Dir, n); !isHookFile(own) || managed(own) {
			p.Forward = append(p.Forward, n)
		}
	}
	return p, nil
}

// previousHooksDir is the hooks directory git would use for the worktree
// without ADE's: the last core.hooksPath outside the worktree's own scope when
// ADE's directory is the worktree's (a reinstall), else what git resolves now.
func previousHooksDir(ctx context.Context, g git.Client, worktree string, ours bool) (string, error) {
	if !ours {
		out, err := g.Run(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}
	var last string
	out, _ := g.Run(ctx, worktree, "config", "--show-scope", "--get-all", hooksPathKey)
	for line := range strings.SplitSeq(out, "\n") {
		scope, v, ok := strings.Cut(line, "\t")
		if ok && scope != "worktree" && strings.TrimSpace(v) != "" {
			last = strings.TrimSpace(v)
		}
	}
	if last != "" {
		return resolveHooksPath(worktree, last), nil
	}
	common, err := g.Run(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(strings.TrimSpace(common), "hooks"), nil
}

// resolveHooksPath makes a core.hooksPath value absolute the way git reads it:
// "~/" is the home directory, a relative path is relative to the worktree.
func resolveHooksPath(worktree, v string) string {
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "~/"):
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, v[2:])
		}
	case !filepath.IsAbs(v):
		return filepath.Join(worktree, v)
	}
	return filepath.Clean(v)
}

// isHookFile reports whether path is a file git could run as a hook (a hook
// that is not executable is ignored by git, but it is still somebody's).
func isHookFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// forwardable lists the hooks in dir that a forwarding stub must keep
// running: executable files, not git's *.sample files, not commit-msg.
func forwardable(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
			continue
		}
		if n := e.Name(); n != hookName && !strings.HasSuffix(n, ".sample") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// stub is a hook that runs the same hook in the directory it replaces.
func stub(target string) string {
	return "#!/bin/sh\n" + marker + " forwards to " + target + "\nexec " + shellQuote(target) + " \"$@\"\n"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Apply installs the planned hook: it writes the hook and the forwarding
// stubs into Dir and points the worktree's core.hooksPath at it
// (extensions.worktreeConfig, so no other worktree is affected). It refuses a
// plan with a Foreign hook and is idempotent.
func (p HookPlan) Apply(ctx context.Context) error {
	if p.Foreign != "" {
		return &ForeignHookError{Path: p.Foreign}
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return err
	}
	want := map[string]string{hookName: hookScript}
	for _, n := range p.Forward {
		want[n] = stub(filepath.Join(p.Prev, n))
	}
	if err := removeManaged(p.Dir, want); err != nil {
		return err
	}
	for n, body := range want {
		if err := writeIfChanged(filepath.Join(p.Dir, n), body); err != nil {
			return err
		}
	}
	g := git.Client{}
	common, err := g.Run(ctx, p.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if err := enableWorktreeConfig(ctx, g, strings.TrimSpace(common)); err != nil {
		return err
	}
	return setWorktree(ctx, g, p.Worktree, hooksPathKey, p.Dir)
}

// removeManaged deletes the ADE-written files of dir that keep is without.
func removeManaged(dir string, keep map[string]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, ok := keep[e.Name()]; ok || !managed(filepath.Join(dir, e.Name())) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// managed reports whether the file at path is one ADE wrote.
func managed(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, second, _ := strings.Cut(string(body), "\n")
	return strings.HasPrefix(second, marker)
}

func writeIfChanged(path, body string) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		if fi, err := os.Stat(path); err == nil && fi.Mode()&0o111 != 0 {
			return nil
		}
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755) // WriteFile keeps the mode of an existing file
}

// InstallHook installs the commit-msg hook into the worktree at dir, as
// PlanHook and Apply do, once the worktree has a commit prefix. It returns
// the plan it applied.
func InstallHook(ctx context.Context, dir string) (HookPlan, error) {
	p, err := PlanHook(ctx, dir)
	if err != nil {
		return p, err
	}
	if Read(ctx, p.Worktree) == "" {
		return p, ErrNoPrefix
	}
	return p, p.Apply(ctx)
}

// RemoveHook takes ADE's hook out of the worktree at dir: it unsets the
// worktree's core.hooksPath when that is ADE's directory and deletes the files
// ADE wrote there. A hook of the user's is never touched. changed is false
// when there was nothing of ADE's to remove.
func RemoveHook(ctx context.Context, dir string) (p HookPlan, changed bool, err error) {
	g := git.Client{}
	if p, err = PlanHook(ctx, dir); err != nil {
		return p, false, err
	}
	cur, _ := g.Run(ctx, p.Worktree, "config", "--worktree", "--get", hooksPathKey)
	if resolveHooksPath(p.Worktree, strings.TrimSpace(cur)) == p.Dir {
		if _, err = g.Run(ctx, p.Worktree, "config", "--worktree", "--unset-all", hooksPathKey); err != nil {
			return p, false, err
		}
		changed = true
	}
	entries, rerr := os.ReadDir(p.Dir)
	if rerr != nil {
		return p, changed, nil
	}
	for _, e := range entries {
		path := filepath.Join(p.Dir, e.Name())
		if !managed(path) {
			continue
		}
		if err = os.Remove(path); err != nil {
			return p, changed, err
		}
		changed = true
	}
	_ = os.Remove(p.Dir) // only when empty: a file of the user's stays
	return p, changed, nil
}
