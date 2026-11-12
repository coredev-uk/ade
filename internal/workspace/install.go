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
// trees the plan resolved. The workspace's own tree comes first when own is true.
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
	if own {
		add(loc.Repo, loc.Tree)
	}
	if p == nil {
		return out
	}
	for _, in := range p.Instances {
		add(in.Node.Repo, in.Tree.Path)
	}
	for _, repo := range slices.Sorted(maps.Keys(p.Trees)) {
		add(repo, p.Trees[repo].Path)
	}
	return out
}

// installKey identifies what a successful install covers: the commands and the
// worktree's creation (its .git file), so a worktree recreated at the same
// path installs again.
func installKey(t InstallTarget) string {
	h := sha256.New()
	for _, c := range t.Commands {
		io.WriteString(h, c) //nolint:errcheck // hash writes cannot fail
		h.Write([]byte{0})
	}
	if fi, err := os.Stat(filepath.Join(t.Tree, ".git")); err == nil {
		fmt.Fprintf(h, "%d", fi.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Manager) installMarker(t InstallTarget) string {
	return filepath.Join(m.StateDir, "install", slugOf(t.Tree))
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
// marker, so the next Install tries again. After a success the marker makes
// later calls no-ops until the commands change (or Force).
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
			return res
		}
	}
	if err := m.markInstalled(t); err != nil {
		res.Outcome, res.Detail = InstallFailed, "record the install: "+err.Error()
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

// WaitInstalled blocks until every target's install marker is current or ctx
// ends. onWait is called once with the targets still pending when the first
// check fails.
func (m *Manager) WaitInstalled(ctx context.Context, targets []InstallTarget, poll time.Duration, onWait func(pending []InstallTarget)) error {
	if poll <= 0 {
		poll = installPoll
	}
	announced := false
	for {
		var pending []InstallTarget
		for _, t := range targets {
			if !m.InstallDone(t) {
				pending = append(pending, t)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		if !announced && onWait != nil {
			announced = true
			onWait(pending)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
