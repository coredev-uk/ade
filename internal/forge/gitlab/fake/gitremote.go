package fake

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// WithGitRoot backs the fake's branches with real bare repositories under dir:
// a project registered with InitRemote has DIR/<name>.git, and creating a
// branch through the API also creates it there (cut from the requested ref),
// so clones of the remote can fetch what Start work "pushed".
func WithGitRoot(dir string) Option { return func(o *options) { o.gitRoot = dir } }

// RemotePath is where the project's bare repository lives (WithGitRoot), or
// "" when the server has no git root.
func (s *Server) RemotePath(project string) string {
	if s.opts.gitRoot == "" {
		return ""
	}
	return filepath.Join(s.opts.gitRoot, path.Base(project)+".git")
}

// InitRemote creates the project's bare repository with one commit on main
// holding files (path -> content). It needs WithGitRoot and the git binary.
func (s *Server) InitRemote(project string, files map[string]string) error {
	bare := s.RemotePath(project)
	if bare == "" {
		return fmt.Errorf("fake: no git root configured")
	}
	work, err := os.MkdirTemp("", "fakegl-work-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	for p, content := range files {
		dst := filepath.Join(work, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main", bare},
		{"-C", work, "init", "-q", "-b", "main"},
		{"-C", work, "add", "-A"},
		{"-C", work, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Initial commit"},
		{"-C", work, "push", "-q", bare, "main"},
	} {
		if err := runGit(args...); err != nil {
			return err
		}
	}
	// Branches the seed already has (MR source branches) exist on the remote too.
	for _, b := range s.BranchNames(project) {
		if b != "main" {
			if err := runGit("-C", bare, "branch", b, "main"); err != nil {
				return err
			}
		}
	}
	return nil
}

// createRemoteBranch mirrors a branch created through the API; it is a no-op
// for projects without a bare repository.
func (s *Server) createRemoteBranch(project, name, from string) error {
	bare := s.RemotePath(project)
	if bare == "" {
		return nil
	}
	if _, err := os.Stat(bare); err != nil {
		return nil
	}
	return runGit("-C", bare, "branch", name, from)
}

func runGit(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Fake GitLab", "GIT_AUTHOR_EMAIL=fake@example.invalid",
		"GIT_COMMITTER_NAME=Fake GitLab", "GIT_COMMITTER_EMAIL=fake@example.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
