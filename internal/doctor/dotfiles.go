package doctor

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/coredev-uk/ade/internal/dotfiles"
	"github.com/coredev-uk/ade/internal/project"
)

const dotfilesGroup = "dotfiles"

// dotfilesChecks reports, per project whose manifest has [dotfiles], whether
// they were applied. Configured but never applied is a warning: a new
// workspace is missing what the team's install script sets up.
func dotfilesChecks(ctx context.Context, o Options) []Check {
	if o.Config == nil || o.Root == "" || o.DataDir == "" {
		return nil
	}
	entries, err := os.ReadDir(o.Root)
	if err != nil {
		return nil
	}
	home := o.Getenv("HOME")
	var checks []Check
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := project.LoadManifest(filepath.Join(o.Root, e.Name()))
		if err != nil {
			continue
		}
		s, ok := dotfiles.Resolve(o.Config, e.Name(), m)
		if !ok {
			continue
		}
		c := Check{Group: dotfilesGroup, Name: s.Project}
		switch st, applied := dotfiles.ReadState(o.DataDir, s.Project); {
		case applied && st.Repo == s.Repo:
			c.Status = OK
			c.Detail = s.Repo + ": applied " + shortCommit(st.Commit) + " (" + st.Method + ") on " + st.AppliedAt.Local().Format(time.DateOnly)
		case dotfiles.CoderApplied(ctx, o.Getenv, home, s.Repo):
			c.Status, c.Detail = OK, s.Repo+": applied by coder"
		default:
			c.Status, c.Detail = Warn, s.Repo+": configured but never applied"
			if applied {
				c.Detail = s.Repo + ": configured, but the last apply was of " + st.Repo
			}
			c.Fix = "run `ade dotfiles` (or `ade dotfiles --dry-run` to see what it would do)"
		}
		checks = append(checks, c)
	}
	return checks
}

func shortCommit(c string) string {
	if len(c) > 10 {
		return c[:10]
	}
	if c == "" {
		return "(no commit)"
	}
	return c
}
