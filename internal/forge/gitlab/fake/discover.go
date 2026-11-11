package fake

import (
	"encoding/base64"
	"net/http"
	"slices"
	"sort"
	"time"
)

// What `ade project bootstrap` reads and writes: the group's labels, and the
// repository files API (the only way to commit without a git clone).

func init() {
	registerRoutes(func(s *Server, mux *http.ServeMux) {
		const pp = "/api/v4/projects/{id}"
		mux.HandleFunc("GET "+pp+"/labels", s.listLabels)
		mux.HandleFunc("POST "+pp+"/repository/files/{path}", s.createFile)
		mux.HandleFunc("PUT "+pp+"/repository/files/{path}", s.updateFile)
	})
}

// sandboxLabels are the group labels scripts/sandbox/gitlab-seed.sh defines.
var sandboxLabels = []string{
	"type::story", "type::bug", "app::api", "app::web",
	"workflow::in progress", "workflow::in review", "workflow::blocked", "workflow::done",
	"weight::1", "weight::2", "weight::3", "weight::5", "weight::8",
}

// FileCommit is one commit the files API recorded.
type FileCommit struct {
	Project, Branch, Path, Message string
	Content                        string
	Update                         bool // replaced an existing file
}

type discovery struct {
	labels  []string
	commits []FileCommit
}

func (s *Server) discovery() *discovery {
	return s.ext("discover", func() any { return &discovery{labels: slices.Clone(sandboxLabels)} }).(*discovery)
}

// AddProject adds an empty project to the group and returns its full path.
func (s *Server) AddProject(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &project{ID: s.st.id(), Path: s.st.group + "/" + name}
	s.st.projects = append(s.st.projects, p)
	s.repos().branches[p.Path] = []string{"main"}
	return p.Path
}

// ArchiveProject marks a project archived.
func (s *Server) ArchiveProject(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.st.findProject(path); p != nil {
		p.Archived = true
	}
}

// AddLabel defines a group label that no issue carries.
func (s *Server) AddLabel(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.discovery()
	if !slices.Contains(d.labels, name) {
		d.labels = append(d.labels, name)
	}
}

// FileCommits lists the commits the files API recorded, oldest first.
func (s *Server) FileCommits() []FileCommit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.discovery().commits)
}

// FileAt reads a file as seen on a branch.
func (s *Server) FileAt(project, ref, path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.repos().view(project, ref)[path]
	return c, ok
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.lookupProject(w, r); !ok {
		return
	}
	names := slices.Clone(s.discovery().labels)
	for _, is := range s.st.issues {
		for _, l := range is.Labels {
			if !slices.Contains(names, l) {
				names = append(names, l)
			}
		}
	}
	sort.Strings(names)
	paged(s, w, r, names, func(n string) any {
		return object{"id": s.st.nextID, "name": n, "color": "#428bca", "description": ""}
	})
}

func (s *Server) createFile(w http.ResponseWriter, r *http.Request) { s.writeRepoFile(w, r, false) }
func (s *Server) updateFile(w http.ResponseWriter, r *http.Request) { s.writeRepoFile(w, r, true) }

func (s *Server) writeRepoFile(w http.ResponseWriter, r *http.Request, update bool) {
	p := params(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	proj, ok := s.lookupProject(w, r)
	if !ok {
		return
	}
	rs := s.repos()
	branch, path, msg := str(p, "branch"), r.PathValue("path"), str(p, "commit_message")
	content := str(p, "content")
	if str(p, "encoding") == "base64" {
		b, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			badRequest(w, "content is not valid base64")
			return
		}
		content = string(b)
	}
	_, exists := rs.view(proj.Path, branch)[path]
	switch {
	case branch == "" || msg == "":
		badRequest(w, "branch and commit_message are required")
		return
	case !slices.Contains(rs.branches[proj.Path], branch):
		writeJSON(w, http.StatusBadRequest, object{"message": "You can only create or edit files when you are on a branch"})
		return
	case !update && exists:
		writeJSON(w, http.StatusBadRequest, object{"message": "A file with this name already exists"})
		return
	case update && !exists:
		writeJSON(w, http.StatusBadRequest, object{"message": "A file with this name doesn't exist"})
		return
	}
	key := proj.Path + "@" + branch
	if rs.overlay[key] == nil {
		rs.overlay[key] = map[string]string{}
	}
	rs.overlay[key][path] = content
	d := s.discovery()
	d.commits = append(d.commits, FileCommit{
		Project: proj.Path, Branch: branch, Path: path, Message: msg, Content: content, Update: update,
	})
	status := http.StatusCreated
	if update {
		status = http.StatusOK
	}
	writeJSON(w, status, object{"file_path": path, "branch": branch})
}

// cadenceNodes renders the iteration cadences of the group's iterations for
// the cadence query: one per cadence title, two-week unless the dates say
// otherwise, starting with the earliest iteration.
func (s *Server) cadenceNodes(detail bool) []object {
	var titles []string
	for _, it := range s.st.iterations {
		if !slices.Contains(titles, it.Cadence) {
			titles = append(titles, it.Cadence)
		}
	}
	nodes := make([]object, 0, len(titles))
	for _, t := range titles {
		n := object{"title": t}
		if detail {
			var first time.Time
			weeks := 0
			for _, it := range s.st.iterations {
				if it.Cadence != t {
					continue
				}
				if first.IsZero() || it.Start.Before(first) {
					first = it.Start
				}
				if weeks == 0 {
					weeks = max(1, int(it.Due.Sub(it.Start).Hours()/24+1)/7)
				}
			}
			n["durationInWeeks"], n["startDate"], n["automatic"] = weeks, first.Format("2006-01-02"), t == deliveryCadence
		}
		nodes = append(nodes, n)
	}
	return nodes
}
