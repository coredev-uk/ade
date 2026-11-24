package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/host/zellij"
)

// tabRecord is a tab Open opened or focused for a worktree.
type tabRecord struct {
	Session string `json:"session"`
	Tab     string `json:"tab"`
}

// tabRecordFile is where the tabs of tree are recorded.
func tabRecordFile(cacheDir, tree string) string {
	return filepath.Join(cacheDir, "ws", "tabs", slugOf(tree)+".json")
}

// readTabs returns the tabs recorded for tree; none when the file is missing or unreadable.
func readTabs(cacheDir, tree string) []tabRecord {
	data, err := os.ReadFile(tabRecordFile(cacheDir, tree))
	if err != nil {
		return nil
	}
	var recs []tabRecord
	if json.Unmarshal(data, &recs) != nil {
		return nil
	}
	return recs
}

// recordTab adds session/tab to the tabs recorded for tree. A worktree can
// have more than one tab: the dashboard names it after the branch, `ade ws up`
// after the directory.
func recordTab(cacheDir, tree, session, tab string) error {
	recs := readTabs(cacheDir, tree)
	rec := tabRecord{Session: session, Tab: tab}
	if slices.Contains(recs, rec) {
		return nil
	}
	file := tabRecordFile(cacheDir, tree)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(append(recs, rec))
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600)
}

// CloseResult says what Close did.
type CloseResult struct {
	Tree    string    `json:"tree"`
	Stopped []Stopped `json:"stopped"`
	// Tabs are the zellij tabs closed: the workspace's tabs and their services tabs.
	Tabs     []string `json:"tabs"`
	Warnings []string `json:"warnings,omitempty"`
}

// sessionTabs are the tabs to close in one zellij session.
type sessionTabs struct {
	session string
	ids     []int    // the caller's own tab, when among them, last
	names   []string // parallel to ids
}

// Close stops the workspace of worktree and closes its zellij tabs. It
// releases the worktree's holds as `ade ws down` does (an instance another
// workspace holds keeps running), then, in each session that still runs,
// focuses the dashboard's home tab (created when missing, so the user lands
// there and the session keeps a tab) and closes every tab Open recorded for
// the worktree, with its services tab. ready runs with the outcome right
// before the first tab closes: the caller may run inside one of those tabs,
// and closing it ends the caller, so whatever it prints goes there.
func Close(ctx context.Context, a *app.App, worktree string, ready func(CloseResult)) (CloseResult, error) {
	loc, err := Locate(a.Root, worktree)
	if err != nil {
		return CloseResult{}, err
	}
	res := CloseResult{Tree: loc.Tree, Stopped: []Stopped{}, Tabs: []string{}}
	mgr, err := NewManager(a)
	if err != nil {
		return res, err
	}
	stopped, err := mgr.Release(ctx, loc.Tree)
	res.Stopped = append(res.Stopped, stopped...)
	var targets []sessionTabs
	recs := readTabs(a.Paths.CacheDir, loc.Tree)
	// Without zellij on PATH no session can be asked about its tabs: they may
	// well be open, so the record stays for a close that can reach them.
	_, lookErr := exec.LookPath("zellij")
	keep := len(recs) > 0 && lookErr != nil
	if keep {
		res.Warnings = append(res.Warnings, "zellij is not on PATH: the workspace's tabs were left open")
	}
	if err == nil && !keep {
		var warn []string
		targets, warn = findTabs(ctx, runZellij, recs, ownPane(os.Getenv))
		res.Warnings = append(res.Warnings, warn...)
		for _, t := range targets {
			res.Tabs = append(res.Tabs, t.names...)
		}
	}
	entry := audit.Entry{
		Action: "workspace.close", Target: loc.Tree, Project: loc.Project, Outcome: "ok",
		Params: map[string]any{"stopped": len(res.Stopped), "tabs": res.Tabs},
	}
	if err != nil {
		entry.Outcome, entry.Error = "error", err.Error()
	}
	if aerr := a.Audit.Append(entry); aerr != nil && err == nil {
		err = aerr
	}
	if err != nil {
		return res, err
	}
	// Forgotten before the tabs close: closing the caller's own tab ends it.
	if !keep {
		if rerr := os.Remove(tabRecordFile(a.Paths.CacheDir, loc.Tree)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			res.Warnings = append(res.Warnings, "forget the tabs: "+rerr.Error())
		}
	}
	var homeFile string
	if len(targets) > 0 {
		ade, err := os.Executable()
		if err != nil {
			return res, fmt.Errorf("locate ade executable: %w", err)
		}
		if homeFile, err = WriteHomeLayout(a.Paths.CacheDir, ade); err != nil {
			return res, err
		}
	}
	if ready != nil {
		ready(res)
	}
	return res, closeTabs(ctx, runZellij, targets, homeFile)
}

