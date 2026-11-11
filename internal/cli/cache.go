package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/ai"
	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/ledger"
	"github.com/coredev-uk/ade/internal/records"
	"github.com/coredev-uk/ade/internal/store"
	"github.com/coredev-uk/ade/internal/workspace"
)

func init() { register(newCacheCmd) }

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cache", Short: "Manage the disposable cache.db"}
	cmd.AddCommand(newCacheResetCmd())
	return cmd
}

// cacheLoss is what deleting cache.db costs beyond a re-sync (PLAN 4.1).
type cacheLoss struct {
	Path               string `json:"path"`
	ReviewDrafts       int    `json:"review_drafts"`        // unsubmitted review comments live only here
	UnreadNotification int    `json:"unread_notifications"` // read/unread state
	RunningInstances   int    `json:"running_instances"`    // workspace services ADE would stop tracking
}

func (l cacheLoss) text() string {
	return fmt.Sprintf("%s: %s, %s",
		l.Path, plural(l.ReviewDrafts, "unsubmitted review draft"), plural(l.UnreadNotification, "unread notification"))
}

// cacheReset is the outcome of `ade cache reset`.
type cacheReset struct {
	cacheLoss
	DryRun        bool `json:"dry_run,omitempty"`
	Reset         bool `json:"reset"`
	AIResults     int  `json:"ai_results"`
	LedgerEntries int  `json:"ledger_entries"`
}

func newCacheResetCmd() *cobra.Command {
	var yes, dry bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete cache.db and rebuild what can be rebuilt without the network",
		Long: "cache.db is disposable (PLAN 4.1): forge data comes back with the next `ade sync`, and the\n" +
			"indexes that cost money, the AI result cache and the spend ledger, are rebuilt right away from\n" +
			"the item records and the audit log (what `ade reindex` does), so nothing is paid for twice.\n" +
			"What is lost: read/unread notification state and unsubmitted review drafts, so the command\n" +
			"says how many there are and asks first (--yes skips the question). It refuses while workspace\n" +
			"instances are running: stop them with `ade ws down`. Quit the dashboard first.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := resetCache(cmd.Context(), appFrom(cmd), cacheOptions{
				DryRun: dry,
				Confirm: func(l cacheLoss) (bool, error) {
					if yes {
						return true, nil
					}
					return confirmCacheReset(cmd.InOrStdin(), cmd.ErrOrStderr(), l)
				},
			})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(w, res)
			}
			switch {
			case res.DryRun:
				fmt.Fprintf(w, "would delete %s\n", res.text())
			case !res.Reset:
				fmt.Fprintln(w, "cancelled: cache.db untouched")
			default:
				fmt.Fprintf(w, "deleted %s\nrebuilt: %s, %s\nforge data returns with the next `ade sync`\n",
					res.text(), plural(res.AIResults, "cached AI answer"), plural(res.LedgerEntries, "ledger entry"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "say what would be deleted and delete nothing")
	return cmd
}

type cacheOptions struct {
	DryRun bool
	// Confirm is asked with what would be lost; false cancels.
	Confirm func(cacheLoss) (bool, error)
}

// resetCache deletes cache.db with its WAL files, recreates it and runs the
// no-network reindex. Every actual reset is audited.
func resetCache(ctx context.Context, a *app.App, o cacheOptions) (cacheReset, error) {
	st, err := a.Cache()
	if err != nil {
		return cacheReset{}, err
	}
	loss := cacheLoss{Path: st.Path}
	for q, dst := range map[string]*int{
		`SELECT count(*) FROM review_drafts WHERE published = 0`: &loss.ReviewDrafts,
		`SELECT count(*) FROM inbox WHERE read = 0`:              &loss.UnreadNotification,
	} {
		if err := st.DB.QueryRow(q).Scan(dst); err != nil {
			return cacheReset{}, fmt.Errorf("count what the reset would lose: %w", err)
		}
	}
	mgr, err := workspace.NewManager(a)
	if err != nil {
		return cacheReset{}, err
	}
	rows, err := mgr.Status(ctx)
	if err != nil {
		return cacheReset{}, err
	}
	for _, r := range rows {
		if r.Alive && r.Kind != "oneshot" {
			loss.RunningInstances++
		}
	}
	res := cacheReset{cacheLoss: loss, DryRun: o.DryRun}
	if loss.RunningInstances > 0 {
		return res, withCode(1, fmt.Errorf("%d workspace instance(s) still running: stop them with `ade ws down` first, or ADE would lose track of their processes",
			loss.RunningInstances))
	}
	if o.DryRun {
		return res, nil
	}
	if ok, err := o.Confirm(loss); err != nil || !ok {
		return res, err
	}

	if err := a.Close(); err != nil {
		return res, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(st.Path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
	}
	fresh, err := store.Open(st.Path)
	if err != nil {
		return res, fmt.Errorf("recreate cache.db: %w", err)
	}
	defer fresh.Close()
	if res.AIResults, err = ai.NewCache(fresh, records.New(a.RecordsDir)).Reindex(); err != nil {
		return res, fmt.Errorf("reindex AI results: %w", err)
	}
	if res.LedgerEntries, err = ledger.New(fresh, a.Audit).Rebuild(); err != nil {
		return res, fmt.Errorf("reindex ledger: %w", err)
	}
	res.Reset = true
	if err := a.Audit.Append(audit.Entry{Action: "cache.reset", Target: st.Path, Params: map[string]any{
		"review_drafts": loss.ReviewDrafts, "unread_notifications": loss.UnreadNotification,
		"ai_results": res.AIResults, "ledger_entries": res.LedgerEntries,
	}}); err != nil {
		return res, fmt.Errorf("cache reset done, but its audit line failed: %w", err)
	}
	return res, nil
}

// confirmCacheReset asks on prompt; with input that is not a terminal it
// refuses instead of guessing, so scripts must pass --yes.
func confirmCacheReset(in io.Reader, prompt io.Writer, l cacheLoss) (bool, error) {
	if f, ok := in.(*os.File); !ok || !isTerminal(f) {
		return false, withCode(2, errors.New("refusing to delete cache.db without a terminal to confirm on: pass --yes"))
	}
	fmt.Fprintf(prompt, "delete %s? [y/N] ", l.text())
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
