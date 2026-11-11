package fake

import (
	"encoding/base64"
	"net/http"
	"slices"
	"sort"
	"strings"
)

func (s *Server) gitRoutes(mux *http.ServeMux) {
	const r = "/repos/{owner}/{repo}"
	s.handle(mux, "GET "+r+"/commits/{ref...}", s.getCommit)
	s.handle(mux, "GET "+r+"/git/matching-refs/heads/{prefix...}", s.matchingRefs)
	s.handle(mux, "GET "+r+"/git/ref/heads/{branch...}", s.getRef)
	s.handle(mux, "POST "+r+"/git/refs", s.createRef)
	s.handle(mux, "PATCH "+r+"/git/refs/heads/{branch...}", s.updateRef)
	s.handle(mux, "DELETE "+r+"/git/refs/heads/{branch...}", s.deleteRef)
	s.handle(mux, "GET "+r+"/git/commits/{sha}", s.getGitCommit)
	s.handle(mux, "POST "+r+"/git/commits", s.createGitCommit)
	s.handle(mux, "GET "+r+"/contents", s.contents)
	s.handle(mux, "GET "+r+"/contents/{path...}", s.contents)
}

func (s *Server) renderRef(rp *repo, branch string) map[string]any {
	sha := rp.Branches[branch]
	return map[string]any{
		"ref": "refs/heads/" + branch, "node_id": "REF_" + branch,
		"url": s.repoAPI(rp) + "/git/ref/heads/" + branch,
		"object": map[string]any{
			"sha": sha, "type": "commit", "url": s.repoAPI(rp) + "/git/commits/" + sha,
		},
	}
}

func (s *Server) getCommit(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	ref := r.PathValue("ref")
	c := rp.resolveRef(ref)
	if c == nil {
		apiError(w, http.StatusUnprocessableEntity, "No commit found for SHA: "+ref)
		return
	}
	writeJSON(w, http.StatusOK, s.renderCommit(rp, c))
}

func (s *Server) matchingRefs(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	prefix := r.PathValue("prefix")
	var names []string
	for n := range rp.Branches {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	paged(s, w, r, names, func(n string) any { return s.renderRef(rp, n) })
}

func (s *Server) getRef(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	branch := r.PathValue("branch")
	if _, ok := rp.Branches[branch]; !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, s.renderRef(rp, branch))
}

func (s *Server) createRef(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	var in struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if !decode(w, r, &in) {
		return
	}
	branch, ok := strings.CutPrefix(in.Ref, "refs/heads/")
	switch {
	case !ok || branch == "":
		apiError(w, http.StatusUnprocessableEntity, "Reference name is invalid")
	case rp.Branches[branch] != "":
		apiError(w, http.StatusUnprocessableEntity, "Reference already exists")
	case rp.Commits[in.SHA] == nil:
		apiError(w, http.StatusUnprocessableEntity, "Object does not exist")
	default:
		rp.Branches[branch] = in.SHA
		s.st.addFeed(rp, "CreateEvent", viewer, s.st.now(), map[string]any{"ref": branch, "ref_type": "branch"})
		writeJSON(w, http.StatusCreated, s.renderRef(rp, branch))
	}
}

func (s *Server) updateRef(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	var in struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	if !decode(w, r, &in) {
		return
	}
	branch := r.PathValue("branch")
	cur, ok := rp.Branches[branch]
	switch {
	case !ok:
		apiError(w, http.StatusUnprocessableEntity, "Reference does not exist")
	case rp.Commits[in.SHA] == nil:
		apiError(w, http.StatusUnprocessableEntity, "Object does not exist")
	case !in.Force && !rp.isAncestor(cur, in.SHA):
		apiError(w, http.StatusUnprocessableEntity, "Update is not a fast forward")
	default:
		rp.Branches[branch] = in.SHA
		writeJSON(w, http.StatusOK, s.renderRef(rp, branch))
	}
}

