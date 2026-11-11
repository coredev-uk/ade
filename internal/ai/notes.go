package ai

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/forge"
)

// maxNotesChars bounds the notes carried into a prompt, like every other
// free-text input.
const maxNotesChars = 4000

// NoteRefs are the item records whose notes belong to work on an MR: the MR's
// own, the issue it closes, and the tracker issue that one names as Parent
// (where start work's user keeps the notes). issue may be nil.
func NoteRefs(mr forge.MR, issue *forge.Issue) []forge.Ref {
	refs := []forge.Ref{mr.Ref}
	if issue != nil {
		refs = append(refs, issue.Ref)
		refs = append(refs, domain.ParentRefs(*issue)...)
	}
	return refs
}

// NotesFor reads the user's notes of each item record, in order, skipping
// empty ones and repeated refs. With several records each block is headed by
// its ref. The result is "" when there are no notes at all.
func (s *Service) NotesFor(forgeName string, refs ...forge.Ref) (string, error) {
	var blocks []string
	seen := map[forge.Ref]bool{}
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		n, err := s.Records.ReadNotes(forgeName, r)
		if err != nil {
			return "", err
		}
		if n = strings.TrimSpace(n); n != "" {
			blocks = append(blocks, fmt.Sprintf("Notes on %s:\n%s", r, clip(n, maxNotesChars)))
		}
	}
	return strings.Join(blocks, "\n\n"), nil
}

// WithNotes adds the user's own notes to the job's prompt. They are part of
// the prompt, so they are part of the cache key: changing the notes buys a
// new answer, and leaving them out (an empty notes) leaves the job as it was.
func WithNotes(j Job, notes string) Job {
	if strings.TrimSpace(notes) == "" {
		return j
	}
	j.Prompt += "\n\nThe author's private notes on this item (background only; do not quote them back):\n" + notes + "\n"
	j.Notes = true
	return j
}
