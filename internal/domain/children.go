package domain

import (
	"fmt"
	"regexp"

	"github.com/coredev-uk/ade/internal/forge"
)

// ChildMRs is a child issue with the merge requests that work on it. Child is
// nil for the trailing group of MRs no child issue claims.
type ChildMRs struct {
	Child *forge.Issue
	MRs   []forge.MR
}

// MRsByChild files each of the item's MRs under the child issue it works on,
// in child order; MRs no child claims follow in a group with a nil Child.
// Children without an MR still get a group. Child points into wi.Children.
func (wi *WorkItem) MRsByChild() []ChildMRs {
	return groupMRs(wi.Children, wi.MRs)
}

// groupMRs files each merge request under the child issue of the same repo.
// With several children in one repo an MR goes to the one its branch, title
// or description names (#N); one that names none stays with the repo's first
// child.
func groupMRs(kids []forge.Issue, mrs []forge.MR) []ChildMRs {
	used := make([]bool, len(mrs))
	var groups []ChildMRs
	for ci := range kids {
		c := &kids[ci]
		siblings := 0
		for _, o := range kids {
			if o.Ref.Project == c.Ref.Project {
				siblings++
			}
		}
		grp := ChildMRs{Child: c}
		for i, mr := range mrs {
			if used[i] || mr.Ref.Project != c.Ref.Project {
				continue
			}
			if siblings > 1 && !mentions(mr, c.Ref.IID) && (!mentionsNone(mr, kids) || !firstInProject(kids, ci)) {
				continue
			}
			used[i] = true
			grp.MRs = append(grp.MRs, mr)
		}
		groups = append(groups, grp)
	}
	var rest ChildMRs
	for i, mr := range mrs {
		if !used[i] {
			rest.MRs = append(rest.MRs, mr)
		}
	}
	if len(rest.MRs) > 0 {
		groups = append(groups, rest)
	}
	return groups
}

func firstInProject(kids []forge.Issue, i int) bool {
	for j := range i {
		if kids[j].Ref.Project == kids[i].Ref.Project {
			return false
		}
	}
	return true
}

func mentions(mr forge.MR, iid int) bool {
	re := regexp.MustCompile(`(?:^|\D)` + fmt.Sprint(iid) + `(?:\D|$)`)
	return re.MatchString(mr.SourceBranch) || re.MatchString(mr.Title) || re.MatchString(mr.Description)
}

func mentionsNone(mr forge.MR, kids []forge.Issue) bool {
	for _, k := range kids {
		if k.Ref.Project == mr.Ref.Project && mentions(mr, k.Ref.IID) {
			return false
		}
	}
	return true
}
