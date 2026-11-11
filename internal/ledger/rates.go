package ledger

import (
	"sort"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/audit"
)

// Decision actions audited when an agent's review draft is triaged.
const (
	DecisionAccept = "review.comment.accept"
	DecisionEdit   = "review.comment.edit"
	DecisionDeny   = "review.comment.deny"
	DecisionDelete = "review.comment.delete" // an agent draft removed unsent counts as denied
	// DraftAction is audited for every draft an agent review pass produced:
	// Target is the draft id, params carry the profile. Decisions audit the
	// draft id as Target too.
	DraftAction = "review.comment.draft"
)

// Rates counts how one profile's review drafts were triaged. A draft is
// counted once, by its last decision; Pending are drafts nobody decided on.
type Rates struct {
	Profile  string `json:"profile"`
	Drafts   int    `json:"drafts"`
	Accepted int    `json:"accepted"`
	Edited   int    `json:"edited"`
	Denied   int    `json:"denied"`
	Pending  int    `json:"pending"`
}

// Decided is the number of drafts someone triaged.
func (r Rates) Decided() int { return r.Accepted + r.Edited + r.Denied }

// ReviewRates joins review passes to triage decisions through the draft ids
// the pass recorded, so a noisy reviewer model shows. Only audit entries
// since `since` (zero: all) are read.
func ReviewRates(log *audit.Log, since time.Time) ([]Rates, error) {
	entries, err := log.Query(audit.Filter{Since: since, Action: "review.comment.*"})
	if err != nil {
		return nil, err
	}
	profileOf := map[string]string{} // draft id -> profile
	for _, e := range entries {
		if e.Action != DraftAction {
			continue
		}
		profile, _ := e.Params["profile"].(string)
		if e.Target != "" {
			profileOf[e.Target] = profile
		}
	}
	last := map[string]string{} // draft id -> last decision action
	for _, e := range entries {
		if !strings.HasPrefix(e.Action, "review.comment.") || e.Action == DraftAction {
			continue
		}
		if e.Target != "" {
			last[e.Target] = e.Action
		}
	}
	byProfile := map[string]*Rates{}
	get := func(p string) *Rates {
		r := byProfile[p]
		if r == nil {
			r = &Rates{Profile: p}
			byProfile[p] = r
		}
		return r
	}
	for id, p := range profileOf {
		r := get(p)
		r.Drafts++
		switch last[id] {
		case DecisionAccept:
			r.Accepted++
		case DecisionEdit:
			r.Edited++
		case DecisionDeny, DecisionDelete:
			r.Denied++
		default:
			r.Pending++
		}
	}
	out := make([]Rates, 0, len(byProfile))
	for _, r := range byProfile {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile < out[j].Profile })
	return out, nil
}
