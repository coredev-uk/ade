package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/audit"
)

func init() { register(newAuditCmd) }

func newAuditCmd() *cobra.Command {
	var (
		since string
		f     audit.Filter
	)
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show the action audit log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if since != "" {
				t, err := parseSince(since, time.Now())
				if err != nil {
					return err
				}
				f.Since = t
			}
			entries, err := appFrom(cmd).Audit.Query(f)
			if err != nil {
				return fmt.Errorf("read audit log: %w", err)
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				if entries == nil {
					entries = []audit.Entry{}
				}
				return printJSON(w, entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(w, "no matching audit entries")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.TS.Local().Format("2006-01-02 15:04:05"), e.Actor, e.Action, auditSubject(e), auditOutcome(e))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "only entries after this: 1d, 2w, 6h, or an RFC3339 time")
	cmd.Flags().StringVar(&f.Action, "action", "", "action glob, e.g. 'git.*'")
	cmd.Flags().StringVar(&f.Item, "item", "", "only entries for this item reference")
	cmd.Flags().StringVar(&f.Project, "project", "", "only entries for this project")
	cmd.Flags().IntVar(&f.Limit, "limit", 0, "show at most the newest N entries (0 = all)")
	cmd.AddCommand(newAuditTailCmd())
	return cmd
}

// auditPoll is how often `audit tail --follow` looks for new lines.
const auditPoll = 250 * time.Millisecond

func newAuditTailCmd() *cobra.Command {
	var (
		f      audit.Filter
		lines  int
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Show the newest audit entries, optionally following new ones",
		Long: "Prints the newest entries (default 10) oldest-first, one per line. With --follow it keeps\n" +
			"running and prints entries as any ade process appends them, until interrupted. With --json\n" +
			"every entry is one JSON object per line (JSONL).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l := appFrom(cmd).Audit
			w := cmd.OutOrStdout()
			print := func(e audit.Entry) error {
				if jsonOut {
					return json.NewEncoder(w).Encode(e)
				}
				_, err := fmt.Fprintln(w, auditLine(e))
				return err
			}
			if lines > 0 { // Query's limit 0 would mean everything
				f.Limit = lines
				entries, err := l.Query(f)
				if err != nil {
					return fmt.Errorf("read audit log: %w", err)
				}
				for _, e := range entries {
					if err := print(e); err != nil {
						return err
					}
				}
			}
			if !follow {
				return nil
			}
			f.Limit = 0
			var werr error
			err := l.Follow(cmd.Context(), f, auditPoll, func(e audit.Entry) {
				if werr == nil {
					werr = print(e)
				}
			})
			return errors.Join(err, werr)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 10, "how many of the newest entries to print first")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep running and print new entries as they are appended")
	cmd.Flags().StringVar(&f.Action, "action", "", "action glob, e.g. 'forge.*'")
	cmd.Flags().StringVar(&f.Item, "item", "", "only entries for this item reference")
	cmd.Flags().StringVar(&f.Project, "project", "", "only entries for this project")
	return cmd
}

// auditLine is one entry as a single line.
func auditLine(e audit.Entry) string {
	return strings.Join([]string{e.TS.Local().Format("2006-01-02 15:04:05"), e.Actor, e.Action, auditSubject(e), auditOutcome(e)}, "  ")
}

func auditSubject(e audit.Entry) string {
	var parts []string
	for _, s := range []string{e.Project, e.Item, e.Target} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

func auditOutcome(e audit.Entry) string {
	out := e.Outcome
	if e.DryRun {
		out += " (dry-run)"
	}
	if e.Error != "" {
		out += ": " + e.Error
	}
	return out
}

// parseSince accepts a relative age (<n>m|h|d|w) or an RFC3339 timestamp.
func parseSince(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if len(s) >= 2 {
		n, err := strconv.Atoi(s[:len(s)-1])
		unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
		if err == nil && n >= 0 && unit != 0 {
			return now.Add(-time.Duration(n) * unit), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --since %q: want 30m, 12h, 1d, 2w or RFC3339", s)
}
