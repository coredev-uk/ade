package cli

import (
	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/forge"
)

// bindNotes adds --no-notes to the AI commands whose input carries notes
// (summarize, draft-reply, review).
func (o *aiOpts) bindNotes(c *cobra.Command) {
	c.Flags().BoolVar(&o.noNotes, "no-notes", false, "leave your notes on the item out of the prompt (they are included by default, and are part of the cache key)")
}

// withNotes adds the user's notes on the refs to an AI job unless --no-notes.
func (e *aiEnv) withNotes(j ai.Job, o aiOpts, refs ...forge.Ref) (ai.Job, error) {
	if o.noNotes {
		return j, nil
	}
	notes, err := e.svc.NotesFor(e.forgeName, refs...)
	if err != nil {
		return j, err
	}
	return ai.WithNotes(j, notes), nil
}

// notesMark is the footer words for an answer whose prompt carried notes.
func notesMark(o aiOut) string {
	if o.IncludesNotes {
		return " · includes your notes"
	}
	return ""
}
