package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/ledger"
)

func init() { register(newBudgetCmd) }

func newBudgetCmd() *cobra.Command {
	var (
		refresh, offline bool
		days             int
	)
	cmd := &cobra.Command{
		Use:   "budget",
		Short: "AI spend: caps, profiles with provider budgets, spend by day/feature/model, stops",
		Long: "The ledger is a query over the ai.* entries of the audit log. Provider figures (LiteLLM\n" +
			"/key/info, vendor billing endpoints) are read at each source's own cadence; --refresh\n" +
			"reads them now, --offline never calls a provider. Displayed provider spend is the last\n" +
			"snapshot plus the ledger since its timestamp.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := budget.ForApp(appFrom(cmd))
			if err != nil {
				return err
			}
			rep, err := budget.Build(cmd.Context(), deps, budget.Options{Refresh: refresh, Offline: offline, Days: days})
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), rep)
			}
			printBudget(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "read provider billing endpoints now, whatever the snapshot's age")
	cmd.Flags().BoolVar(&offline, "offline", false, "no provider calls: cached snapshots and the ledger only")
	cmd.Flags().IntVar(&days, "days", 7, "days in the by-day series")
	return cmd
}

func usd(v float64) string {
	if v != 0 && v < 0.01 && v > -0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func printBudget(w io.Writer, r budget.Report) {
	fmt.Fprintf(w, "AI budget · mode %s · profile %s · %s, %d from cache (last 30 days)\n\n", r.Mode, nonEmpty(r.Profile, "none"), plural(r.Calls, "call"), r.CacheHits)

	fmt.Fprintln(w, "Caps")
	for _, c := range r.Caps {
		switch {
		case !c.Known:
			fmt.Fprintf(w, "  %-7s no sprint known\n", c.Period)
		case c.LimitUSD == 0:
			fmt.Fprintf(w, "  %-7s %s spent · no cap\n", c.Period, usd(c.SpentUSD))
		default:
			state := "ok"
			switch {
			case c.Remaining <= 0:
				state = "CAP REACHED"
			case c.SpentUSD >= 0.8*c.LimitUSD:
				state = "near cap"
			}
			fmt.Fprintf(w, "  %-7s %s of %s · %s left · %s\n", c.Period, usd(c.SpentUSD), usd(c.LimitUSD), usd(c.Remaining), state)
		}
	}

	fmt.Fprintln(w, "\nProfiles")
	for _, p := range r.Profiles {
		mark := " "
		if p.Active {
			mark = "*"
		}
		fmt.Fprintf(w, " %s %s · %s · week %s\n", mark, p.Name, p.Driver, usd(p.WeekUSD))
		var roles []string
		for _, role := range []string{"explain", "draft", "plan", "implement", "review"} {
			if m, ok := p.Models[role]; ok {
				roles = append(roles, role+"="+m)
			}
		}
		if len(roles) > 0 {
			fmt.Fprintf(w, "     models: %s\n", strings.Join(roles, " "))
		}
		if p.Provider != nil {
			fmt.Fprintf(w, "     %s\n", providerLine(*p.Provider, p.Stale))
		}
		if p.ProviderErr != "" {
			fmt.Fprintf(w, "     provider: %s\n", p.ProviderErr)
		}
	}

	section := func(title string, bs []ledger.Bucket) {
		fmt.Fprintf(w, "\n%s\n", title)
		if len(bs) == 0 {
			fmt.Fprintln(w, "  none")
			return
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, b := range bs {
			fmt.Fprintf(tw, "  %s\t%s\t%d calls\t%d cached\t%d in / %d out tokens\n", b.Key, usd(b.CostUSD), b.Calls, b.CacheHits, b.TokensIn, b.TokensOut)
		}
		tw.Flush()
	}
	section("By day", r.ByDay)
	section("By feature (30 days)", r.ByFeature)
	section("By model (30 days)", r.ByModel)
	section("Top items (30 days)", r.TopItems)

	fmt.Fprintln(w, "\nBudget stops")
	if len(r.Stops) == 0 {
		fmt.Fprintln(w, "  none")
	}
	for _, s := range r.Stops {
		fmt.Fprintf(w, "  %s  %s  %s\n", s.TS.Local().Format("2006-01-02 15:04"), nonEmpty(s.Item, "-"), s.Error)
	}

	fmt.Fprintln(w, "\nReview triage")
	if len(r.Rates) == 0 {
		fmt.Fprintln(w, "  no agent review drafts yet")
	}
	for _, x := range r.Rates {
		fmt.Fprintf(w, "  %s  %s · %d accepted · %d edited · %d denied · %d pending\n", x.Profile, plural(x.Drafts, "draft"), x.Accepted, x.Edited, x.Denied, x.Pending)
	}
}

// providerLine words one merged provider figure.
func providerLine(m billing.Merged, stale bool) string {
	s := m.Snapshot
	var parts []string
	switch {
	case m.Spend != nil && s.Limit != nil:
		parts = append(parts, fmt.Sprintf("%s spend %s of %s (%s scope)", s.Source, usd(*m.Spend), usd(*s.Limit), s.Scope))
	case m.Spend != nil:
		parts = append(parts, fmt.Sprintf("%s spend %s (%s scope)", s.Source, usd(*m.Spend), s.Scope))
	case s.Balance != nil:
		parts = append(parts, fmt.Sprintf("%s balance %s", s.Source, usd(*s.Balance)))
	default:
		parts = append(parts, s.Source+" reported no spend")
	}
	if m.Remaining != nil && s.Limit != nil {
		parts = append(parts, usd(*m.Remaining)+" left")
	}
	if s.SoftLimit != nil {
		parts = append(parts, "soft limit "+usd(*s.SoftLimit))
	}
	if s.Window != "" {
		parts = append(parts, "window "+s.Window)
	}
	if s.ResetAt != nil {
		parts = append(parts, "resets "+s.ResetAt.Local().Format("2 Jan"))
	}
	parts = append(parts, "as of "+s.AsOf.Local().Format(time.TimeOnly))
	if m.LocalDelta != 0 {
		parts = append(parts, fmt.Sprintf("incl. %s local since", usd(m.LocalDelta)))
	}
	if stale {
		parts = append(parts, "STALE")
	}
	return strings.Join(parts, " · ")
}