// pane is the zellij pane the caller runs in: its session and pane id, both
// empty outside zellij.
type pane struct{ session, id string }

func ownPane(getenv func(string) string) pane {
	if getenv("ZELLIJ") == "" {
		return pane{}
	}
	return pane{session: getenv("ZELLIJ_SESSION_NAME"), id: getenv("ZELLIJ_PANE_ID")}
}

// findTabs looks up the ids of the recorded tabs, and of their services tabs,
// in every session that is still running. The session the caller runs in
// comes last, and in it the caller's own tab. Tabs closed by hand since are
// simply not found; a session whose tabs cannot be listed is a warning.
func findTabs(ctx context.Context, run zellijRunner, recs []tabRecord, own pane) ([]sessionTabs, []string) {
	want := map[string]map[string]bool{}
	var sessions []string
	for _, r := range recs {
		if want[r.Session] == nil {
			want[r.Session] = map[string]bool{}
			sessions = append(sessions, r.Session)
		}
		want[r.Session][r.Tab] = true
		want[r.Session][r.Tab+ServicesTabSuffix] = true
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	// The caller's session goes last: closing its own tab ends the caller.
	slices.SortStableFunc(sessions, func(x, y string) int {
		switch {
		case x == own.session && y != own.session:
			return 1
		case y == own.session && x != own.session:
			return -1
		}
		return 0
	})
	listing, _ := run(ctx, "list-sessions", "--no-formatting")
	var (
		out  []sessionTabs
		warn []string
	)
	for _, session := range sessions {
		if zellij.SessionState(listing, session) != zellij.Live {
			continue
		}
		data, err := run(ctx, "--session", session, "action", "list-tabs", "--json")
		if err != nil {
			warn = append(warn, fmt.Sprintf("list the tabs of session %s: %v", session, err))
			continue
		}
		var tabs []struct {
			Name  string `json:"name"`
			TabID int    `json:"tab_id"`
		}
		if err := json.Unmarshal([]byte(data), &tabs); err != nil {
			warn = append(warn, fmt.Sprintf("list the tabs of session %s: %v", session, err))
			continue
		}
		mine := -1
		if session == own.session && own.id != "" {
			mine = paneTab(ctx, run, session, own.id)
		}
		st := sessionTabs{session: session}
		lastName := ""
		for _, t := range tabs {
			switch {
			case !want[session][t.Name]:
			case t.TabID == mine:
				lastName = t.Name
			default:
				st.ids = append(st.ids, t.TabID)
				st.names = append(st.names, t.Name)
			}
		}
		if lastName != "" {
			st.ids = append(st.ids, mine)
			st.names = append(st.names, lastName)
		}
		if len(st.ids) > 0 {
			out = append(out, st)
		}
	}
	return out, warn
}

// paneTab is the id of the tab that holds terminal pane id; -1 when unknown.
func paneTab(ctx context.Context, run zellijRunner, session, id string) int {
	data, err := run(ctx, "--session", session, "action", "list-panes", "--json", "--tab")
	if err != nil {
		return -1
	}
	var panes []struct {
		ID       int  `json:"id"`
		IsPlugin bool `json:"is_plugin"`
		TabID    int  `json:"tab_id"`
	}
	if json.Unmarshal([]byte(data), &panes) != nil {
		return -1
	}
	for _, p := range panes {
		if !p.IsPlugin && strconv.Itoa(p.ID) == id {
			return p.TabID
		}
	}
	return -1
}

// closeTabs focuses the home tab of each session (creating it from homeFile
// when it is missing or its dashboard has exited) and closes the tabs by id.
func closeTabs(ctx context.Context, run zellijRunner, targets []sessionTabs, homeFile string) error {
	var errs []error
	for _, t := range targets {
		if _, err := focusTab(ctx, run, t.session, zellij.HomeTab, homeFile); err != nil {
			errs = append(errs, fmt.Errorf("session %s: show the home tab: %w", t.session, err))
			continue // closing the last tab would end the session
		}
		for _, id := range t.ids {
			if _, err := run(ctx, "--session", t.session, "action", "close-tab-by-id", strconv.Itoa(id)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
