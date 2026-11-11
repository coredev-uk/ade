package bootstrap

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// trackerName is how teams call the repository that holds their planning
// issues.
var trackerName = regexp.MustCompile(`(?i)(^|[-_.])(tracker|issues|planning)([-_.]|$)`)

// Candidate is a repository considered as the tracker.
type Candidate struct {
	Repo      forge.Repo
	AppIssues int  // issues carrying an app-like label
	NameHit   bool // named tracker, issues or planning
}

// PickTracker chooses the tracker among repos by the issues that carry
// app-like labels and by name. It returns the winner's index, or -1 and the
// indexes worth asking about when the choice is the user's:
//
//   - one repo has the most app-labelled issues, and either no repo or that
//     one is named like a tracker: that one;
//   - no repo has app-labelled issues, and one is named like a tracker: that one;
//   - several tie on the count: the one named like a tracker, if exactly one is.
func PickTracker(cands []Candidate) (winner int, ambiguous []int) {
	most := 0
	for _, c := range cands {
		most = max(most, c.AppIssues)
	}
	var top, named []int
	for i, c := range cands {
		if most > 0 && c.AppIssues == most {
			top = append(top, i)
		}
		if c.NameHit {
			named = append(named, i)
		}
	}
	switch {
	case len(top) == 1 && (len(named) == 0 || slices.Contains(named, top[0])):
		return top[0], nil
	case len(top) == 0 && len(named) == 1:
		return named[0], nil
	case len(top) > 1:
		if both := intersect(top, named); len(both) == 1 {
			return both[0], nil
		}
	}
	return -1, slices.Compact(slices.Sorted(slices.Values(append(top, named...))))
}

func intersect(a, b []int) []int {
	var out []int
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// findRepo resolves a --tracker value to one of repos: the full path, or the
// repository name when it is unique.
func findRepo(repos []forge.Repo, want string) (forge.Repo, error) {
	var byName []forge.Repo
	for _, r := range repos {
		if strings.EqualFold(r.Path, want) {
			return r, nil
		}
		if strings.EqualFold(r.Name, want) {
			byName = append(byName, r)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		names := make([]string, len(repos))
		for i, r := range repos {
			names[i] = r.Path
		}
		return forge.Repo{}, fmt.Errorf("tracker %q is not a repository of the group (have: %s)", want, strings.Join(names, ", "))
	default:
		return forge.Repo{}, fmt.Errorf("tracker %q names %d repositories: give the full path", want, len(byName))
	}
}
