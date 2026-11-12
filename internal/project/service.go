package project

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
)

const defaultConcurrency = 4

// Progress counts repos finished out of all repos in the run ("updating 4/7").
type Progress struct {
	Done  int
	Total int
}

// Service runs project operations against an App.
type Service struct {
	App *app.App
	// Actor is recorded on audit entries ("user", "ade:update").
	Actor string
	// OnProgress, when set, is called after each repo completes. Calls are serialised.
	OnProgress func(Progress)

	tokens tokenCache
	// cached makes manifest reads use the local copy of a remote source when
	// there is one (CachedManifest), instead of fetching it again.
	cached bool
}

// New returns a Service whose audit entries carry actor.
func New(a *app.App, actor string) *Service { return &Service{App: a, Actor: actor} }

// run is one project resolved to the manifest ADE will act on.
type run struct {
	name     string
	dir      string
	source   string
	manifest *config.Manifest
	raw      []byte
	stale    bool // manifest came from the snapshot because the source failed
}

func (s *Service) concurrency() int {
	if n := s.App.Config.Update.Concurrency; n > 0 {
		return n
	}
	return defaultConcurrency
}

func (s *Service) publish(kind string, sev events.Severity, project, subject, title, detail string) {
	e := events.New(kind, sev, title)
	e.Project, e.Subject, e.Detail = project, subject, detail
	s.App.Bus.Publish(e)
}

func (s *Service) audit(e audit.Entry) {
	e.Actor = s.Actor
	// An unwritable audit log must not abort a clone that already happened.
	_ = s.App.Audit.Append(e)
}

// resolve fetches the manifest for source. With allowStale, a failing source
// falls back to the last good snapshot (run.stale is set and the error is returned
// alongside so the caller can report it).
func (s *Service) resolve(ctx context.Context, source string, allowStale bool) (*run, error) {
	canon, err := s.canonical(source)
	if err != nil {
		return nil, err
	}
	m, raw, err := s.loadManifest(ctx, canon)
	if err == nil {
		return &run{name: m.Name, dir: filepath.Join(s.App.Root, m.Name), source: canon, manifest: m, raw: raw}, nil
	}
	if !allowStale {
		return nil, err
	}
	name, sn, serr := findSnapshot(s.App.Root, canon)
	if serr != nil || sn == nil {
		return nil, err
	}
	old, perr := sn.parse()
	if perr != nil {
		return nil, err
	}
	return &run{name: name, dir: filepath.Join(s.App.Root, name), source: canon, manifest: old, raw: []byte(sn.Manifest), stale: true}, err
}

// Add registers source in the user config when needed, then clones whatever is
// missing. Re-running it only fills gaps.
func (s *Service) Add(ctx context.Context, source string) (Report, error) {
	r, err := s.resolve(ctx, source, false)
	if err != nil {
		s.audit(audit.Entry{Action: "project.add", Target: source, Outcome: "error", Error: err.Error()})
		return Report{}, err
	}
	registered, err := s.register(source)
	if err != nil {
		return Report{}, fmt.Errorf("register project: %w", err)
	}
	rep, err := s.syncRuns(ctx, []*run{r}, modeAdd)
	s.audit(audit.Entry{
		Action: "project.add", Target: r.source, Project: r.name,
		Params: map[string]any{"registered": registered, "repos": len(rep.Results)}, Outcome: outcome(err),
	})
	return rep, err
}

// Update runs PLAN 2.3 steps 1-5 and 7 over every registered project, then
// publishes the git.update.done summary.
func (s *Service) Update(ctx context.Context) (Report, error) {
	runs, invalid := s.resolveAll(ctx)
	rep, err := s.syncRuns(ctx, runs, modeUpdate)
	rep.Invalid = invalid
	if err != nil {
		return rep, err
	}
	e := events.New("git.update.done", rep.Severity(), rep.Title())
	// A run that changed nothing is worth a toast, not an inbox row: periodic
	// updates would otherwise bury real events under "all repos up to date".
	e.Transient = !rep.Changed()
	s.App.Bus.Publish(e)
	return rep, nil
}

