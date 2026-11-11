package fake

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// What `ade project bootstrap` reads and writes: an owner's repositories, a
// repository's labels, and the contents API for committing a file.

func (s *Server) discoverRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /orgs/{owner}/repos", s.listOwnerRepos)
	s.handle(mux, "GET /users/{owner}/repos", s.listOwnerRepos)
	s.handle(mux, "GET /user/repos", s.listMyRepos)
	s.handle(mux, "GET /repos/{owner}/{repo}/labels", s.listRepoLabels)
	s.handle(mux, "PUT /repos/{owner}/{repo}/contents/{path...}", s.putContents)
}

// AddRepo adds a repository with the shared milestones, labels and template,
// and returns its full name.
func (s *Server) AddRepo(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.st.newRepo(name, s.st.clock)
	return rp.Owner + "/" + rp.Name
}

// ArchiveRepo marks a repository archived.
func (s *Server) ArchiveRepo(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rp := s.st.repoNamed(name); rp != nil {
		rp.Archived = true
	}
}

// AddFile commits a file to the default branch of a repository.
func (s *Server) AddFile(name, path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rp := s.st.repoNamed(name); rp != nil {
		s.commitFile(rp, rp.Default, path, content, "Add "+path)
	}
}

// FileAt reads a file as committed on a branch.
func (s *Server) FileAt(name, branch, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.st.repoNamed(name)
	if rp == nil {
		return "", false
	}
	c := rp.resolveRef(branch)
	if c == nil {
		return "", false
	}
	content, ok := c.Files[path]
	return content, ok
}

// commitFile adds a commit that sets path on branch and moves the branch.
func (s *Server) commitFile(rp *repo, branch, path, content, msg string) *commit {
	head := rp.resolveRef(branch)
	files := cloneFiles(head.Files)
	files[path] = content
	c := s.st.commit(rp, msg, s.st.now(), files, head.SHA)
	rp.Branches[branch] = c.SHA
	return c
}

func (s *Server) listOwnerRepos(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.PathValue("owner"), s.st.owner) {
		notFound(w)
		return
	}
	paged(s, w, r, s.st.repos, func(rp *repo) any { return s.renderRepo(rp) })
}

func (s *Server) listRepoLabels(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	paged(s, w, r, rp.Labels, func(l *label) any { return s.renderLabel(rp, l) })
}

// putContents creates or replaces a file on a branch with one commit. As on
// GitHub, replacing a file needs its current blob sha.
func (s *Server) putContents(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	var in struct {
		Message string `json:"message"`
		Content string `json:"content"`
		Branch  string `json:"branch"`
		SHA     string `json:"sha"`
	}
	if !decode(w, r, &in) {
		return
	}
	path := strings.Trim(r.PathValue("path"), "/")
	raw, err := base64.StdEncoding.DecodeString(in.Content)
	if in.Message == "" || err != nil || path == "" {
		custom(w, "Contents", "message and base64 content are required")
		return
	}
	if in.Branch == "" {
		in.Branch = rp.Default
	}
	head := rp.resolveRef(in.Branch)
	if head == nil || rp.Branches[in.Branch] == "" {
		notFound(w)
		return
	}
	old, exists := head.Files[path]
	switch {
	case exists && in.SHA == "":
		custom(w, "Contents", `"sha" wasn't supplied.`)
		return
	case exists && in.SHA != blobSHA(old):
		apiError(w, http.StatusConflict, path+" does not match "+in.SHA)
		return
	}
	c := s.commitFile(rp, in.Branch, path, string(raw), in.Message)
	status := http.StatusCreated
	if exists {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"content": map[string]any{"name": path[strings.LastIndex(path, "/")+1:], "path": path, "sha": blobSHA(string(raw))},
		"commit":  map[string]any{"sha": c.SHA, "message": in.Message},
	})
}

// listMyRepos is the authenticated user's own repositories, private ones
// included. The fake's repos belong to one owner, so a viewer who is not that
// owner has none; this lists the owner's repos for a viewer standing in for it.
func (s *Server) listMyRepos(w http.ResponseWriter, r *http.Request) {
	if q := r.URL.Query().Get("affiliation"); q != "" && q != "owner" {
		custom(w, "Repository", "unsupported affiliation")
		return
	}
	paged(s, w, r, s.st.repos, func(rp *repo) any { return s.renderRepo(rp) })
}
