package tui

import (
	tea "charm.land/bubbletea/v2"
)

// mrEntries is every MR-page action by name, so a terminal that swallows keys
// (a browser tty) still reaches each one. An entry that belongs to a tab
// switches to it first and then presses the key, so it does what the key
// does there.
func (m model) mrEntries() []palEntry {
	// at runs keys on a tab: the tab first, then each key in turn.
	at := func(name, cli string, tab mrTab, keys ...string) palEntry {
		return palEntry{name: name, key: keys[len(keys)-1], cli: cli, run: func(m model) (model, tea.Cmd) {
			cmds := []tea.Cmd{m.setTab(tab)}
			for _, k := range keys {
				next, cmd := m.press(k)
				m, cmds = next, append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}}
	}
	// any presses a key that means the same on every tab.
	any := func(name, key, cli string) palEntry {
		return palEntry{name: name, key: key, cli: cli, run: func(m model) (model, tea.Cmd) { return m.press(key) }}
	}
	ai := m.aiOn()
	es := []palEntry{
		at("Tab: Overview", "", tabOverview, "1"),
		at("Tab: Threads", "", tabThreads, "2"),
		at("Tab: Diff", "", tabDiff, "3"),
		at("Tab: Review", "", tabReview, "4"),
		at("Tab: CI", "", tabCI, "5"),
		at("Tab: History", "", tabHistory, "6"),
		any("Next tab", "tab", ""),
		any("Previous tab", "shift+tab", ""),
		any("Next MR of the item", "]", ""),
		any("Previous MR of the item", "[", ""),
		any("Back one step, then Home", "esc", ""),
		any("Move down", "j", ""),
		any("Move up", "k", ""),
		any("Jump to the top", "g", ""),
		any("Jump to the bottom", "G", ""),
		at("Copy the MR link", "", tabOverview, "y"),
		any("Reload from the forge", "U", "ade sync"),
		any("Approve", "a", "ade mr approve"),
		any("Mark ready or back to draft", "R", "ade mr ready | ade mr draft"),
		any("Merge", "m", "ade mr merge"),
		any("Submit the review as one batch", "S", "ade review submit"),
		any("Agents for this item", "P", "ade agent"),
		at("Threads: open the thread", "", tabThreads, "enter"),
		at("Threads: reply", "ade mr reply", tabThreads, "r"),
		at("Threads: resolve or reopen", "ade mr resolve", tabThreads, "x"),
		at("Diff: open delta (or your pager)", "", tabDiff, "D"),
		at("Diff: comment on the line", "", tabDiff, "enter", "c"),
		at("Diff: accept the agent draft at the line", "", tabDiff, "enter", "y"),
		at("Diff: deny the draft at the line", "", tabDiff, "enter", "n"),
		at("Diff: edit the draft at the line", "", tabDiff, "enter", "e"),
		at("Review: accept the draft", "", tabReview, "y"),
		at("Review: accept every draft", "", tabReview, "Y"),
		at("Review: deny the draft", "", tabReview, "n"),
		at("Review: edit the draft", "", tabReview, "e"),
		at("Review: cycle the severity", "", tabReview, "s"),
		at("Review: delete the draft", "", tabReview, "d"),
		at("Review: show the draft on its line", "", tabReview, "enter"),
		at("CI: job log (full screen, / searches)", "", tabCI, "l"),
		at("CI: retry the pipeline", "ade mr retry", tabCI, "r"),
	}
	if ai {
		es = append(es,
			any("Agent review pass: drafts in Review", "A", "ade ai review"),
			at("Overview: raw description or summary", "", tabOverview, "d"),
			at("Overview: new summary", "ade ai summarize", tabOverview, "s"),
			at("Threads: AI draft reply", "ade ai draft-reply", tabThreads, "d"),
			at("Threads: AI thread summary", "ade ai summarize", tabThreads, "s"),
		)
	}
	return es
}