func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// resolveAll resolves every registered source in parallel. Failures are
// reported as project.manifest.invalid; a snapshot keeps the project updating.
func (s *Service) resolveAll(ctx context.Context) (runs []*run, invalid []string) {
	refs := s.App.Config.Projects
	results := make([]*run, len(refs))
	errs := make([]error, len(refs))
	forEach(s.concurrency(), len(refs), func(i int) {
		results[i], errs[i] = s.resolve(ctx, refs[i].Source, true)
	})
	seen := map[string]bool{}
	for i, r := range results {
		if errs[i] != nil {
			invalid = append(invalid, errs[i].Error())
			s.publish("project.manifest.invalid", events.Warn, nameOf(r), refs[i].Source,
				"manifest rejected: "+refs[i].Source, errs[i].Error())
		}
		if r == nil || seen[r.dir] {
			continue
		}
		seen[r.dir] = true
		if !r.stale {
			s.flagRemoved(r)
		}
		runs = append(runs, r)
	}
	return runs, invalid
}

func nameOf(r *run) string {
	if r == nil {
		return ""
	}
	return r.name
}

// flagRemoved notes repos that the new manifest dropped. They stay on disk.
func (s *Service) flagRemoved(r *run) {
	sn, err := readSnapshot(r.dir)
	if err != nil {
		return
	}
	old, err := sn.parse()
	if err != nil {
		return
	}
	for _, name := range old.RepoNames() {
		if _, ok := r.manifest.Repos[name]; !ok {
			s.publish("project.repo.removed", events.Warn, r.name, name,
				fmt.Sprintf("%s/%s was removed from the manifest; its clone is kept", r.name, name),
				filepath.Join(r.dir, name))
		}
	}
}

type mode int

const (
	modeAdd mode = iota
	modeUpdate
)

type task struct {
	run  *run
	name string
}

// syncRuns syncs every repo of every run on one shared worker pool, so
// update.concurrency bounds the whole operation rather than each project.
func (s *Service) syncRuns(ctx context.Context, runs []*run, m mode) (Report, error) {
	var tasks []task
	for _, r := range runs {
		if !r.stale {
			if err := writeSnapshotFor(r, time.Time{}); err != nil {
				return Report{}, fmt.Errorf("write %s: %w", snapshotFile, err)
			}
		}
		for _, name := range r.manifest.RepoNames() {
			tasks = append(tasks, task{r, name})
		}
	}
	results := make([]RepoResult, len(tasks))
	var (
		mu   sync.Mutex
		done int
	)
	progress := func() {
		if s.OnProgress == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		s.OnProgress(Progress{Done: done, Total: len(tasks)})
	}
	progress()
	forEach(s.concurrency(), len(tasks), func(i int) {
		results[i] = s.syncRepo(ctx, tasks[i].run, tasks[i].name, m)
		mu.Lock()
		done++
		mu.Unlock()
		progress()
	})
	if err := ctx.Err(); err != nil {
		return Report{Results: results}, err
	}
	for _, r := range runs {
		if !r.stale {
			if err := writeSnapshotFor(r, time.Now().UTC()); err != nil {
				return Report{Results: results}, fmt.Errorf("write %s: %w", snapshotFile, err)
			}
		}
	}
	return Report{Results: results}, nil
}

// writeSnapshotFor stores the run's manifest; a zero lastUpdate keeps the
// previous one.
func writeSnapshotFor(r *run, lastUpdate time.Time) error {
	sn := snapshot{Source: r.source, FetchedAt: time.Now().UTC(), LastUpdate: lastUpdate, Manifest: string(r.raw)}
	if lastUpdate.IsZero() {
		if prev, err := readSnapshot(r.dir); err == nil {
			sn.LastUpdate = prev.LastUpdate
		}
	}
	return writeSnapshot(r.dir, sn)
}

// forEach runs fn(0..n-1) with at most limit in flight and waits for all.
func forEach(limit, n int, fn func(i int)) {
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}()
	}
	wg.Wait()
}

// register appends a [[projects]] block for source to the user config unless an
// equivalent source is already there. It appends text instead of rewriting the
// file, so the user's comments and layout survive.
func (s *Service) register(source string) (added bool, err error) {
	canon, err := s.canonical(source)
	if err != nil {
		return false, err
	}
	for _, p := range s.App.Config.Projects {
		if c, err := s.canonical(p.Source); err == nil && c == canon {
			return false, nil
		}
	}
	var block bytes.Buffer
	block.WriteString("\n")
	entry := map[string][]config.ProjectRef{"projects": {{Source: source}}}
	if err := toml.NewEncoder(&block).Encode(entry); err != nil {
		return false, err
	}
	file := s.App.Config.File
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := f.Write(block.Bytes()); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	s.App.Config.Projects = append(s.App.Config.Projects, config.ProjectRef{Source: source})
	return true, nil
}
