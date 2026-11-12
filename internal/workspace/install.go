package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/devshell"
)

// InstallTarget is one worktree and the install commands of its repo
// (`[repos.<r>.hooks] install`).
type InstallTarget struct {
	Repo     string
	Tree     string
	Commands []string
	Devshell devshell.Shell
}

// InstallOutcome says what Install did to one target.
type InstallOutcome string

const (
	InstallRan      InstallOutcome = "installed"
	InstallUpToDate InstallOutcome = "up to date"
	InstallFailed   InstallOutcome = "failed"
)

// InstallResult is Install's report for one target.
type InstallResult struct {
	Repo    string         `json:"repo"`
	Tree    string         `json:"tree"`
	Outcome InstallOutcome `json:"outcome"`
	Detail  string         `json:"detail,omitempty"`
}

// InstallOptions tunes Install.
type InstallOptions struct {
	// Force runs the commands although the marker says they already succeeded.
	Force bool
	// Out receives the commands' output (stdout and stderr) and one line per command.
	Out io.Writer
	// Audit, when set, is called after each command with its error (nil on success).
	Audit func(t InstallTarget, command string, err error)
}

// InstallTargets lists the worktrees of a plan whose repos declare install
// commands, requirements first: the trees its instances run in, then the other
// trees the plan resolved. The workspace's own tree comes last when own is
// true, so a long install there does not delay the services that need only
// their own trees.
func (m *Manager) InstallTargets(loc Location, p *Plan, own bool) []InstallTarget {
	var out []InstallTarget
	seen := map[string]bool{}
	add := func(repo, tree string) {
		cmds := loc.Manifest.Repos[repo].Hooks.Install
		if repo == "" || tree == "" || seen[tree] || len(cmds) == 0 {
			return
		}
		seen[tree] = true
		out = append(out, InstallTarget{
			Repo: repo, Tree: tree, Commands: cmds,
			Devshell: m.Devshell.Detect(tree, loc.Manifest.Repos[repo].Devshell),
		})
	}
	addOther := func(repo, tree string) {
		if !own || tree != loc.Tree {
			add(repo, tree)
		}
	}
	if p != nil {
		for _, in := range p.Instances {
			addOther(in.Node.Repo, in.Tree.Path)
		}
		for _, repo := range slices.Sorted(maps.Keys(p.Trees)) {
			addOther(repo, p.Trees[repo].Path)
		}
	}
	if own {
		add(loc.Repo, loc.Tree)
	}
	return out
}

// lockfiles are the dependency locks at a worktree's root whose content is part
// of the install key: a pull that changes one installs again.
var lockfiles = []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "go.sum", "Cargo.lock", "poetry.lock", "uv.lock", "flake.lock"}