func (s *Server) deleteRef(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	branch := r.PathValue("branch")
	if _, ok := rp.Branches[branch]; !ok {
		apiError(w, http.StatusUnprocessableEntity, "Reference does not exist")
		return
	}
	delete(rp.Branches, branch)
	s.st.addFeed(rp, "DeleteEvent", viewer, s.st.now(), map[string]any{"ref": branch, "ref_type": "branch"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) renderGitCommit(rp *repo, c *commit) map[string]any {
	person := map[string]any{"name": "Demo User", "email": "demo-user@example.test", "date": iso(c.Created)}
	parents := make([]any, 0, len(c.Parents))
	for _, p := range c.Parents {
		parents = append(parents, map[string]any{
			"sha": p, "url": s.repoAPI(rp) + "/git/commits/" + p, "html_url": s.repoHTML(rp) + "/commit/" + p,
		})
	}
	return map[string]any{
		"sha": c.SHA, "node_id": "C_" + c.SHA[:12], "url": s.repoAPI(rp) + "/git/commits/" + c.SHA,
		"html_url": s.repoHTML(rp) + "/commit/" + c.SHA, "message": c.Message,
		"tree":   map[string]any{"sha": c.Tree, "url": s.repoAPI(rp) + "/git/trees/" + c.Tree},
		"author": person, "committer": person, "parents": parents,
	}
}

func (s *Server) getGitCommit(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	c := rp.Commits[r.PathValue("sha")]
	if c == nil {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, s.renderGitCommit(rp, c))
}

// createGitCommit stores a commit over an existing tree. The tree must be the
// tree of a commit the repo already has; the new commit shares its files.
func (s *Server) createGitCommit(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	var in struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Message == "" {
		validation(w, fieldError{Resource: "Commit", Field: "message", Code: "missing_field"})
		return
	}
	var base *commit
	for _, c := range rp.Commits {
		if c.Tree == in.Tree && (base == nil || c.SHA < base.SHA) {
			base = c
		}
	}
	if base == nil {
		apiError(w, http.StatusUnprocessableEntity, "Tree SHA does not exist")
		return
	}
	for _, p := range in.Parents {
		if rp.Commits[p] == nil {
			apiError(w, http.StatusUnprocessableEntity, "Parent SHA does not exist")
			return
		}
	}
	at := s.st.now()
	c := s.st.commit(rp, in.Message, at, cloneFiles(base.Files), in.Parents...)
	writeJSON(w, http.StatusCreated, s.renderGitCommit(rp, c))
}

// contents serves the repository contents API for a branch or commit.
func (s *Server) contents(w http.ResponseWriter, r *http.Request) {
	rp := s.repoOf(w, r)
	if rp == nil {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = rp.Default
	}
	c := rp.resolveRef(ref)
	if c == nil {
		apiError(w, http.StatusNotFound, "No commit found for the ref "+ref)
		return
	}
	path := strings.Trim(r.PathValue("path"), "/")
	if content, ok := c.Files[path]; ok && path != "" {
		s.writeFile(w, r, rp, path, content)
		return
	}
	entries := dirEntries(c, path)
	if len(entries) == 0 {
		notFound(w)
		return
	}
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		m := map[string]any{
			"type": e.kind, "name": e.name, "path": e.path, "sha": e.sha, "size": e.size,
			"url": s.repoAPI(rp) + "/contents/" + e.path + "?ref=" + ref,
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request, rp *repo, path, content string) {
	if strings.Contains(r.Header.Get("Accept"), "application/vnd.github.raw") ||
		strings.Contains(r.Header.Get("Accept"), "application/vnd.github.v3.raw") {
		w.Header().Set("Content-Type", "application/vnd.github.raw+json; charset=utf-8")
		_, _ = w.Write([]byte(content))
		return
	}
	// GitHub wraps the base64 payload at 60 columns.
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	var wrapped strings.Builder
	for chunk := range slices.Chunk([]byte(enc), 60) {
		wrapped.Write(chunk)
		wrapped.WriteByte('\n')
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"type": "file", "encoding": "base64", "size": len(content), "name": path[strings.LastIndex(path, "/")+1:],
		"path": path, "sha": blobSHA(content), "content": wrapped.String(),
		"url":      s.repoAPI(rp) + "/contents/" + path,
		"html_url": s.repoHTML(rp) + "/blob/" + rp.Default + "/" + path,
	})
}

type dirEntry struct {
	kind, name, path, sha string
	size                  int
}

// dirEntries lists the immediate children of a directory in a commit's tree.
func dirEntries(c *commit, dir string) []dirEntry {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	seen := map[string]dirEntry{}
	for p, content := range c.Files {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		name, _, nested := strings.Cut(rest, "/")
		if nested {
			seen[name] = dirEntry{kind: "dir", name: name, path: prefix + name, sha: treeSHA(map[string]string{prefix + name: ""})}
		} else {
			seen[name] = dirEntry{kind: "file", name: name, path: p, sha: blobSHA(content), size: len(content)}
		}
	}
	out := make([]dirEntry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b dirEntry) int { return strings.Compare(a.name, b.name) })
	return out
}
