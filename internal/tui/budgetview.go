package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/billing"
	"github.com/coredev-uk/ade/internal/budget"
	"github.com/coredev-uk/ade/internal/ledger"
)

// budKind says what a selectable Budget row stands for.
type budKind int

const (
	bkCap budKind = iota
	bkProfile
	bkDay
	bkFeature
	bkModel
	bkItem
	bkStop
	bkReview
)

// budRow is one selectable line; idx indexes the report slice its kind names.
type budRow struct {
	kind budKind
	idx  int
}

// budRows lists the selectable rows in display order. budgetList draws the
// same rows in the same order; with no AI calls at all the spend sections
// collapse into one empty line and contribute none.
func budRows(r *budget.Report) []budRow {
	var rows []budRow
	add := func(k budKind, n int) {
		for i := range n {
			rows = append(rows, budRow{k, i})
		}
	}
	add(bkCap, len(r.Caps))
	add(bkProfile, len(r.Profiles))
	if r.Calls > 0 {
		add(bkDay, len(r.ByDay))
		add(bkFeature, len(r.ByFeature))
		add(bkModel, len(r.ByModel))
		add(bkItem, len(r.TopItems))
	}
	add(bkStop, len(r.Stops))
	add(bkReview, len(r.Rates))
	return rows
}

