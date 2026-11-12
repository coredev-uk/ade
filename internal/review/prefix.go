package review

import (
	"path/filepath"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/commitmsg"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/store"
)

// PrefixForMR is the commit prefix (manifest conventions.commit_prefix) that
// applies to the merge of ref: the cached work item that owns the MR supplies
// the parent and child iids, and its project's manifest the template. It is
// "" when there is no cache, no owning item, no manifest or no prefix
// configured, so callers then send nothing and the forge's defaults stand.
func PrefixForMR(a *app.App, st *store.Store, ref forge.Ref) string {
	if a == nil || st == nil {
		return ""
	}
	items, err := forgesync.ItemRefs(st)
	if err != nil {
		return ""
	}
	for _, it := range items {
		if !commitprefix.Owns(it.Item, ref) {
			continue
		}
		m, err := project.LoadManifest(filepath.Join(a.Root, it.Project))
		if err != nil {
			continue
		}
		if p := commitprefix.ForMR(m, it.Item, ref); p != "" {
			return p
		}
	}
	return ""
}

// MergeOptsFor fills the squash commit message of o for mr when a prefix
// applies. A squash commit replaces the MR's commits, so it is the MR's first
// commit and carries the prefix. A merge commit is never prefixed (only the
// first commit of an MR is), so Message stays empty for the forge's default.
// Without a prefix o is returned unchanged.
func MergeOptsFor(o forge.MergeOpts, prefix string, mr forge.MR) forge.MergeOpts {
	if prefix == "" {
		return o
	}
	o.SquashMessage = commitmsg.SquashMessage(prefix, mr.Title)
	return o
}
