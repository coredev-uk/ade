package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/notify"
)

func init() { register(newInboxCmd) }

func openInbox(cmd *cobra.Command) (*notify.Inbox, error) {
	st, err := appFrom(cmd).Cache()
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	return notify.NewInbox(st), nil
}

func newInboxCmd() *cobra.Command {
	var o notify.ListOpts
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "List notifications recorded by ade",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, err := openInbox(cmd)
			if err != nil {
				return err
			}
			items, err := in.List(o)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), inboxJSON(items))
			}
			return renderInbox(cmd.OutOrStdout(), appFrom(cmd), items)
		},
	}
	cmd.Flags().BoolVar(&o.UnreadOnly, "unread", false, "only unread items")
	cmd.Flags().IntVar(&o.Limit, "limit", 0, "show at most N newest items (0 = all)")
	cmd.Flags().StringVar(&o.Project, "project", "", "only items for this project")
	cmd.AddCommand(newInboxReadCmd(), newInboxShowCmd())
	return cmd
}

func newInboxReadCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "read <id>... | --all",
		Short: "Mark inbox items read",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 0) {
				return errors.New("give item ids or --all, not both or neither")
			}
			in, err := openInbox(cmd)
			if err != nil {
				return err
			}
			var n int
			if all {
				n, err = in.MarkAllRead()
			} else {
				n, err = markReadByRef(in, args)
			}
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), map[string]int{"marked": n})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "marked %d read\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "mark every unread item read")
	return cmd
}

// markReadByRef resolves each ref (full id or unique prefix) before marking, so
// a typo fails the whole command instead of half-applying.
func markReadByRef(in *notify.Inbox, refs []string) (int, error) {
	ids := make([]string, len(refs))
	for i, r := range refs {
		it, err := in.Get(r)
		if err != nil {
			return 0, err
		}
		ids[i] = it.ID
	}
	return in.MarkRead(ids...)
}

func newInboxShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one inbox item in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := openInbox(cmd)
			if err != nil {
				return err
			}
			it, err := in.Get(args[0])
			if errors.Is(err, sql.ErrNoRows) {
				return withCode(2, err)
			}
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), toInboxJSON(it))
			}
			renderItem(cmd.OutOrStdout(), it)
			return nil
		},
	}
}

type inboxItemJSON struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Project  string    `json:"project,omitempty"`
	Subject  string    `json:"subject,omitempty"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail,omitempty"`
	Link     string    `json:"link,omitempty"`
	Read     bool      `json:"read"`
}

func toInboxJSON(it notify.Item) inboxItemJSON {
	return inboxItemJSON{it.ID, it.Time, it.Kind, it.Severity.String(), it.Project, it.Subject, it.Title, it.Detail, it.Link, it.Read}
}

func inboxJSON(items []notify.Item) []inboxItemJSON {
	out := make([]inboxItemJSON, len(items)) // non-nil so empty prints []
	for i, it := range items {
		out[i] = toInboxJSON(it)
	}
	return out
}

func renderInbox(w io.Writer, a *app.App, items []notify.Item) error {
	if len(items) == 0 {
		fmt.Fprintln(w, "inbox is empty")
		return nil
	}
	ascii := a.Config.Host.Glyphs == "ascii"
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, it := range items {
		mark := " "
		if !it.Read {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n", mark, it.ID, it.Time.Local().Format("01-02 15:04"),
			severityLabel(it.Severity, ascii), it.Kind, it.Title)
	}
	return tw.Flush()
}

func severityLabel(s events.Severity, ascii bool) string {
	glyph := map[events.Severity][2]string{
		events.Info:    {"ℹ", "i"},
		events.Success: {"✓", "+"},
		events.Warn:    {"⚠", "!"},
		events.Error:   {"✗", "x"},
	}[s]
	g := glyph[0]
	if ascii {
		g = glyph[1]
	}
	return g + " " + s.String()
}

func renderItem(w io.Writer, it notify.Item) {
	fmt.Fprintf(w, "%s  %s\n", it.Title, severityLabel(it.Severity, false))
	fmt.Fprintf(w, "id:      %s\nkind:    %s\ntime:    %s\n", it.ID, it.Kind, it.Time.Local().Format(time.RFC3339))
	for _, f := range [][2]string{{"project", it.Project}, {"subject", it.Subject}, {"link", it.Link}} {
		if f[1] != "" {
			fmt.Fprintf(w, "%-8s %s\n", f[0]+":", f[1])
		}
	}
	if it.Detail != "" {
		fmt.Fprintf(w, "\n%s\n", it.Detail)
	}
}
