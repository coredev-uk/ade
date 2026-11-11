// Package audit is ADE's append-only action log: one JSON line per action in
// <records>/audit/YYYY-MM.jsonl. It is durable and never rewritten.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"
)

// Entry is one audited action. Append masks credentials in Target, Error,
// URL and Params (see Redact), but callers should still keep secrets out.
type Entry struct {
	ID      string         `json:"id"`
	TS      time.Time      `json:"ts"`
	Actor   string         `json:"actor"`  // user | ade:update | agent:<run-id>
	Action  string         `json:"action"` // dotted, e.g. "git.clone", "project.add"
	Target  string         `json:"target,omitempty"`
	Item    string         `json:"item,omitempty"`
	Project string         `json:"project,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	DryRun  bool           `json:"dry_run,omitempty"`
	Outcome string         `json:"outcome"` // ok | error | cancelled | skipped
	Error   string         `json:"error,omitempty"`
	URL     string         `json:"url,omitempty"`
	AI      *AIInfo        `json:"ai,omitempty"`
}

// AIInfo is filled for ai.* actions (Phase 4).
type AIInfo struct {
	Profile    string  `json:"profile"`
	Model      string  `json:"model"`
	TokensIn   int     `json:"tokens_in"`
	TokensOut  int     `json:"tokens_out"`
	CostUSD    float64 `json:"cost_usd"`
	CostSource string  `json:"cost_source"`
	CacheHit   bool    `json:"cache_hit"`
	Artifact   string  `json:"artifact,omitempty"`
	DestHost   string  `json:"dest_host,omitempty"`
	Bytes      int     `json:"bytes,omitempty"`
}

// Log appends entries to monthly JSONL files under Dir.
type Log struct {
	Dir   string
	Actor string // default actor for entries without one
}

// Open ensures <recordsDir>/audit exists (0700).
func Open(recordsDir, actor string) (*Log, error) {
	dir := filepath.Join(recordsDir, "audit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{Dir: dir, Actor: actor}, nil
}

// Append writes e as one line, under an exclusive flock, then fsyncs. Safe
// across concurrent ade processes.
func (l *Log) Append(e Entry) error {
	if e.ID == "" {
		e.ID = ulid.Make().String()
	}
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Actor == "" {
		e.Actor = l.Actor
	}
	if e.Outcome == "" {
		e.Outcome = "ok"
	}
	redactEntry(&e)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := filepath.Join(l.Dir, e.TS.UTC().Format("2006-01")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", path, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

// Filter selects entries for Query. Zero fields match everything.
type Filter struct {
	Since   time.Time
	Action  string // glob on the dotted action, e.g. "git.*"
	Item    string
	Project string
	Limit   int // newest N; 0 = all
}

// Query returns matching entries, oldest first.
func (l *Log) Query(f Filter) ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(l.Dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Entry
	for _, path := range files {
		month := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if !f.Since.IsZero() {
			if end, err := time.Parse("2006-01", month); err == nil && end.AddDate(0, 1, 0).Before(f.Since) {
				continue
			}
		}
		if err := scanFile(path, func(e Entry) {
			if f.match(e) {
				out = append(out, e)
			}
		}); err != nil {
			return nil, err
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out, nil
}

func (f Filter) match(e Entry) bool {
	if !f.Since.IsZero() && e.TS.Before(f.Since) {
		return false
	}
	if f.Item != "" && e.Item != f.Item {
		return false
	}
	if f.Project != "" && e.Project != f.Project {
		return false
	}
	if f.Action != "" && !GlobMatch(f.Action, e.Action) {
		return false
	}
	return true
}

func scanFile(path string, fn func(Entry)) error {
	fh, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			fn(e)
		}
	}
	return sc.Err()
}

// GlobMatch matches dotted names against patterns where "*" spans any run of
// characters, including dots ("git.*" matches "git.pull.failed"; "git.*.failed"
// matches "git.pull.failed").
func GlobMatch(pattern, name string) bool {
	if pattern == "*" || pattern == name {
		return true
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return false
	}
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	rest := name[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(rest, parts[i])
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}