// usd formats money: two decimals, four for amounts under a cent so a
// single cheap call is not shown as $0.00.
func usd(v float64) string {
	if v != 0 && v > -0.01 && v < 0.01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func (g glyphs) sep() string { return " " + g.Sep + " " }

// guideRule draws a section rule whose guide words are dropped from the
// right, whole groups at a time, until the line fits.
func guideRule(g glyphs, title string, groups []string, w int) string {
	t := " " + stBold.Render(title) + " "
	for n := len(groups); n >= 0; n-- {
		gd := ""
		if n > 0 {
			gd = " " + stFaint.Render(strings.Join(groups[:n], g.sep()))
		}
		fill := w - lipgloss.Width(t) - lipgloss.Width(gd) - 1
		if fill >= 3 || n == 0 {
			return t + stRule.Render(strings.Repeat(g.Rule, max(fill, 1))) + gd
		}
	}
	return t
}

// stationBar draws spend against a cap as a station line: the baseline, the
// ground travelled, the train where the spend stands, the cap at the end.
func stationBar(g glyphs, frac float64, n int) string {
	if n < 3 {
		return ""
	}
	pos := int(max(0, min(frac, 1))*float64(n-1) + 0.5)
	var sb strings.Builder
	for i := range n {
		switch {
		case i == pos:
			sb.WriteString(g.Train)
		case i == 0:
			sb.WriteString(g.Done)
		case i == n-1:
			sb.WriteString(g.Ahead)
		case i < pos:
			sb.WriteString(g.Travelled)
		default:
			sb.WriteString(g.ToCome)
		}
	}
	return sb.String()
}

// spendBar is a spark bar: ground travelled in proportion to v/maxV, at
// least one cell for any spend, padded to n cells.
func spendBar(g glyphs, v, maxV float64, n int) string {
	k := 0
	if v > 0 && maxV > 0 {
		k = max(1, min(n, int(v/maxV*float64(n)+0.5)))
	}
	return strings.Repeat(g.Travelled, k) + strings.Repeat(" ", n-k)
}

func pct(n, d int) string {
	if d == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.0f%%", float64(n)*100/float64(d))
}

// sourceLabel names a billing source the way the page talks about it.
func sourceLabel(src string) string {
	switch src {
	case billing.SourceLiteLLM:
		return "gateway"
	case billing.SourceOMP:
		return "subscription"
	}
	return src
}

// --- body ------------------------------------------------------------------------

func (m model) budgetHeaderLeft() string {
	return stBold.Render("ADE") + "  " + stFaint.Render("home "+m.g.Sep+" worktrees "+m.g.Sep+" ") + stBold.Render("Budget")
}

func (m model) budgetFooter(w int) string {
	r := "refresh providers"
	if m.bud.refreshing {
		r = "refreshing…"
	}
	if w < twoPaneMinW {
		if m.showDetail {
			return " " + keys("j/k", "move", "J/K", "scroll", "tab", "list", "r", "refresh", "b", "back", "L", "log", "?", "keys")
		}
		return " " + keys("j/k", "move", "tab", "details", "r", "refresh", "b", "back", "L", "log", "?", "keys")
	}
	return " " + keys("j/k", "move", "J/K", "detail", "enter", "open item", "r", r, "b", "back", "L", "log", "i", "inbox", "?", "keys", "q", "quit")
}

// budgetBody draws the page to exactly w×h cells: the list on the left and
// the selection's details on the right (single pane below 100 columns, tab
// swaps).
func (m model) budgetBody(w, h int) string {
	g := m.g
	if m.bud.rep == nil {
		if m.bud.err != nil {
			return toolError(g, "could not read the budget", m.bud.err, "r tries again", w, h)
		}
		return fit(" "+stFaint.Render("reading the ledger…"), w, h)
	}
	if w < twoPaneMinW {
		if m.showDetail {
			return fit(scrollDetail(g, m.budgetDetail(w-2), m.bud.dscroll, h), w, h)
		}
		return m.budgetList(w, h)
	}
	listW := w * 64 / 100
	right := fit(scrollDetail(g, m.budgetDetail(w-listW-4), m.bud.dscroll, h), w-listW-3, h) // one cell of right margin
	return columns(stRule.Render(" │ "), h, m.budgetList(listW, h), right)
}

// budgetStatus is the one line above the caps: whether the figures came from
// cached snapshots, are being refreshed, or failed to refresh.
func (m model) budgetStatus() string {
	g, b := m.g, m.bud
	var parts []string
	if b.rep.Mode == "off" {
		parts = append(parts, stWarn.Render(g.Warn+" AI is off")+stFaint.Render(" (ai.mode = off): no calls are made"))
	}
	ps := providerStateOf(b.rep)
	figures := "figures not read yet"
	if !ps.oldest.IsZero() {
		figures = "figures as of " + ago(ps.oldest)
	}
	switch {
	case b.refreshing:
		parts = append(parts, g.Sync+" refreshing providers", stFaint.Render(figures))
	case b.err != nil:
		parts = append(parts, stBad.Render(g.Bad+" refresh failed: "+shortErr(b.err)), stFaint.Render(figures))
	default:
		issue := false
		if len(ps.failed) > 0 {
			issue = true
			parts = append(parts, stBad.Render(g.Bad+" no provider figure for "+strings.Join(ps.failed, ", "))+stFaint.Render(" "+g.Sep+" r tries again"))
		}
		if len(ps.stale) > 0 || !b.live {
			issue = true
			parts = append(parts, stWarn.Render(g.Warn+" stale")+stFaint.Render(": cached provider "+figures+" "+g.Sep+" r reads providers now"))
		}
		if !issue {
			parts = append(parts, stFaint.Render("provider "+figures))
		}
	}
	return " " + strings.Join(parts, stFaint.Render(g.sep()))
}

// providerState sums up the last provider read over the profiles: the ones
// whose figure is an older snapshot, the ones with no figure at all, and the
// age of the oldest figure on the page.
type providerState struct {
	stale, failed []string
	oldest        time.Time
}

func providerStateOf(rep *budget.Report) providerState {
	var s providerState
	for _, p := range rep.Profiles {
		switch {
		case p.Provider == nil && p.ProviderErr != "" && p.ProviderErr != "not read yet":
			s.failed = append(s.failed, p.Name)
		case p.Provider != nil && p.Stale:
			s.stale = append(s.stale, p.Name)
		}
		if p.Provider != nil && !p.Provider.Snapshot.AsOf.IsZero() && (s.oldest.IsZero() || p.Provider.Snapshot.AsOf.Before(s.oldest)) {
			s.oldest = p.Provider.Snapshot.AsOf
		}
	}
	return s
}

// providerWhy shortens a provider error to the cause a person acts on; the
// detail pane has the full text.
func providerWhy(raw string) string {
	low := strings.ToLower(raw)
	for _, c := range []struct{ needle, why string }{
		{"deadline exceeded", "timed out"},
		{"timeout", "timed out"},
		{"timed out", "timed out"},
		{"connection refused", "connection refused"},
		{"no such host", "host not found"},
		{"401", "key rejected"},
		{"403", "key rejected"},
		{"unauthorized", "key rejected"},
		{"forbidden", "key rejected"},
	} {
		if strings.Contains(low, c.needle) {
			return c.why
		}
	}
	return "read failed"
}

// budgetList draws every section of the report as one scrolling list, with
// the selected row kept in view.
func (m model) budgetList(w, h int) string {
	g, rep := m.g, m.bud.rep
	rows := budRows(rep)
	pos := make(map[budRow]int, len(rows))
	for i, r := range rows {
		pos[r] = i
	}
	out := []string{m.budgetStatus(), ""}
	selLine := 0
	add := func(k budKind, i int, draw func(lead string, sel bool) string) {
		sel := pos[budRow{k, i}] == m.bud.sel
		lead := "   "
		if sel {
			selLine = len(out)
			lead = " " + stBold.Render(g.Selected) + " "
		}
		out = append(out, draw(lead, sel))
	}
	label := func(s string, sel bool) string {
		if sel {
			return stBold.Render(s)
		}
		return s
	}
	empty := func(s string) { out = append(out, "   "+stFaint.Render(s)) }

	// Caps
	var heads []string
	for _, c := range rep.Caps {
		switch {
		case c.LimitUSD > 0 && c.Known:
			heads = append(heads, fmt.Sprintf("%s %s of %s", c.Period, usd(c.SpentUSD), usd(c.LimitUSD)))
		case c.Known:
			heads = append(heads, fmt.Sprintf("%s %s", c.Period, usd(c.SpentUSD)))
		}
	}
	out = append(out, guideRule(g, "Caps", heads, w))
	for i, c := range rep.Caps {
		add(bkCap, i, func(lead string, sel bool) string { return lead + m.capRow(c, label, sel, w-3) })
	}

	// Profiles
	var pg []string
	if rep.Mode != "" {
		pg = append(pg, "ai "+rep.Mode)
	}
	if rep.Profile != "" {
		pg = append(pg, "active "+rep.Profile)
	}
	out = append(out, "", guideRule(g, "Profiles", pg, w))
	if len(rep.Profiles) == 0 {
		empty("no AI profiles configured")
	}
	nameW, driverW := 8, 6
	for _, p := range rep.Profiles {
		nameW = max(nameW, min(lipgloss.Width(p.Name), 16))
		driverW = max(driverW, min(lipgloss.Width(p.Driver), 13))
	}
	for i, p := range rep.Profiles {
		add(bkProfile, i, func(lead string, sel bool) string {
			station, active := stFaint.Render(g.Ahead), strings.Repeat(" ", 7)
			if p.Active {
				station, active = stBold.Render(g.Train), stBold.Render(pad("active", 7))
			}
			head := lead + station + " " + label(pad(p.Name, nameW), sel) + " " + active + " "
			if w >= 90 { // the driver is in the details when the list is narrow
				head += stFaint.Render(pad(p.Driver, driverW)) + " "
			}
			return head + m.providerFigure(p, w-lipgloss.Width(head))
		})
	}

	// Spend
	if rep.Calls == 0 {
		out = append(out, "", guideRule(g, "Spend", []string{"by day, feature and model", "last 30 days"}, w))
		empty("no AI calls yet")
	} else {
		var total float64
		maxDay := 0.0
		for _, d := range rep.ByDay {
			total += d.CostUSD
			maxDay = max(maxDay, d.CostUSD)
		}
		out = append(out, "", guideRule(g, "By day", []string{fmt.Sprintf("last %d days", len(rep.ByDay)), usd(total)}, w))
		today := time.Now().Format("2006-01-02")
		if !rep.Now.IsZero() {
			today = rep.Now.Format("2006-01-02")
		}
		for i, d := range rep.ByDay {
			add(bkDay, i, func(lead string, sel bool) string {
				note := ""
				switch {
				case d.Key == today:
					note = stFaint.Render("  today")
				case d.Calls == 0:
					note = stFaint.Render("  no calls")
				}
				return lead + label(pad(fmtDay(d.Key), 10), sel) + "  " + spendBar(g, d.CostUSD, maxDay, 14) + "  " + padLeft(usd(d.CostUSD), 8) + note
			})
		}
		out = append(out, "", guideRule(g, "By feature", []string{"last 30 days", countWord(rep.Calls, "call"), fmt.Sprintf("%d cache %s", rep.CacheHits, plural(rep.CacheHits, "hit"))}, w))
		m.bucketRows(add, label, bkFeature, rep.ByFeature, w, func(b ledger.Bucket) string { return b.Key })
		out = append(out, "", guideRule(g, "By model", []string{"last 30 days"}, w))
		m.bucketRows(add, label, bkModel, rep.ByModel, w, func(b ledger.Bucket) string { return b.Key })
		out = append(out, "", guideRule(g, "Top items", []string{"last 30 days", "enter opens one on Home"}, w))
		if len(rep.TopItems) == 0 {
			empty("no calls were attributed to an item")
		}
		m.bucketRows(add, label, bkItem, rep.TopItems, w, func(b ledger.Bucket) string { return refShort(b.Key) })
	}

	// Stops
	out = append(out, "", guideRule(g, "Recent stops", []string{"budget stops, newest first"}, w))
	if len(rep.Stops) == 0 {
		out = append(out, "   "+stOK.Render(g.OK)+" "+stFaint.Render("no budget stops"))
	}
	for i, e := range rep.Stops {
		add(bkStop, i, func(lead string, sel bool) string {
			head := lead + stBad.Render(g.Bad+" stopped") + "  " + stFaint.Render(e.TS.Local().Format("2 Jan 15:04")) + "  " + label(e.Feature, sel)
			if e.Profile != "" {
				head += stFaint.Render(g.sep() + e.Profile)
			}
			if e.Error != "" {
				head += stFaint.Render(g.sep() + e.Error)
			}
			return head
		})
	}

	// Review triage
	out = append(out, "", guideRule(g, "Review triage", []string{"last 30 days", "agent drafts per profile"}, w))
	if len(rep.Rates) == 0 {
		empty("no agent review drafts yet")
	}
	rw := 8
	for _, r := range rep.Rates {
		rw = max(rw, min(lipgloss.Width(r.Profile), 16))
	}
	for i, r := range rep.Rates {
		add(bkReview, i, func(lead string, sel bool) string {
			return lead + label(pad(r.Profile, rw), sel) + " " + m.reviewSummary(r)
		})
	}

	top := 0
	if selLine >= h-1 {
		top = selLine - h + 2
	}
	return fit(strings.Join(out[min(top, len(out)):], "\n"), w, h)
}

func padLeft(s string, n int) string {
	if d := n - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// bucketRows draws one row per bucket: name, a spark bar scaled to the
// section's costliest bucket, the cost and the call count.
func (m model) bucketRows(add func(budKind, int, func(string, bool) string), label func(string, bool) string,
	k budKind, bs []ledger.Bucket, w int, name func(ledger.Bucket) string,
) {
	g := m.g
	nameW, maxCost := 8, 0.0
	for _, b := range bs {
		nameW = max(nameW, lipgloss.Width(name(b)))
		maxCost = max(maxCost, b.CostUSD)
	}
	nameW = min(nameW, max(10, (w-3)/3))
	for i, b := range bs {
		add(k, i, func(lead string, sel bool) string {
			calls := countWord(b.Calls, "call")
			if b.CacheHits > 0 {
				calls += g.sep() + fmt.Sprintf("%d cached", b.CacheHits)
			}
			return lead + label(pad(name(b), nameW), sel) + "  " + spendBar(g, b.CostUSD, maxCost, 12) + "  " + padLeft(usd(b.CostUSD), 8) + "  " + stFaint.Render(calls)
		})
	}
}

// --- caps ------------------------------------------------------------------------

type capState int

const (
	capOK capState = iota
	capNear
	capOver
	capReached
	capNoCap
	capNoSprint
)

func capStateOf(c budget.Cap) capState {
	switch {
	case !c.Known:
		return capNoSprint
	case c.LimitUSD <= 0:
		return capNoCap
	case c.SpentUSD > c.LimitUSD:
		return capOver
	case c.SpentUSD >= c.LimitUSD:
		return capReached
	case c.SpentUSD >= c.LimitUSD*nearCap:
		return capNear
	}
	return capOK
}

// capWords is the state as glyph and words, styled; every state has both.
func (m model) capWords(c budget.Cap) string {
	g := m.g
	switch capStateOf(c) {
	case capOver:
		return stBad.Render(g.Bad+" over cap by "+usd(c.SpentUSD-c.LimitUSD)) + stFaint.Render(g.sep()+"calls stopped")
	case capReached:
		return stBad.Render(g.Bad+" cap reached") + stFaint.Render(g.sep()+"calls stopped")
	case capNear:
		return stWarn.Render(g.Warn+" near cap") + stFaint.Render(g.sep()+usd(c.Remaining)+" left")
	case capOK:
		return stOK.Render(g.OK+" ok") + stFaint.Render(g.sep()+usd(c.Remaining)+" left")
	case capNoCap:
		return stFaint.Render("no cap set")
	}
	return stFaint.Render("no sprint known")
}

// capRow is one cap: its period, the station-line bar, the share used, and
// the state in words.
func (m model) capRow(c budget.Cap, label func(string, bool) string, sel bool, w int) string {
	g := m.g
	head := label(pad(c.Period, 7), sel)
	switch capStateOf(c) {
	case capNoSprint:
		if c.LimitUSD > 0 {
			return head + stFaint.Render("no sprint known"+g.sep()+"cap "+usd(c.LimitUSD)+" not enforced")
		}
		return head + stFaint.Render("no sprint known"+g.sep()+"U syncs to learn it")
	case capNoCap:
		return head + usd(c.SpentUSD) + stFaint.Render(" spent"+g.sep()+"no cap set")
	}
	frac := c.SpentUSD / c.LimitUSD
	bw := 14
	if w < 70 {
		bw = 10
	}
	bar := stationBar(g, frac, bw)
	switch capStateOf(c) {
	case capOver, capReached:
		bar = stBad.Render(bar)
	case capNear:
		bar = stWarn.Render(bar)
	}
	return head + bar + " " + padLeft(fmt.Sprintf("%d%%", min(int(frac*100+0.5), 999)), 4) + "  " + m.capWords(c)
}

// --- profiles --------------------------------------------------------------------

// providerFigure is the merged provider figure for a profile's list row,
// cut to w cells: "gateway $0.0080 of $0.01 · resets 12 Nov", with stale and
// error states in words.
func (m model) providerFigure(p budget.ProfileView, w int) string {
	g := m.g
	var s string
	switch {
	case p.Provider == nil && (p.Source == "" || p.Source == "none"):
		s = stFaint.Render("no billing source")
	case p.Provider == nil && p.ProviderErr == "not read yet": // an offline build with no snapshot yet
		s = stFaint.Render(sourceLabel(p.Source) + ": not read yet")
	case p.Provider == nil && p.ProviderErr != "":
		s = stBad.Render(g.Bad+" no figure") + stFaint.Render(g.sep()+providerWhy(p.ProviderErr))
	case p.Provider == nil:
		s = stFaint.Render(sourceLabel(p.Source) + ": not read yet")
	default:
		mg := *p.Provider
		if mg.Snapshot.Source == "" {
			mg.Snapshot.Source = p.Source
		}
		s = figureText(g, &mg)
		switch {
		case p.Stale && p.ProviderErr != "":
			s += stFaint.Render(g.sep()) + stWarn.Render(g.Warn+" stale") + stFaint.Render(g.sep()+providerWhy(p.ProviderErr))
		case p.Stale:
			s += stFaint.Render(g.sep()) + stWarn.Render(g.Warn+" stale")
		}
	}
	return ansi.Truncate(s, max(w, 1), "…")
}

// figureText is "gateway $0.0080 of $0.01 · resets 12 Nov". A soft limit
// is worded as soft; a prepaid balance as balance. Nothing here carries a
// credential: snapshots hold figures only.
func figureText(g glyphs, mg *billing.Merged) string {
	snap := mg.Snapshot
	label := sourceLabel(snap.Source)
	var parts []string
	switch {
	case mg.Spend != nil && snap.Limit != nil:
		parts = append(parts, fmt.Sprintf("%s %s of %s", label, usd(*mg.Spend), usd(*snap.Limit)))
		if snap.SoftLimit != nil {
			parts = append(parts, "soft limit "+usd(*snap.SoftLimit))
		}
	case mg.Spend != nil && snap.SoftLimit != nil:
		parts = append(parts, fmt.Sprintf("%s %s of %s soft limit", label, usd(*mg.Spend), usd(*snap.SoftLimit)))
	case mg.Spend != nil:
		parts = append(parts, fmt.Sprintf("%s %s spent", label, usd(*mg.Spend)))
	case snap.Balance == nil:
		parts = append(parts, label+" no spend reported")
	}
	if snap.Balance != nil {
		b := "balance " + usd(*snap.Balance)
		if len(parts) == 0 {
			b = label + " " + b
		}
		parts = append(parts, b)
	}
	if snap.ResetAt != nil {
		parts = append(parts, "resets "+snap.ResetAt.Local().Format("2 Jan"))
	}
	return strings.Join(parts, g.sep())
}

// reviewSummary is "12 drafts · 58% accepted · 17% edited · 25% denied":
// rates are shares of the drafts someone triaged, so a noisy reviewer model
// shows.
func (m model) reviewSummary(r ledger.Rates) string {
	g := m.g
	s := countWord(r.Drafts, "draft")
	if d := r.Decided(); d == 0 {
		s += g.sep() + "none triaged yet"
	} else {
		s += g.sep() + pct(r.Accepted, d) + " accepted" + g.sep() + pct(r.Edited, d) + " edited" + g.sep() + pct(r.Denied, d) + " denied"
	}
	if r.Pending > 0 {
		s += g.sep() + fmt.Sprintf("%d pending", r.Pending)
	}
	out := stFaint.Render(s)
	if d := r.Decided(); d >= 5 && r.Denied*2 > d {
		out += stFaint.Render(g.sep()) + stWarn.Render(g.Warn+" mostly denied")
	}
	return out
}

// --- details ---------------------------------------------------------------------

// budgetDetail is the right pane (or the tab view): the selected row in full.
func (m model) budgetDetail(w int) []string {
	rep := m.bud.rep
	rows := budRows(rep)
	if len(rows) == 0 {
		return nil
	}
	r := rows[clamp(m.bud.sel, 0, len(rows)-1)]
	g := m.g
	kv := func(k, v string) string { return stFaint.Render(pad(k, 10)) + v }
	prose := func(l []string, s string, style lipgloss.Style) []string {
		for _, line := range wrap(s, w-1) {
			l = append(l, style.Render(line))
		}
		return l
	}
	var l []string
	bucketKV := func(b ledger.Bucket) {
		l = append(l, kv("cost", usd(b.CostUSD)), kv("calls", fmt.Sprintf("%d (%d from cache)", b.Calls, b.CacheHits)),
			kv("tokens", fmt.Sprintf("%d in %s %d out %s %d cached", b.TokensIn, g.Sep, b.TokensOut, g.Sep, b.TokensCache)))
	}
	switch r.kind {
	case bkCap:
		c := rep.Caps[r.idx]
		l = append(l, stBold.Render(capitalize(c.Period)+" cap"), "")
		switch capStateOf(c) {
		case capNoSprint:
			l = prose(l, "No sprint window is known yet, so the sprint cap is not enforced rather than guessed. U syncs the forge and learns it.", stFaint)
		default:
			l = append(l, kv("state", m.capWords(c)), kv("spent", usd(c.SpentUSD)))
			if c.LimitUSD > 0 {
				l = append(l, kv("cap", usd(c.LimitUSD)), kv("left", leftWords(c.Remaining)))
			}
			l = append(l, "")
			l = prose(l, "Checked locally before every AI call, against the ledger figure. The provider's own cap sits beside it under Profiles.", stFaint)
		}
	case bkProfile:
		l = m.profileDetail(rep.Profiles[r.idx], w, kv, prose)
	case bkDay:
		b := rep.ByDay[r.idx]
		l = append(l, stBold.Render(fmtDay(b.Key)), "")
		bucketKV(b)
	case bkFeature:
		b := rep.ByFeature[r.idx]
		l = append(l, stBold.Render(b.Key), stFaint.Render("feature "+g.Sep+" last 30 days"), "")
		bucketKV(b)
	case bkModel:
		b := rep.ByModel[r.idx]
		l = append(l, stBold.Render(b.Key), stFaint.Render("model "+g.Sep+" last 30 days"), "")
		bucketKV(b)
	case bkItem:
		b := rep.TopItems[r.idx]
		l = append(l, stBold.Render(b.Key), stFaint.Render("item "+g.Sep+" last 30 days"), "")
		bucketKV(b)
		l = append(l, "")
		if _, ok := m.homeTargetFor(b.Key); ok {
			l = append(l, stFaint.Render("enter opens it on Home"))
		} else {
			l = append(l, stFaint.Render("not on Home in the last sync"))
		}
	case bkStop:
		e := rep.Stops[r.idx]
		l = append(l, stBad.Render(g.Bad+" ")+stBold.Render("budget stop"), stFaint.Render(e.TS.Local().Format("Mon 2 Jan 15:04")), "",
			kv("feature", e.Feature), kv("profile", e.Profile), kv("model", e.Model))
		if e.Item != "" {
			l = append(l, kv("item", e.Item))
		}
		if e.Error != "" {
			l = append(l, "")
			l = prose(l, e.Error, lipgloss.NewStyle())
		}
	case bkReview:
		x := rep.Rates[r.idx]
		d := x.Decided()
		l = append(l, stBold.Render(x.Profile), stFaint.Render("agent review drafts "+g.Sep+" last 30 days"), "",
			kv("drafts", fmt.Sprint(x.Drafts)),
			kv("accepted", fmt.Sprintf("%d (%s)", x.Accepted, pct(x.Accepted, d))),
			kv("edited", fmt.Sprintf("%d (%s)", x.Edited, pct(x.Edited, d))),
			kv("denied", fmt.Sprintf("%d (%s)", x.Denied, pct(x.Denied, d))),
			kv("pending", fmt.Sprint(x.Pending)), "")
		l = prose(l, "Rates are shares of the drafts you triaged. A draft deleted unsent counts as denied.", stFaint)
	}
	for i, s := range l {
		l[i] = ansi.Truncate(s, w, "…")
	}
	return l
}

func (m model) profileDetail(p budget.ProfileView, w int, kv func(k, v string) string, prose func([]string, string, lipgloss.Style) []string) []string {
	g := m.g
	title := stBold.Render(p.Name)
	kind := "profile " + g.Sep + " driver " + p.Driver
	if p.Active {
		kind = "active " + g.Sep + " driver " + p.Driver
	}
	l := []string{title, stFaint.Render(kind), ""}
	roles := []string{"explain", "draft", "plan", "implement", "review"}
	for _, r := range slices.Sorted(maps.Keys(p.Models)) {
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	l = append(l, stBold.Render("Models"))
	for _, r := range roles {
		if id, ok := p.Models[r]; ok {
			l = append(l, kv(r, id))
		}
	}
	l = append(l, "", stBold.Render("Spend"), kv("this week", usd(p.WeekUSD)+stFaint.Render(" in the ledger")))
	l = append(l, "", stBold.Render("Provider"))
	switch {
	case p.Source == "":
		l = append(l, stFaint.Render("no billing source configured"))
	case p.Provider == nil:
		l = append(l, kv("source", sourceLabel(p.Source)))
	default:
		mg, snap := p.Provider, p.Provider.Snapshot
		l = append(l, kv("source", sourceLabel(snap.Source)))
		if snap.Scope != "" {
			l = append(l, kv("scope", snap.Scope))
		}
		if mg.Spend != nil {
			l = append(l, kv("spend", usd(*mg.Spend)))
			if snap.Spend != nil {
				l = append(l, kv("provider", usd(*snap.Spend)), kv("ledger", "+"+usd(mg.LocalDelta)+stFaint.Render(" since the snapshot")))
			}
		}
		if snap.Limit != nil {
			l = append(l, kv("cap", usd(*snap.Limit)))
		}
		if snap.SoftLimit != nil {
			l = append(l, kv("soft", usd(*snap.SoftLimit)+stFaint.Render(" (never blocks)")))
		}
		if mg.Remaining != nil {
			l = append(l, kv("left", leftWords(*mg.Remaining)))
		}
		if snap.Balance != nil {
			l = append(l, kv("balance", usd(*snap.Balance)))
		}
		win := snap.Window
		if snap.ResetAt != nil {
			win = strings.TrimSpace(win + " " + g.Sep + " resets " + snap.ResetAt.Local().Format("2 Jan 15:04"))
		}
		if win != "" {
			l = append(l, kv("window", win))
		}
		if !snap.AsOf.IsZero() {
			l = append(l, kv("as of", ago(snap.AsOf)))
		}
		if snap.Note != "" {
			l = prose(l, snap.Note, stFaint)
		}
	}
	if p.Stale {
		l = prose(append(l, ""), g.Warn+" stale: the snapshot above is older than asked for, so the provider may be further along.", stWarn)
	}
	if p.ProviderErr != "" && p.ProviderErr != "not read yet" {
		l = append(l, "", stBad.Render(g.Bad+" provider read failed: "+providerWhy(p.ProviderErr)))
		for _, ln := range hardWrap(audit.Redact(p.ProviderErr), max(w-1, 10)) {
			l = append(l, stFaint.Render(ln))
		}
		if p.Provider == nil {
			l = append(l, "", stFaint.Render("no figure yet · r reads the provider again"))
		} else {
			l = append(l, "", stFaint.Render("r reads the provider again"))
		}
	}
	return l
}

// leftWords words the headroom under a cap; a negative figure is "none", not "$-0.29".
func leftWords(v float64) string {
	if v < 0 {
		return "none" + stFaint.Render(" (over by "+usd(-v)+")")
	}
	return usd(v)
}
