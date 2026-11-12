package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/review"
)

func init() { register(newNoteCmd) }

func newNoteCmd() *cobra.Command {
	var blocked bool
	var forgeFlag string
	cmd := &cobra.Command{
		Use:   "note <ref> [text...]",
		Short: "Leave a local note on an item (what stops it, what you last did), or print its notes",
		Long: "With text, adds a dated line to the item's notes (`## Notes` in its record): `- 2026-11-12 14:20 text`.\n" +
			"--blocked marks it `[blocked]`: the dashboard flags the item until a later note that is not blocked.\n" +
			"Without text, prints the dated notes, newest first. Notes stay on this machine; nothing goes to the\n" +
			"forge. They are the same notes `ade item --edit` and the dashboard's `n` show. An item with no record\n" +
			"yet gets one from the cache. <ref> is group/project#N, or N when one project is registered.",
		Example: "  ade note acme/tracker#15 waiting for the API key from support --blocked\n  ade note 15 key arrived, wiring it up\n  ade note 15 --json",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			ref, err := itemRef(cmd, args[0])
			if err != nil {
				return err
			}
			name, err := review.ForgeFor(cmd.Context(), a, ref, forgeFlag)
			if err != nil {
				return err
			}
			st := records.New(a.RecordsDir)
			if text := strings.TrimSpace(strings.Join(args[1:], " ")); text != "" {
				if err := appendNote(a, st, name, ref, text, blocked); err != nil {
					return err
				}
			} else if blocked {
				return errors.New("--blocked needs the note's text")
			}
			text, err := st.ReadNotes(name, ref)
			if err != nil {
				return err
			}
			return printNotes(cmd.OutOrStdout(), ref, name, records.ParseNotes(text))
		},
	}
	cmd.Flags().BoolVar(&blocked, "blocked", false, "mark the note as what is blocking the item")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", "forge (from the user config) serving the ref; default: found from registered projects")
	return cmd
}

// appendNote adds the note, making the record from the cached item first when
// the item has none: notes do not wait for a sync.
func appendNote(a *app.App, st *records.Store, forgeName string, ref forge.Ref, text string, blocked bool) error {
	now := time.Now().In(ai.Location(a.Config))
	err := st.AppendNote(forgeName, ref, text, now, blocked)
	if !errors.Is(err, records.ErrNoRecord) {
		return err
	}
	cache, cerr := a.Cache()
	if cerr != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	results, cerr := forgesync.Load(cache)
	if cerr != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	for _, r := range results {
		for _, wi := range r.Items {
			if wi.Ref != ref {
				continue
			}
			if _, _, werr := st.WriteSnapshot(forgeName, wi, nil, a.Config.Records.SnapshotComments); werr != nil {
				return werr
			}
			return st.AppendNote(forgeName, ref, text, now, blocked)
		}
	}
	return fmt.Errorf("%s: %w", ref, err)
}

// noteOut is the JSON shape of `ade note`.
type noteOut struct {
	Ref     string     `json:"ref"`
	Forge   string     `json:"forge"`
	Blocked bool       `json:"blocked"` // the newest note says so
	Notes   []noteJSON `json:"notes"`   // dated notes, newest first
}

type noteJSON struct {
	At      string `json:"at"`
	Blocked bool   `json:"blocked"`
	Text    string `json:"text"`
}

func printNotes(w io.Writer, ref forge.Ref, forgeName string, notes []records.Note) error {
	if jsonOut {
		out := noteOut{Ref: ref.String(), Forge: forgeName, Blocked: records.Blocked(notes), Notes: []noteJSON{}}
		for _, n := range notes {
			out.Notes = append(out.Notes, noteJSON{At: n.At.Format("2006-01-02 15:04"), Blocked: n.Blocked, Text: n.Text})
		}
		return printJSON(w, out)
	}
	if len(notes) == 0 {
		fmt.Fprintf(w, "%s: no notes yet (ade note %s <text> [--blocked])\n", ref, ref)
		return nil
	}
	fmt.Fprintln(w, ref.String())
	for _, n := range notes {
		tag := ""
		if n.Blocked {
			tag = "[blocked] "
		}
		fmt.Fprintf(w, "  %s  %s%s\n", n.At.Format("2006-01-02 15:04"), tag, n.Text)
	}
	return nil
}
