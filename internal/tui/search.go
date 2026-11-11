package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/project"
)

// `/` filters Home as you type: every word must match, in order, somewhere in
// a row's ref, title, labels, epic, reason or (for an MR) author and branch.
// The same text also searches the project's open MRs on the forge, which Home
// does not otherwise hold; that runs off the UI loop after a pause in typing.

// searchDebounce is how long typing must pause before the forge is asked.
const searchDebounce = 350 * time.Millisecond

// searchState is the `/` input and what the forge search returned. It lives
// in model.hs.search.
type searchState struct {
	typing  bool   // the input has focus
	input   string // the query; applied as typed, also after enter
	gen     int    // bumped per edit: stale debounce ticks and answers are dropped
	pool    []remoteMR
	fetched bool // pool holds this search's answer
	loading bool
	err     error
}

type (
	// searchTickMsg fires searchDebounce after an edit.
	searchTickMsg struct{ gen int }
	// searchPoolMsg is the forge's open MRs for every project on Home.
	searchPoolMsg struct {
		gen  int
		pool []remoteMR
		err  error
	}
)

// fuzzyMatch reports whether every word of q matches s (see termMatches).
func fuzzyMatch(q, s string) bool { return fuzzyFields(q, []string{s}) }

// fuzzyFields reports whether every word of q matches within at least one of
// the fields.
func fuzzyFields(q string, fields []string) bool {
	hays := make([][]rune, len(fields))
	for i, f := range fields {
		hays[i] = []rune(strings.ToLower(f))
	}
	for _, w := range strings.Fields(strings.ToLower(q)) {
		found := false
		for _, h := range hays {
			if termMatches([]rune(w), h) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// termMatches reports whether term is a substring of hay or, for a term of
// three letters or more, a subsequence whose letters lie within twice the
// term's length plus one ("rte" finds "Rate", "blg" finds "Billing"; "rate" does not
// find "draft … ready"), so a loose word still narrows the list.
func termMatches(term, hay []rune) bool {
	for start := range hay {
		if hay[start] != term[0] {
			continue
		}
		// Greedy subsequence from this start: where the last letter lands.
		i, end := start, start
		for _, r := range term {
			for i < len(hay) && hay[i] != r {
				i++
			}
			if i == len(hay) {
				end = -1
				break
			}
			end = i
			i++
		}
		if end < 0 {
			continue
		}
		span := end - start + 1
		if span == len(term) || len(term) >= 3 && span <= 2*len(term)+1 {
			return true
		}
	}
	return false
}

// fuzzyScore ranks matches for the palette: consecutive runs and word starts
// score, gaps cost. ok is false when q does not match.
func fuzzyScore(q, s string) (score int, ok bool) {
	hay := []rune(strings.ToLower(s))
	q = strings.ToLower(strings.Join(strings.Fields(q), ""))
	i, prev := 0, -2
	for _, r := range q {
		for i < len(hay) && hay[i] != r {
			i++
		}
		if i == len(hay) {
			return 0, false
		}
		switch {
		case i == prev+1:
			score += 3
		case i == 0 || !unicode.IsLetter(hay[i-1]) && !unicode.IsDigit(hay[i-1]):
			score += 2
		default:
			score++
		}
		prev = i
		i++
	}
	return score*10 - len(hay), true
}

// rowFields are the texts a search matches a row against; a word must match
// within one of them, never across two.
func rowFields(r homeRow) []string {
	if r.mr != nil {
		mr := r.mr
		return []string{mr.Ref.String(), mr.Title, mr.Author.Username, mr.SourceBranch}
	}
	it := r.item
	return []string{
		it.Ref.String(), it.Issue.Title, it.Type, strings.Join(it.Apps, " "), it.Workflow,
		it.Issue.Epic, it.Reason, string(it.Court),
	}
}

func (m model) homeQuery() homeQuery {
	q := homeQuery{filter: m.homeFilter, facet: m.hs.facet, value: m.hs.facetVal, project: m.hs.project, text: strings.TrimSpace(m.hs.search.input)}
	if q.text != "" {
		q.remote = m.hs.search.pool
	}
	return q
}

// searching is true while a query narrows Home.
func (m model) searching() bool { return strings.TrimSpace(m.hs.search.input) != "" }

// --- keys ----------------------------------------------------------------------

// openSearch handles `/` on Home.
func (m model) openSearch() (model, tea.Cmd) {
	if m.page != pageHome || m.mr != nil || m.isAgilePage() || m.showDetail {
		return m, nil
	}
	m.hs.search.typing = true
	return m, nil
}

// searchKey edits the query while the input has focus.
func (m model) searchKey(k tea.KeyPressMsg) (model, tea.Cmd) {
	s := &m.hs.search
	ks := k.String()
	switch ks {
	case "esc":
		return m.clearSearch(), nil
	case "enter":
		s.typing = false
		return m, nil
	case "up", "ctrl+p":
		m.move(-1)
		return m, nil
	case "down", "ctrl+n":
		m.move(1)
		return m, nil
	case "ctrl+u":
		s.input = ""
	case "backspace", "ctrl+h":
		r := []rune(s.input)
		if len(r) > 0 {
			s.input = string(r[:len(r)-1])
		}
	default:
		if k.Text == "" || strings.ContainsAny(k.Text, "\r\n\t") {
			return m, nil
		}
		s.input += k.Text
	}
	return m.searchEdited()
}

// searchEdited re-filters at once and schedules the forge search.
func (m model) searchEdited() (model, tea.Cmd) {
	s := &m.hs.search
	s.gen++
	m.homeSel = 0
	m.rebuildHome("")
	if strings.TrimSpace(s.input) == "" || s.fetched {
		return m, nil
	}
	gen := s.gen
	return m, tea.Tick(searchDebounce, func(time.Time) tea.Msg { return searchTickMsg{gen} })
}

func (m model) clearSearch() model {
	m.hs.search = searchState{gen: m.hs.search.gen + 1}
	m.homeSel = 0
	m.rebuildHome("")
	return m
}

// searchUpdate handles the search's own messages.
func (m model) searchUpdate(msg tea.Msg) (model, tea.Cmd, bool) {
	s := &m.hs.search
	switch msg := msg.(type) {
	case searchTickMsg:
		if msg.gen != s.gen || s.fetched || s.loading || !m.searching() {
			return m, nil, true
		}
		s.loading = true
		return m, m.fetchOpenMRs(s.gen), true
	case searchPoolMsg:
		if msg.gen != s.gen && msg.err == nil && !s.loading {
			return m, nil, true
		}
		s.loading, s.err = false, msg.err
		if msg.err == nil {
			s.pool, s.fetched = msg.pool, true
			m.rebuildHome(m.selectedRef())
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m model) selectedRef() string {
	if m.homeSel < len(m.homeRows) {
		return m.homeRows[m.homeSel].ref()
	}
	return ""
}

// fetchOpenMRs lists every open MR of the repos of each project on Home. The
// answer is kept for the rest of the search session, so refining the query
// never asks again.
func (m model) fetchOpenMRs(gen int) tea.Cmd {
	ctx, a, mm := m.ctx, m.app, m
	if ctx == nil {
		ctx = context.Background()
	}
	type target struct{ project, forgeKey, repo string }
	forgeOf := map[string]string{} // ADE project -> forge key, for the projects searched
	for _, res := range m.results {
		if m.hs.project == "" || res.Project == m.hs.project {
			forgeOf[res.Project] = res.Forge
		}
	}
	return func() tea.Msg {
		// The repos come from the registered manifests, as the sync reads them.
		var targets []target
		var firstErr error
		for _, ref := range a.Config.Projects {
			mf, err := project.FetchManifest(ctx, a, ref)
			if err != nil {
				firstErr = firstError(firstErr, err)
				continue
			}
			key, ok := forgeOf[mf.Name]
			if !ok {
				continue
			}
			for _, name := range mf.RepoNames() {
				if p := mf.Repos[name].Path; p != "" {
					targets = append(targets, target{mf.Name, key, p})
				}
			}
		}
		if len(targets) == 0 && firstErr == nil {
			firstErr = errors.New("no repositories to search")
		}
		var pool []remoteMR
		for _, t := range targets {
			be, err := mm.backend(ctx, forge.Ref{Project: t.repo, Kind: forge.KindMR}, t.forgeKey)
			if err == nil {
				var mrs []forge.MR
				mrs, err = be.ListMRs(ctx, forge.MRQuery{Project: t.repo, State: "opened"})
				for _, mr := range mrs {
					pool = append(pool, remoteMR{project: t.project, mr: mr})
				}
			}
			firstErr = firstError(firstErr, err)
		}
		sort.SliceStable(pool, func(i, j int) bool { return pool[i].mr.UpdatedAt.After(pool[j].mr.UpdatedAt) })
		if len(pool) > 0 {
			firstErr = nil // a partial answer is still an answer; the rest is retried next search
		}
		return searchPoolMsg{gen: gen, pool: pool, err: firstErr}
	}
}

// --- drawing -------------------------------------------------------------------

// searchBar is the line above the list while a query is typed or applied.
func (m model) searchBar(w int) string {
	s := m.hs.search
	prompt := stBold.Render("/") + " " + s.input
	if s.typing {
		prompt += stBold.Render("▏")
	}
	n := len(m.homeRows)
	right := stFaint.Render(fmt.Sprintf("%d %s", n, map[bool]string{true: "match", false: "matches"}[n == 1]))
	switch {
	case s.loading:
		right = stFaint.Render(m.g.Sync+" searching open MRs "+m.g.Sep+" ") + right
	case s.err != nil:
		right = stBad.Render(m.g.Bad+" MR search failed ") + stFaint.Render(m.g.Sep+" ") + right
	}
	gap := max(1, w-3-lipgloss.Width(prompt)-lipgloss.Width(right))
	return ansi.Truncate(" "+prompt+strings.Repeat(" ", gap)+right, w, "…")
}

// firstError keeps the first failure of several tries.
func firstError(first, next error) error {
	if first != nil {
		return first
	}
	return next
}
