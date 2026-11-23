package fake

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// sourceProject is the project the MR's source branch lives in.
func (m *mr) sourceProject() *project {
	if m.SourceProject != nil {
		return m.SourceProject
	}
	return m.Project
}

// AddForkMR adds an open MR into target from sourceBranch of the fork project
// forkPath (made when missing): source_project_id differs from
// target_project_id, and the source branch is NOT a branch of the target
// repo. When the target has a bare repository (WithGitRoot + InitRemote) its
// head commit is served as refs/merge-requests/<iid>/head, as GitLab does for
// every MR; the MR's sha is that commit.
func (s *Server) AddForkMR(target, forkPath, sourceBranch, title string) (forge.Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tp := s.st.findProject(target)
	if tp == nil {
		return forge.Ref{}, fmt.Errorf("fake: no project %q", target)
	}
	fp := s.st.findProject(forkPath)
	if fp == nil {
		fp = &project{ID: s.st.id(), Path: forkPath}
		s.st.projects = append(s.st.projects, fp)
		s.repos().branches[fp.Path] = []string{"main"}
	}
	iid := 1
	for _, o := range s.st.mrs {
		if o.Project == tp {
			iid = max(iid, o.IID+1)
		}
	}
	sha := branchSHA(fp.Path, sourceBranch)
	if bare := s.RemotePath(tp.Path); bare != "" {
		if _, err := os.Stat(bare); err == nil {
			head, err := commitOnRemote(bare, title)
			if err != nil {
				return forge.Ref{}, err
			}
			if err := runGit("-C", bare, "update-ref", fmt.Sprintf("refs/merge-requests/%d/head", iid), head); err != nil {
				return forge.Ref{}, err
			}
			sha = head
		}
	}
	now := s.st.clock
	s.st.mrs = append(s.st.mrs, &mr{
		ID: s.st.id(), IID: iid, Project: tp, SourceProject: fp, Title: title,
		State: "opened", Source: sourceBranch, Target: "main", SHA: sha,
		Author: s.st.me, Created: now, Updated: now,
	})
	return s.st.ref(tp, iid, forge.KindMR), nil
}

// commitOnRemote makes a commit on top of main in bare that no branch holds:
// the head of a fork MR.
func commitOnRemote(bare, msg string) (string, error) {
	cmd := exec.Command("git", "-C", bare, "commit-tree", "main^{tree}", "-p", "main", "-m", msg)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Fake GitLab", "GIT_AUTHOR_EMAIL=fake@example.invalid",
		"GIT_COMMITTER_NAME=Fake GitLab", "GIT_COMMITTER_EMAIL=fake@example.invalid",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
