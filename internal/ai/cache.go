package ai

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"strconv"
	"time"

	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
)

func init() {
	store.Register("ai.cache", `CREATE TABLE ai_cache (
		input_hash TEXT PRIMARY KEY,
		artifact   TEXT NOT NULL,  -- path relative to the records dir
		feature    TEXT NOT NULL,
		model      TEXT NOT NULL,
		created    TEXT NOT NULL
	)`)
}

// InputHash is the cache key: a content hash of what determines the output.
// Each part is length-prefixed, so ("ab","c") and ("a","bc") differ.
func InputHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p)) + ":"))
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Cache maps an input hash to the artifact that holds its answer. The
// artifacts in the item records are the truth; this table is only an index
// over them, rebuilt by Reindex after cache.db is lost.
type Cache struct {
	db  *sql.DB
	rec *records.Store
}

// NewCache wraps an open cache.db and the records the artifacts live in.
func NewCache(st *store.Store, rec *records.Store) *Cache { return &Cache{db: st.DB, rec: rec} }

// Get returns the cached artifact for hash. An index row whose file is gone
// or unreadable is dropped and reported as a miss.
func (c *Cache) Get(hash string) (records.Artifact, bool, error) {
	var path string
	err := c.db.QueryRow(`SELECT artifact FROM ai_cache WHERE input_hash = ?`, hash).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return records.Artifact{}, false, nil
	}
	if err != nil {
		return records.Artifact{}, false, err
	}
	a, err := c.rec.ReadArtifact(path)
	if err != nil {
		if _, derr := c.db.Exec(`DELETE FROM ai_cache WHERE input_hash = ?`, hash); derr != nil {
			return records.Artifact{}, false, derr
		}
		if errors.Is(err, fs.ErrNotExist) {
			return records.Artifact{}, false, nil
		}
		return records.Artifact{}, false, nil // unreadable: treat as a miss, the answer gets re-bought
	}
	if a.InputHash != hash {
		return records.Artifact{}, false, nil // the file was replaced by something else
	}
	return a, true, nil
}

// Put records that artifact path answers hash; the newest artifact wins.
func (c *Cache) Put(hash, path, feature, model string, created time.Time) error {
	_, err := c.db.Exec(`INSERT INTO ai_cache (input_hash, artifact, feature, model, created) VALUES (?,?,?,?,?)
		ON CONFLICT(input_hash) DO UPDATE SET artifact = excluded.artifact, feature = excluded.feature,
		model = excluded.model, created = excluded.created`,
		hash, path, feature, model, created.UTC().Format(time.RFC3339))
	return err
}

// Reindex rebuilds the whole index from the artifacts on disk and returns
// how many answers it found. Nothing is paid for twice after a cache reset.
func (c *Cache) Reindex() (int, error) {
	arts, err := c.rec.Artifacts()
	if err != nil {
		return 0, err
	}
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(`DELETE FROM ai_cache`); err != nil {
		return 0, err
	}
	// Artifacts come oldest first (their names start with a timestamp), so a
	// later duplicate of a hash replaces the earlier one.
	for _, a := range arts {
		if _, err := tx.Exec(`INSERT INTO ai_cache (input_hash, artifact, feature, model, created) VALUES (?,?,?,?,?)
			ON CONFLICT(input_hash) DO UPDATE SET artifact = excluded.artifact, feature = excluded.feature,
			model = excluded.model, created = excluded.created`,
			a.InputHash, a.Path, a.Feature, a.Model, a.Created.UTC().Format(time.RFC3339)); err != nil {
			return 0, err
		}
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM ai_cache`).Scan(&n); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
