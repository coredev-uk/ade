package store

import (
	"encoding/json"
	"time"
)

// stamped is a cached value with the time it was stored.
type stamped struct {
	At time.Time       `json:"at"`
	V  json.RawMessage `json:"v"`
}

// Cached returns the value stored under key in the kv table when it is
// younger than ttl, else calls fetch and stores the result. An unreadable
// entry counts as a miss; a failed store does not fail the fetch.
func Cached[T any](st *Store, key string, ttl time.Duration, now time.Time, fetch func() (T, error)) (T, error) {
	if v, ok := Peek[T](st, key, ttl, now); ok {
		return v, nil
	}
	v, err := fetch()
	if err != nil {
		var zero T
		return zero, err
	}
	Put(st, key, now, v)
	return v, nil
}

// Peek reads the value Cached stores under key when it is younger than ttl.
func Peek[T any](st *Store, key string, ttl time.Duration, now time.Time) (v T, ok bool) {
	var raw string
	if err := st.DB.QueryRow(`SELECT v FROM kv WHERE k = ?`, key).Scan(&raw); err != nil {
		return v, false
	}
	var s stamped
	if json.Unmarshal([]byte(raw), &s) != nil || now.Sub(s.At) >= ttl || json.Unmarshal(s.V, &v) != nil {
		var zero T
		return zero, false
	}
	return v, true
}

// Put stores v under key as Cached does; a failed store is ignored, the cache
// being disposable.
func Put[T any](st *Store, key string, now time.Time, v T) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if b, err = json.Marshal(stamped{At: now, V: b}); err != nil {
		return
	}
	st.DB.Exec(`INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, key, string(b)) //nolint:errcheck // cache only
}
