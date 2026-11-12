package domain

import (
	"regexp"
	"strconv"

	"github.com/coredev-uk/ade/internal/forge"
)

// parentRe finds "Parent: group/proj#N" and "Part of group/proj#N" (also with
// markdown emphasis) and the same with a full issue URL.
var parentRe = regexp.MustCompile(
	`(?i)\b(?:parent|part of)\b[ \t:*_]*(?:https?://[^/\s]+/([\w./-]+?)/(?:-/)?issues/(\d+)|([\w.-]+(?:/[\w.-]+)+)#(\d+))`)

// ParentRefs returns the tracker issues a child's description names as its
// parent, in order of appearance without duplicates.
func ParentRefs(child forge.Issue) []forge.Ref {
	var out []forge.Ref
	for _, m := range parentRe.FindAllStringSubmatch(child.Description, -1) {
		project, num := m[3], m[4]
		if project == "" {
			project, num = m[1], m[2]
		}
		iid, err := strconv.Atoi(num)
		if err != nil || iid <= 0 {
			continue
		}
		ref := forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
		if !containsRef(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// Parents returns the tracker issues child belongs to: the parent a forge
// found for it (extra: the hierarchy parent, or a relates_to link to the
// tracker), then those its description names. Each appears once.
func Parents(child forge.Issue, extra map[forge.Ref]forge.Ref) []forge.Ref {
	var out []forge.Ref
	if p, ok := extra[child.Ref]; ok {
		out = append(out, p)
	}
	for _, p := range ParentRefs(child) {
		if !containsRef(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// IsChildLink reports whether a tracker's link points at a child issue: the
// sandbox pattern links children with relates_to; sub_issue is the native
// hierarchy. blocks/is_blocked_by/parent never denote children.
func IsChildLink(l forge.Link) bool {
	return l.Ref.Kind == forge.KindIssue && (l.Type == "relates_to" || l.Type == "sub_issue")
}

func containsRef(refs []forge.Ref, r forge.Ref) bool {
	for _, x := range refs {
		if x == r {
			return true
		}
	}
	return false
}

// children selects the candidates that belong to tracker: linked by a child
// link from it, or having it as a parent (see Parents). Each is returned once,
// in candidate order; the tracker itself never counts.
func children(tracker forge.Issue, links []forge.Link, parents map[forge.Ref]forge.Ref, candidates []forge.Issue) []forge.Issue {
	linked := make([]forge.Ref, 0, len(links))
	for _, l := range links {
		if IsChildLink(l) {
			linked = append(linked, l.Ref)
		}
	}
	var out []forge.Issue
	var seen []forge.Ref
	for _, c := range candidates {
		if c.Ref == tracker.Ref || containsRef(seen, c.Ref) {
			continue
		}
		if containsRef(linked, c.Ref) || containsRef(Parents(c, parents), tracker.Ref) {
			out = append(out, c)
			seen = append(seen, c.Ref)
		}
	}
	return out
}
