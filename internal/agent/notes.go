package agent

import (
	"fmt"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/records"
)

// maxNotesBytes bounds the user's notes carried into a prompt.
const maxNotesBytes = 8 << 10

// AddNotes reads the user's own notes from the item records of the tracker
// issue and its child and adds them to c (PLAN 4: notes feed plan, run and
// address prompts). With several records each block is headed by its ref. A
// caller told --no-notes (or whose user toggled them off) does not call it.
func (c *Context) AddNotes(st *records.Store, t Target) error {
	refs := []forge.Ref{t.Tracker}
	if t.Child != nil && *t.Child != t.Tracker {
		refs = append(refs, *t.Child)
	}
	var blocks []string
	for _, r := range refs {
		n, err := st.ReadNotes(t.Forge, r)
		if err != nil {
			return err
		}
		if n = clipNotes(n); n != "" {
			blocks = append(blocks, n)
			if len(refs) > 1 {
				blocks[len(blocks)-1] = fmt.Sprintf("Notes on %s:\n%s", r, n)
			}
		}
	}
	c.Notes = strings.Join(blocks, "\n\n")
	return nil
}

func clipNotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxNotesBytes {
		s = s[:maxNotesBytes] + "\n[truncated]"
	}
	return s
}
