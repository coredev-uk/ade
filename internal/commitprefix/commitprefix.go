// Package commitprefix applies the commit prefix convention (manifest
// conventions.commit_prefix) to the things ADE knows about: it expands the
// template for a work item and keeps the result in each item worktree's git
// config, where a commit-msg hook and ADE's own commits read it. It sits
// between commitmsg (pure text) and the packages that know manifests and items.
package commitprefix

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/commitmsg"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/git"
)

// Worktree git-config keys (section ade) written for an item's worktree.
const (
	KeyParent = "ade.parent"       // the tracker issue's iid
	KeyPrefix = "ade.commitPrefix" // the expanded commit prefix; absent when none is configured
)

// ForIssues expands the manifest's commit_prefix for an item whose tracker
// issue is parent and whose child issue in the code repo is child (0: none).
// It returns "" when no prefix is configured, the template does not validate
// or parent is unknown.
func ForIssues(m *config.Manifest, parent, child int) string {
	if m == nil || m.Conventions.CommitPrefix == "" || parent <= 0 {
		return ""
	}
	p, err := commitmsg.Expand(m.Conventions.CommitPrefix, commitmsg.Vars{Parent: parent, Child: child, Project: m.Name})
	if err != nil {
		return ""
	}
	return p
}

// ChildIn is the iid of the item's child issue in the repo at repoPath (the
// forge path), 0 when there is none.
func ChildIn(wi domain.WorkItem, repoPath string) int {
	for _, c := range wi.Children {
		if strings.EqualFold(c.Ref.Project, repoPath) {
			return c.Ref.IID
		}
	}
	return 0
}

// ExpandPrefix is the prefix for work on wi in the manifest's repo named repo
// ("" when none is configured).
func ExpandPrefix(m *config.Manifest, wi domain.WorkItem, repo string) string {
	if m == nil {
		return ""
	}
	return ForIssues(m, wi.Ref.IID, ChildIn(wi, m.Repos[repo].Path))
}

// ForMR is the prefix for the MR ref when it belongs to wi: the child is the
// item's child issue in the MR's project. "" when none applies.
func ForMR(m *config.Manifest, wi domain.WorkItem, ref forge.Ref) string {
	return ForIssues(m, wi.Ref.IID, ChildIn(wi, ref.Project))
}

// Owns reports whether ref is one of wi's merge requests.
func Owns(wi domain.WorkItem, ref forge.Ref) bool {
	for _, mr := range wi.MRs {
		if strings.EqualFold(mr.Ref.Project, ref.Project) && mr.Ref.IID == ref.IID {
			return true
		}
	}
	return false
}

// WriteWorktreeConfig records an item's worktree in its own git config
// (extensions.worktreeConfig, so the values are per worktree, also in a bare
// repo's worktrees): ade.parent is the tracker iid (when known, > 0) and
// ade.commitPrefix the expanded prefix; an empty prefix removes a stale one.
// It is idempotent and cheap to call each time a workspace opens.
func WriteWorktreeConfig(ctx context.Context, worktreeDir string, parent int, prefix string) error {
	g := git.Client{}
	common, err := g.Run(ctx, worktreeDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	common = strings.TrimSpace(common)
	if err := enableWorktreeConfig(ctx, g, common); err != nil {
		return err
	}
	if parent > 0 {
		if err := setWorktree(ctx, g, worktreeDir, KeyParent, strconv.Itoa(parent)); err != nil {
			return err
		}
	}
	return setWorktree(ctx, g, worktreeDir, KeyPrefix, prefix)
}

// enableWorktreeConfig turns on extensions.worktreeConfig for the repository
// at common. A bare repository keeps core.bare = true in its shared config,
// which would make every linked worktree "bare" once per-worktree config is on
// (git: "this operation must be run in a work tree"), so that setting moves to
// the bare repository's own config.worktree first.
func enableWorktreeConfig(ctx context.Context, g git.Client, common string) error {
	if bare, _ := g.Run(ctx, common, "config", "--local", "--get", "core.bare"); strings.TrimSpace(bare) == "true" {
		if _, err := g.Run(ctx, common, "config", "--file", filepath.Join(common, "config.worktree"), "core.bare", "true"); err != nil {
			return err
		}
		if v, _ := g.Run(ctx, common, "config", "--get", "extensions.worktreeConfig"); strings.TrimSpace(v) != "true" {
			if _, err := g.Run(ctx, common, "config", "extensions.worktreeConfig", "true"); err != nil {
				return err
			}
		}
		_, err := g.Run(ctx, common, "config", "--local", "--unset", "core.bare")
		return err
	}
	if v, _ := g.Run(ctx, common, "config", "--get", "extensions.worktreeConfig"); strings.TrimSpace(v) != "true" {
		_, err := g.Run(ctx, common, "config", "extensions.worktreeConfig", "true")
		return err
	}
	return nil
}

// setWorktree writes key in the worktree's config, or removes it when value is
// empty. It skips the write when the value is already there.
func setWorktree(ctx context.Context, g git.Client, dir, key, value string) error {
	cur, err := g.Run(ctx, dir, "config", "--worktree", "--get", key)
	have := err == nil
	if value == "" {
		if !have {
			return nil
		}
		_, err := g.Run(ctx, dir, "config", "--worktree", "--unset-all", key)
		return err
	}
	if have && strings.TrimSpace(cur) == value {
		return nil
	}
	_, err = g.Run(ctx, dir, "config", "--worktree", key, value)
	return err
}

// Read is the commit prefix recorded for the worktree at dir, "" when none.
func Read(ctx context.Context, dir string) string {
	out, err := git.Client{}.Run(ctx, dir, "config", "--get", KeyPrefix)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
