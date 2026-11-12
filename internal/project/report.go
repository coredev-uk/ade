package project

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/events"
)

// Failure is one operation on a repo that did not work.
type Failure struct {
	Op     string `json:"op"` // clone | fetch | pull | hook
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Skip is a tree ADE deliberately left untouched.
type Skip struct {
	Tree   string `json:"tree"` // "web/main"
	Reason string `json:"reason"`
}

// RepoResult is what one repo's sync did.
type RepoResult struct {
	Project  string    `json:"project"`
	Repo     string    `json:"repo"`
	Cloned   bool      `json:"cloned,omitempty"`
	Pulled   []string  `json:"pulled,omitempty"` // trees fast-forwarded
	Skipped  []Skip    `json:"skipped,omitempty"`
	Failures []Failure `json:"failures,omitempty"`
}

// Report is the outcome of an Add or Update run.
type Report struct {
	Results []RepoResult `json:"repos"`
	// Setup lists the generated project-wide files (AGENTS.md, .ade/shell) this
	// run wrote or removed, and what stopped it writing others.
	Setup []SetupResult `json:"setup,omitempty"`
	// Invalid lists manifest errors; the last good snapshot stayed in use.
	Invalid []string `json:"invalid_manifests,omitempty"`
}

// Failed reports whether any repo operation failed.
func (r Report) Failed() bool {
	for _, res := range r.Results {
		if len(res.Failures) > 0 {
			return true
		}
	}
	return false
}

// Changed reports whether the run did anything worth recording: a clone, a
// fast-forward, a skip, a failure, a generated file written or removed, or a
// rejected manifest. Setup warnings do not count: they repeat on every run.
func (r Report) Changed() bool {
	if len(r.Invalid) > 0 {
		return true
	}
	for _, su := range r.Setup {
		if len(su.Written)+len(su.Removed) > 0 {
			return true
		}
	}
	for _, res := range r.Results {
		if res.Cloned || len(res.Pulled) > 0 || len(res.Skipped) > 0 || len(res.Failures) > 0 {
			return true
		}
	}
	return false
}

// Severity is Error when anything failed, Warn when anything was skipped, a
// project-wide setup step warned or a manifest was rejected, else Success.
func (r Report) Severity() events.Severity {
	switch {
	case r.Failed():
		return events.Error
	case len(r.Invalid) > 0 || r.count(func(res RepoResult) int { return len(res.Skipped) }) > 0 || r.setupCount(func(su SetupResult) int { return len(su.Warnings) }) > 0:
		return events.Warn
	}
	return events.Success
}

func (r Report) count(f func(RepoResult) int) int {
	n := 0
	for _, res := range r.Results {
		n += f(res)
	}
	return n
}

func (r Report) setupCount(f func(SetupResult) int) int {
	n := 0
	for _, su := range r.Setup {
		n += f(su)
	}
	return n
}

// Title is the one-line summary, e.g.
// "pulled 4 repos · 1 skipped (web/main dirty) · 1 failed (api: auth)".
func (r Report) Title() string {
	var parts []string
	cloned := r.count(func(res RepoResult) int { return b2i(res.Cloned) })
	pulled := r.count(func(res RepoResult) int { return b2i(len(res.Pulled) > 0) })
	if cloned > 0 {
		parts = append(parts, "cloned "+plural(cloned, "repo"))
	}
	if pulled > 0 {
		parts = append(parts, "pulled "+plural(pulled, "repo"))
	}
	var skipped, failed []string
	for _, res := range r.Results {
		for _, s := range res.Skipped {
			skipped = append(skipped, s.Tree+" "+s.Reason)
		}
		for _, f := range res.Failures {
			failed = append(failed, res.Repo+": "+f.Reason)
		}
	}
	if len(skipped) > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped (%s)", len(skipped), listed(skipped)))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed (%s)", len(failed), listed(failed)))
	}
	if n := r.setupCount(func(su SetupResult) int { return len(su.Written) + len(su.Removed) }); n > 0 {
		parts = append(parts, "updated "+plural(n, "project file"))
	}
	if n := r.setupCount(func(su SetupResult) int { return len(su.Warnings) }); n > 0 {
		parts = append(parts, plural(n, "setup warning"))
	}
	if len(r.Invalid) > 0 {
		parts = append(parts, plural(len(r.Invalid), "manifest")+" invalid")
	}
	if len(parts) == 0 {
		return "all repos up to date"
	}
	return strings.Join(parts, " · ")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// listed shortens a list of reasons to the first three.
func listed(items []string) string {
	const max = 3
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(items[:max], ", "), len(items)-max)
}
