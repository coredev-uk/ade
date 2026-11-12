package fake

import (
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// gitHTTP serves the bare repositories of WithGitRoot over git's smart HTTP
// protocol (git http-backend), the way GitLab serves GROUP/PROJECT.git, so a
// real `git clone https://host/prefix/group/project.git` works against the
// fake. Anything that is not a .git path goes to next. A host without git
// http-backend gets next for everything.
func (s *Server) gitHTTP(next http.Handler) http.Handler {
	if s.opts.gitRoot == "" {
		return next
	}
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return next
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	h := &cgi.Handler{
		Path: backend,
		Env:  []string{"GIT_PROJECT_ROOT=" + s.opts.gitRoot, "GIT_HTTP_EXPORT_ALL=1"},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := strings.Index(r.URL.Path, ".git/")
		if i < 0 {
			next.ServeHTTP(w, r)
			return
		}
		// /group/project.git/info/refs -> /project.git/info/refs (RemotePath is DIR/<base>.git).
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + path.Base(r.URL.Path[:i]) + r.URL.Path[i:]
		r2.URL.RawPath = ""
		h.ServeHTTP(w, r2)
	})
}
