package records

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AIDir is the folder of AI output inside an item record.
const AIDir = "ai"

// Artifact is one AI output kept in the item record: frontmatter that says
// what bought it, then the output as the body. Artifacts are the durable
// source of the result cache; cache.db only indexes them (see Artifacts).
type Artifact struct {
	Feature       string
	Profile       string
	Model         string
	PromptVersion string
	InputHash     string
	TokensIn      int
	TokensOut     int
	CostUSD       float64
	Created       time.Time
	AuditID       string
	Body          string

	// Path is set by WriteArtifact and Artifacts: relative to the data dir,
	// slash-separated.
	Path string
}

func (a Artifact) frontmatter() string {
	var b strings.Builder
	b.WriteString(fence + "\n")
	line := func(k, v string) { fmt.Fprintf(&b, "%s: %s\n", k, quote(v)) }
	line("feature", a.Feature)
	line("profile", a.Profile)
	line("model", a.Model)
	line("prompt_version", a.PromptVersion)
	line("input_hash", a.InputHash)
	fmt.Fprintf(&b, "tokens: [%d, %d]\n", a.TokensIn, a.TokensOut)
	line("cost_usd", strconv.FormatFloat(a.CostUSD, 'f', -1, 64))
	line("created", a.Created.UTC().Format(time.RFC3339))
	line("audit_id", a.AuditID)
	b.WriteString(fence + "\n")
	return b.String()
}

// WriteArtifact stores a under <itemDir>/ai/<stamp>-<name>.md without ever
// replacing an earlier file, and returns its path relative to the data dir.
// name is a short slug of what was asked ("summary-mr233").
func (s *Store) WriteArtifact(itemDir, name string, a Artifact) (string, error) {
	if a.Created.IsZero() {
		a.Created = s.now()
	}
	dir := filepath.Join(itemDir, AIDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := a.Created.UTC().Format("20060102T150405") + "-" + segment(name)
	for n := 1; ; n++ {
		file := base + ".md"
		if n > 1 {
			file = fmt.Sprintf("%s-%d.md", base, n)
		}
		path := filepath.Join(dir, file)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		body := strings.TrimRight(a.Body, "\n") + "\n"
		_, werr := f.WriteString(a.frontmatter() + body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(path) //nolint:errcheck // best effort: a partial artifact must not satisfy the cache
			return "", werr
		}
		return s.rel(path), nil
	}
}

// rel makes path relative to the data dir, with forward slashes.
func (s *Store) rel(path string) string {
	r, err := filepath.Rel(s.dir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(r)
}

// ReadArtifact reads the artifact at rel (a path relative to the data dir, as
// WriteArtifact returns). A file without ADE frontmatter is an error: it
// cannot vouch for what it contains.
func (s *Store) ReadArtifact(rel string) (Artifact, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return Artifact{}, fmt.Errorf("artifact path %q escapes the records dir", rel)
	}
	return readArtifact(filepath.Join(s.dir, clean), rel)
}

func readArtifact(path, rel string) (Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Artifact{}, err
	}
	fm, body, ok := parseFrontmatter(string(data))
	if !ok {
		return Artifact{}, fmt.Errorf("%s: no frontmatter", rel)
	}
	a := Artifact{
		Feature:       fm.Scalars["feature"],
		Profile:       fm.Scalars["profile"],
		Model:         fm.Scalars["model"],
		PromptVersion: fm.Scalars["prompt_version"],
		InputHash:     fm.Scalars["input_hash"],
		AuditID:       fm.Scalars["audit_id"],
		Body:          strings.TrimSuffix(body, "\n"),
		Path:          filepath.ToSlash(rel),
	}
	if toks := fm.Lists["tokens"]; len(toks) == 2 {
		a.TokensIn, _ = strconv.Atoi(toks[0])
		a.TokensOut, _ = strconv.Atoi(toks[1])
	}
	a.CostUSD, _ = strconv.ParseFloat(fm.Scalars["cost_usd"], 64)
	a.Created, _ = time.Parse(time.RFC3339, fm.Scalars["created"])
	return a, nil
}

// Artifacts lists every AI artifact under items/ (any depth, any "ai"
// folder), for rebuilding the result cache after cache.db is lost. Files
// without valid frontmatter or an input hash are skipped.
func (s *Store) Artifacts() ([]Artifact, error) {
	root := filepath.Join(s.dir, "items")
	var out []Artifact
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".md" || filepath.Base(filepath.Dir(p)) != AIDir {
			return nil
		}
		a, rerr := readArtifact(p, s.rel(p))
		if rerr != nil || a.InputHash == "" {
			return nil
		}
		out = append(out, a)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("index artifacts: %w", err)
	}
	return out, nil
}
