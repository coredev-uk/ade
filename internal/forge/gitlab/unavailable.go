package gitlab

import (
	"sort"
	"sync"
)

// FeatureApprovals is the MR approvals endpoint (GET .../approvals). Very old
// or reduced instances answer 404; the MR then has no approval state.
const FeatureApprovals Feature = "mr-approvals"

// featureName is what a user reads for f in "not available on this GitLab: …".
func featureName(f Feature) string {
	for _, r := range Requirements {
		if r.Feature == f {
			return r.Name
		}
	}
	if f == FeatureApprovals {
		return "merge request approvals"
	}
	return string(f)
}

// unavailableSet remembers which gated features this instance did not offer.
// Recording is idempotent, so a feature that 404s for every MR of a sync is
// reported once.
type unavailableSet struct {
	mu sync.Mutex
	m  map[Feature]bool
}

func (s *unavailableSet) add(f Feature) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[Feature]bool{}
	}
	s.m[f] = true
}

func (s *unavailableSet) has(f Feature) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[f]
}

func (s *unavailableSet) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.m))
	for f := range s.m {
		out = append(out, featureName(f))
	}
	sort.Strings(out)
	return out
}

// markUnavailable records that the instance does not offer f.
func (c *Client) markUnavailable(f Feature) { c.missing.add(f) }

// Unavailable lists, sorted and without repeats, the features this client
// found missing on the instance so far (an endpoint answered 404, or a GraphQL
// field the schema lacks). Callers report them once per run as "not available
// on this GitLab: <feature>"; the driver itself carries on without them.
func (c *Client) Unavailable() []string { return c.missing.list() }
