package config

// DefaultMaxConcurrency is how many API requests to one forge may be in
// flight at once when `[forges.x] max_concurrency` is not set. Self-managed
// instances have their own limits (or none), so it is configurable.
const DefaultMaxConcurrency = 4

// Concurrency is the in-flight request cap for the forge: max_concurrency, or
// the default for hand-built configs that did not go through Load.
func (f Forge) Concurrency() int {
	if f.MaxConcurrency > 0 {
		return f.MaxConcurrency
	}
	return DefaultMaxConcurrency
}
