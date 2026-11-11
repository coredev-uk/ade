package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/editor"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/overview"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/spf13/cobra"
)

// notesEditorCmd is the editor command that opens the item's record at its
// notes (host.editor, else $VISUAL/$EDITOR). A record without the notes
// heading gets one first, so the editor lands there.
func notesEditorCmd(a *app.App, st *records.Store, forgeName string, ref forge.Ref, rec records.Record) (editor.Editor, *exec.Cmd, error) {
	if rec.NotesLine == 0 {
		if err := st.WriteNotes(forgeName, ref, rec.Notes); err != nil {
			return editor.Editor{}, nil, err
		}
		var err error
		if rec, err = st.Read(forgeName, ref); err != nil {
			return editor.Editor{}, nil, err
		}
	}
	ed := editor.Resolve(a.Config.Host.Editor, editor.FromOS())
	return ed, ed.Command(rec.Path, rec.NotesLine, true), nil
}

// watchItem is `ade item <ref> --watch`: the work item that owns ref (its
// tracker issue, a child issue or a merge request), read from the cache. On a
// terminal it is a live pane; anywhere else the view is printed once, in
// plain text, at $COLUMNS (80 without it).
func watchItem(cmd *cobra.Command, a *app.App, ref forge.Ref) error {
	cache, err := a.Cache()
	if err != nil {
		return err
	}
	st := records.New(a.RecordsDir)
	l := &overview.Loader{Cache: cache, Records: st, Ref: ref}

	out := cmd.OutOrStdout()
	if f, ok := out.(*os.File); !ok || !isTerminal(f) || !readerIsTerminal(cmd.InOrStdin()) {
		snap, err := l.Load()
		if errors.Is(err, overview.ErrNotCached) {
			return fmt.Errorf("%s: %w", ref, err)
		}
		if err != nil {
			return err
		}
		width, _ := strconv.Atoi(os.Getenv("COLUMNS"))
		if width <= 0 {
			width = 80
		}
		_, err = lipgloss.Fprint(out, overview.Render(snap, a.Config.Host.Glyphs, width, time.Now()))
		return err
	}
	return overview.Run(cmd.Context(), overview.Options{
		Loader: l,
		Glyphs: a.Config.Host.Glyphs,
		Edit: func(s overview.Snapshot) (*exec.Cmd, error) {
			rec, err := st.Read(s.Forge, s.Item.Ref)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", s.Item.Ref, err)
			}
			_, c, err := notesEditorCmd(a, st, s.Forge, s.Item.Ref, rec)
			return c, err
		},
	})
}
