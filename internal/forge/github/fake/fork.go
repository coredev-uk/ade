package fake

import (
	"fmt"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// forkHead rewrites head.repo of a rendered PR whose head branch lives in a
// fork: the fork's full name, or null once the fork is deleted.
func (s *Server) forkHead(m map[string]any, p *pull) {
	if p.HeadRepo == "" && !p.HeadGone {
		return
	}
	head, _ := m["head"].(map[string]any)
	if head == nil {
		return
	}
	if p.HeadGone {
		head["repo"] = nil
		return
	}
	owner, name, _ := strings.Cut(p.HeadRepo, "/")
	head["label"] = owner + ":" + p.Head
	head["repo"] = map[string]any{"full_name": p.HeadRepo, "name": name, "owner": s.renderUser(owner), "private": false}
}

// AddForkPull adds an open PR into project's default branch whose head branch
// lives in the fork forkFullName ("owner/name"). The branch is not a branch
// of project. With gone, the fork is deleted: head.repo is null.
func (s *Server) AddForkPull(project, forkFullName, headBranch, title string, gone bool) (forge.Ref, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, name, ok := splitProject(project)
	rp := s.st.repo(owner, name)
	if !ok || rp == nil {
		return forge.Ref{}, fmt.Errorf("no repo %q", project)
	}
	base := rp.resolveRef(rp.Default)
	if base == nil {
		return forge.Ref{}, fmt.Errorf("repo %q has no %s branch", project, rp.Default)
	}
	at := s.st.clock.Add(-time.Hour)
	files := cloneFiles(base.Files)
	files["FORK.md"] = "From " + forkFullName + "\n"
	head := s.st.commit(rp, title, at, files, base.SHA)
	it := s.st.addItem(rp, title, "", s.st.owner, at)
	it.PR = &pull{
		Head: headBranch, Base: rp.Default, HeadSHA: head.SHA, BaseSHA: base.SHA,
		HeadRepo: forkFullName, HeadGone: gone, Files: diffCommits(base, head),
	}
	return forge.Ref{Project: project, IID: it.Number, Kind: forge.KindMR}, nil
}
