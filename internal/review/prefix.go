package review

import (
	"github.com/coredev-uk/ade/internal/commitmsg"
	"github.com/coredev-uk/ade/internal/forge"
)

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
