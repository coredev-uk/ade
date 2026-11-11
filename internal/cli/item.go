package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/review"
)

func init() { register(newItemCmd) }

func newItemCmd() *cobra.Command {
	var edit bool
	var forgeFlag string
	var watch bool
	cmd := &cobra.Command{
		Use:   "item <ref>",
		Short: "Show an item's record: the synced snapshot and your notes (--edit opens the notes)",
		Long: "Every synced item has a markdown record (PLAN 4). Between the ADE markers is the snapshot ADE\n" +
			"rewrites on every sync; below `## Notes` is yours, and ADE never touches it. This prints both.\n" +
			"--edit opens the record in your editor (host.editor, else $VISUAL/$EDITOR) at the notes, and\n" +
			"prints the notes again afterwards. Your notes are added to agent plan/run/address prompts and to\n" +
			"AI summaries, draft replies and reviews (`--no-notes` leaves them out). No network is used.\n" +
			"<ref> is group/project#N (issue) or group/project!N (merge request), or N when one project is\n" +
			"registered. --watch shows the whole work item instead (stage, child issues and their MRs,\n" +
			"description, acceptance, your notes) as a live read-only pane that follows the cache; <ref> may\n" +
			"be the tracker issue, a child issue or an MR. `q` quits, `r` reloads, `e` edits the notes, `j/k`\n" +
			"scroll. Off a terminal it prints the view once, in plain text.",
		Example: "  ade item acme/tracker#15\n  ade item 15 --edit\n  ade item acme/api!3 --json\n  ade item acme/tracker#15 --watch",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := appFrom(cmd)
			ref, err := itemRef(cmd, args[0])
			if err != nil {
				return err
			}
			if watch {
				if edit || jsonOut {
					return errors.New("--watch cannot be combined with --edit or --json")
				}
				return watchItem(cmd, a, ref)
			}
			name, err := review.ForgeFor(cmd.Context(), a, ref, forgeFlag)
			if err != nil {
				return err
			}
			st := records.New(a.RecordsDir)
			rec, err := st.Read(name, ref)
			if err != nil {
				return fmt.Errorf("%s: %w", ref, err)
			}
			if edit {
				ed, c, err := notesEditorCmd(a, st, name, ref, rec)
				if err != nil {
					return err
				}
				c.Stdin, c.Stdout, c.Stderr = os.Stdin, cmd.OutOrStdout(), os.Stderr
				if jsonOut {
					c.Stdout = os.Stderr
				}
				if err := c.Run(); err != nil {
					return fmt.Errorf("editor %s: %w", ed, err)
				}
				if rec, err = st.Read(name, ref); err != nil {
					return err
				}
			}
			return printItem(cmd.OutOrStdout(), ref, name, rec)
		},
	}
	cmd.Flags().BoolVar(&edit, "edit", false, "open the record in your editor at the notes")
	cmd.Flags().BoolVar(&watch, "watch", false, "live read-only overview of the item's work item from the cache (a zellij pane); off a terminal, print it once")
	cmd.Flags().StringVar(&forgeFlag, "forge", "", "forge (from the user config) serving the ref; default: found from registered projects")
	return cmd
}

// itemRef parses an issue or MR ref; a bare number is an issue of the only
// registered project's tracker.
func itemRef(cmd *cobra.Command, arg string) (forge.Ref, error) {
	arg = strings.TrimSpace(arg)
	n, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
	if err != nil {
		return forge.ParseRef(arg)
	}
	a := appFrom(cmd)
	if len(a.Config.Projects) != 1 {
		return forge.Ref{}, fmt.Errorf("%q is ambiguous with %d projects registered: use group/project#N", arg, len(a.Config.Projects))
	}
	m, err := project.FetchManifest(cmd.Context(), a, a.Config.Projects[0])
	if err != nil {
		return forge.Ref{}, err
	}
	if m.Tracker.Project == "" {
		return forge.Ref{}, errors.New("the registered project has no tracker.project")
	}
	return forge.Ref{Project: m.Tracker.Project, IID: n, Kind: forge.KindIssue}, nil
}

// itemOut is the JSON shape of `ade item`.
type itemOut struct {
	Ref         string         `json:"ref"`
	Forge       string         `json:"forge"`
	Path        string         `json:"path"`
	Frontmatter map[string]any `json:"frontmatter"`
	Snapshot    string         `json:"snapshot"`
	Notes       string         `json:"notes"`
}

func printItem(w io.Writer, ref forge.Ref, forgeName string, rec records.Record) error {
	if jsonOut {
		fm := map[string]any{}
		for k, v := range rec.Frontmatter.Scalars {
			fm[k] = v
		}
		for k, v := range rec.Frontmatter.Lists {
			fm[k] = v
		}
		return printJSON(w, itemOut{Ref: ref.String(), Forge: forgeName, Path: rec.Path, Frontmatter: fm, Snapshot: rec.Snapshot, Notes: rec.Notes})
	}
	sc := rec.Frontmatter.Scalars
	head := []string{ref.String()}
	for _, k := range []string{"stage", "court", "iteration"} {
		if v := sc[k]; v != "" {
			head = append(head, k+" "+v)
		}
	}
	if v := sc["weight"]; v != "" {
		head = append(head, "weight "+v)
	}
	if v := sc["synced"]; v != "" {
		head = append(head, "synced "+v)
	}
	fmt.Fprintln(w, strings.Join(head, " · "))
	fmt.Fprintf(w, "record: %s\n\n", rec.Path)
	if rec.Snapshot != "" {
		fmt.Fprintln(w, rec.Snapshot)
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "## Notes")
	if n := strings.TrimSpace(rec.Notes); n != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, n)
	} else {
		fmt.Fprintf(w, "\n(none yet: ade item %s --edit)\n", ref)
	}
	return nil
}
