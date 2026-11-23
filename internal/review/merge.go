package review

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/commitprefix"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/store"
)

// MergeVerdict says whether a merge can go ahead, and why not. The merge
// dialog and `ade mr merge` read the same one.
type MergeVerdict struct {
	OK     bool
	AutoOK bool   // not mergeable yet, but "merge when the pipeline succeeds" is
	Why    string // why not, as the dialog words it
}

// Verdict judges mr from what the forge reports: state, draft, conflicts and
// the detailed merge status (unresolved threads, approvals, pipeline).
func Verdict(mr forge.MR) MergeVerdict {
	switch {
	case mr.State == "merged":
		return MergeVerdict{Why: "already merged"}
	case mr.State != "opened":
		return MergeVerdict{Why: "the merge request is " + mr.State}
	case mr.Draft:
		return MergeVerdict{Why: "it is still a draft (R marks it ready)"}
	case mr.HasConflicts:
		return MergeVerdict{Why: "it conflicts with " + mr.TargetBranch + ": rebase first"}
	}
	switch mr.DetailedStatus {
	case "", "mergeable":
		return MergeVerdict{OK: true}
	case "ci_still_running":
		return MergeVerdict{AutoOK: true, Why: "the pipeline is still running"}
	case "ci_must_pass":
		return MergeVerdict{AutoOK: mr.Pipeline != nil && mr.Pipeline.Status != "failed", Why: "the pipeline must pass first"}
	case "not_approved":
		return MergeVerdict{Why: "it needs approval"}
	case "discussions_not_resolved":
		word := "threads"
		if mr.Unresolved == 1 {
			word = "thread"
		}
		return MergeVerdict{Why: fmt.Sprintf("%d unresolved %s", mr.Unresolved, word)}
	case "need_rebase":
		return MergeVerdict{Why: "it needs a rebase"}
	case "checking", "unchecked":
		return MergeVerdict{Why: "GitLab is still checking whether it can merge: try again in a moment"}
	case "blocked_status":
		return MergeVerdict{Why: "another merge request blocks it"}
	}
	return MergeVerdict{Why: "GitLab reports " + strings.ReplaceAll(mr.DetailedStatus, "_", " ")}
}

// MergePolicy is what the owning project's manifest says about merging an MR.
// The zero value (no cache, no owning item, no manifest) means no policy.
type MergePolicy struct {
	// Prefix is the expanded conventions.commit_prefix; "" when none applies.
	Prefix string
	// Squash and RemoveSourceBranch are conventions.mr.squash and
	// remove_source_branch; false when the manifest does not say.
	Squash, RemoveSourceBranch bool
}

// MergePolicyFor reads the merge policy for ref: the cached work item that
// owns the MR supplies the parent and child iids, and its project's manifest
// the conventions. An MR ADE has not synced has no policy, so callers then
// send nothing and the forge's defaults stand.
func MergePolicyFor(a *app.App, st *store.Store, ref forge.Ref) MergePolicy {
	if a == nil || st == nil {
		return MergePolicy{}
	}
	items, err := forgesync.ItemRefs(st)
	if err != nil {
		return MergePolicy{}
	}
	for _, it := range items {
		if !commitprefix.Owns(it.Item, ref) {
			continue
		}
		m, err := project.LoadManifest(filepath.Join(a.Root, it.Project))
		if err != nil {
			continue
		}
		c := m.Conventions.MR
		return MergePolicy{
			Prefix:             commitprefix.ForMR(m, it.Item, ref),
			Squash:             c.Squash != nil && *c.Squash,
			RemoveSourceBranch: c.RemoveSourceBranch != nil && *c.RemoveSourceBranch,
		}
	}
	return MergePolicy{}
}