// installKey identifies what a successful install covers: the commands, the
// content of the worktree's lockfiles, and the worktree's creation (its .git
// file), so a worktree recreated at the same path installs again.
func installKey(t InstallTarget) string {
	h := sha256.New()
	for _, c := range t.Commands {
		io.WriteString(h, c) //nolint:errcheck // hash writes cannot fail
		h.Write([]byte{0})
	}
	for _, name := range lockfiles {
		if sum, ok := fileSum(filepath.Join(t.Tree, name)); ok {
			fmt.Fprintf(h, "%s=%s\n", name, sum)
		}
	}
	if fi, err := os.Stat(filepath.Join(t.Tree, ".git")); err == nil {
		fmt.Fprintf(h, "%d", fi.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fileSum(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func (m *Manager) installMarker(t InstallTarget) string {
	return filepath.Join(m.StateDir, "install", slugOf(t.Tree))
}

// installFailedMarker holds what the last failed install of the target said,
// for the panes waiting for it.
func (m *Manager) installFailedMarker(t InstallTarget) string { return m.installMarker(t) + ".failed" }

// installFailure returns the detail of the target's failed install, when it
// was for the current commands and lockfiles.
func (m *Manager) installFailure(t InstallTarget) (string, bool) {
	data, err := os.ReadFile(m.installFailedMarker(t))
	if err != nil {
		return "", false
	}
	key, detail, _ := strings.Cut(string(data), "\n")
	if key != installKey(t) {
		return "", false
	}
	return strings.TrimSpace(detail), true
}

func (m *Manager) markInstallFailed(t InstallTarget, detail string) {
	path := m.installFailedMarker(t)
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, []byte(installKey(t)+"\n"+detail+"\n"), 0o600)
	}
}

// InstallDone reports whether the target's commands already succeeded in its
// worktree. A target without commands has nothing to wait for.
func (m *Manager) InstallDone(t InstallTarget) bool {
	if len(t.Commands) == 0 {
		return true
	}
	data, err := os.ReadFile(m.installMarker(t))
	return err == nil && strings.TrimSpace(string(data)) == installKey(t)
}

func (m *Manager) markInstalled(t InstallTarget) error {
	path := m.installMarker(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(installKey(t)+"\n"), 0o600)
}

// Install runs the target's commands in its worktree, inside the repo's dev
// shell, one after the other; the first failure stops the run and leaves no
// marker, so the next Install tries again, but a failure marker with the detail
// (<marker>.failed) that WaitInstalled reports at once. The next attempt
// clears it. After a success the marker makes later calls no-ops until the
// commands or the lockfiles change (or Force).
func (m *Manager) Install(ctx context.Context, t InstallTarget, o InstallOptions) InstallResult {
	res := InstallResult{Repo: t.Repo, Tree: t.Tree, Outcome: InstallUpToDate}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	unlock, err := m.lock(ctx, "install@"+t.Tree)
	if err != nil {
		return InstallResult{Repo: t.Repo, Tree: t.Tree, Outcome: InstallFailed, Detail: err.Error()}
	}
	defer unlock()
	if !o.Force && m.InstallDone(t) {
		return res
	}
	_ = os.Remove(m.installFailedMarker(t))
	if err := os.Remove(m.installMarker(t)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return InstallResult{Repo: t.Repo, Tree: t.Tree, Outcome: InstallFailed, Detail: err.Error()}
	}
	for _, c := range t.Commands {
		fmt.Fprintf(out, "$ %s\n", c)
		err := runInstallCommand(ctx, t, c, out)
		if o.Audit != nil {
			o.Audit(t, c, err)
		}
		if err != nil {
			res.Outcome, res.Detail = InstallFailed, fmt.Sprintf("%q: %v", c, err)
			m.markInstallFailed(t, res.Detail)
			return res
		}
	}
	if err := m.markInstalled(t); err != nil {
		res.Outcome, res.Detail = InstallFailed, "record the install: "+err.Error()
		m.markInstallFailed(t, res.Detail)
		return res
	}
	res.Outcome = InstallRan
	return res
}

func runInstallCommand(ctx context.Context, t InstallTarget, command string, out io.Writer) error {
	argv := t.Devshell.Wrap([]string{"sh", "-c", command})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = t.Tree
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// installPoll is how often WaitInstalled looks for the markers.
const installPoll = 500 * time.Millisecond

// InstallWait tunes WaitInstalled.
type InstallWait struct {
	// Poll is how often the markers are looked at (default half a second).
	Poll time.Duration
	// Timeout bounds the wait (default DefaultWaitTimeout).
	Timeout time.Duration
	// Every is how often Waiting repeats (default DefaultWaitReport).
	Every time.Duration
	// Waiting is called with the targets still pending when the first check
	// fails (elapsed 0), and again every Every with the time waited so far.
	Waiting func(pending []InstallTarget, elapsed time.Duration)
}

// WaitInstalled blocks until every target's install marker is current. It
// ends with an error when ctx ends, when a pending target's last install
// failed (the detail is in the error), and when w.Timeout passes.
func (m *Manager) WaitInstalled(ctx context.Context, targets []InstallTarget, w InstallWait) error {
	if w.Poll <= 0 {
		w.Poll = installPoll
	}
	pace := newPacer(w.Every, w.Timeout)
	for {
		var pending []InstallTarget
		for _, t := range targets {
			if m.InstallDone(t) {
				continue
			}
			if detail, failed := m.installFailure(t); failed {
				return fmt.Errorf("install of %s (%s) failed: %s", t.Repo, t.Tree, detail)
			}
			pending = append(pending, t)
		}
		if len(pending) == 0 {
			return nil
		}
		if pace.expired() {
			return fmt.Errorf("install of %s (%s) did not finish within %s", pending[0].Repo, pending[0].Tree, pace.timeout)
		}
		if elapsed, due := pace.report(); due && w.Waiting != nil {
			w.Waiting(pending, elapsed)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.Poll):
		}
	}
}
