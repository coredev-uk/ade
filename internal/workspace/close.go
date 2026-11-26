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
	"strings"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/host/zellij"
)

// tabRecord is a tab Open opened or focused for a worktree.
type tabRecord struct {
	Session string `json:"session"`
	Tab     string `json:"tab"`
	// Review marks a review tab (the diff): opening the worktree's workspace
	// does not reopen it in place of a dev tab, nor the other way round.
	Review bool `json:"review,omitempty"`
}

// tabRecords is the record file of one worktree: the tree, so the record of a
// worktree that is gone can be pruned, and its tabs.
type tabRecords struct {
	Tree string      `json:"tree"`
	Tabs []tabRecord `json:"tabs"`
}

// tabRecordDir holds one record file per worktree.
func tabRecordDir(cacheDir string) string { return filepath.Join(cacheDir, "ws", "tabs") }

// tabRecordFile is where the tabs of tree are recorded.
func tabRecordFile(cacheDir, tree string) string {
	return filepath.Join(tabRecordDir(cacheDir), slugOf(tree)+".json")
}

// readTabs returns the tabs recorded for tree; none when the file is missing or unreadable.
func readTabs(cacheDir, tree string) []tabRecord {
	data, err := os.ReadFile(tabRecordFile(cacheDir, tree))
	if err != nil {
		return nil
	}
	var r tabRecords
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return r.Tabs
}

// writeTabs replaces the tabs recorded for tree with recs; none removes the record.
func writeTabs(cacheDir, tree string, recs []tabRecord) error {
	file := tabRecordFile(cacheDir, tree)
	if len(recs) == 0 {
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("forget the tabs: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(tabRecords{Tree: tree, Tabs: recs})
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600)
}

// recordTab adds session/tab to the tabs recorded for tree. A worktree can
// have more than one tab: the dashboard names it after the branch, `ade ws up`
// after the directory. The records of worktrees that are gone are pruned first.
func recordTab(cacheDir, tree, session, tab string) error {
	return recordTabOf(cacheDir, tree, tabRecord{Session: session, Tab: tab})
}

// recordTabOf is recordTab for a full record.
func recordTabOf(cacheDir, tree string, rec tabRecord) error {
	pruneErr := pruneTabRecords(cacheDir)
	recs := readTabs(cacheDir, tree)
	if slices.Contains(recs, rec) {
		return pruneErr
	}
	return errors.Join(pruneErr, writeTabs(cacheDir, tree, append(recs, rec)))
}

// pruneTabRecords removes the records of worktrees that no longer exist
// (`ade gc`, `ade cleanup`, `ade project remove` or git removed them), and
// records that cannot be read, which no close would use.
func pruneTabRecords(cacheDir string) error {
	dir := tabRecordDir(cacheDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		file := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var r tabRecords
		if json.Unmarshal(data, &r) == nil && r.Tree != "" {
			if _, err := os.Stat(r.Tree); !errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// tabsIn are the records of recs in one of sessions.
func tabsIn(recs []tabRecord, sessions []string) []tabRecord {
	var out []tabRecord
	for _, r := range recs {
		if slices.Contains(sessions, r.Session) {
			out = append(out, r)
		}
	}
	return out
}

// CloseResult says what Close did.
type CloseResult struct {
	Tree    string    `json:"tree"`
	Stopped []Stopped `json:"stopped"`
	// Tabs are the zellij tabs closed: the workspace's tabs and their services tabs.
	Tabs []string `json:"tabs"`
	// KeptTab is the caller's own tab, which Retire leaves open.
	KeptTab  string   `json:"kept_tab,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// sessionTabs are the tabs to close in one zellij session.
type sessionTabs struct {
	session string
	ids     []int    // the caller's own tab, when among them, last
	names   []string // parallel to ids
	own     bool     // the last of ids is the caller's own tab
}

// Close stops the workspace of worktree and closes its zellij tabs. It
// releases the worktree's holds as `ade ws down` does (an instance another
// workspace holds keeps running), then, in each session that still runs,
// focuses the dashboard's home tab (created when missing, so the user lands
// there and the session keeps a tab) and closes every tab Open recorded for
// the worktree, with its services tab. ready runs with the outcome right
// before the first tab closes: the caller may run inside one of those tabs,
// and closing it ends the caller, so whatever it prints goes there. The record
// keeps the tabs of every session where they could not be listed or closed,
// for a later close.
func Close(ctx context.Context, a *app.App, worktree string, ready func(CloseResult)) (CloseResult, error) {
	loc, err := Locate(a.Root, worktree)
	if err != nil {
		return CloseResult{}, err
	}
	return closeTree(ctx, a, loc, false, ready)
}

// Retire is Close for a worktree about to be removed (`ade gc`, `ade
// cleanup`): its instances stop and its tabs close before the directory goes.
// The tab the caller runs in, when it is one of them, stays open, since closing
// it would end the removal half way; KeptTab names it. An error means the
// instances could not be stopped, so the worktree must stay; a tab that does
// not close is a warning. A path Locate rejects never had a workspace (Open
// locates it too), so there is nothing to stop.
func Retire(ctx context.Context, a *app.App, worktree string) (CloseResult, error) {
	loc, err := Locate(a.Root, worktree)
	if err != nil {
		return CloseResult{Tree: worktree, Stopped: []Stopped{}, Tabs: []string{}}, nil
	}
	return closeTree(ctx, a, loc, true, nil)
}

// RetireNotes are the lines that say what Retire did, for the removal's report.
func (r CloseResult) RetireNotes() []string {
	var out []string
	for _, s := range r.Stopped {
		out = append(out, "stopped "+s.Node)
	}
	if len(r.Tabs) > 0 {
		out = append(out, "closed tabs "+strings.Join(r.Tabs, ", "))
	}
	if r.KeptTab != "" {
		out = append(out, "tab "+r.KeptTab+" left open: this command runs in it")
	}
	for _, w := range r.Warnings {
		out = append(out, "warning: "+w)
	}
	return out
}

// closeTree is Close and, with retire set, Retire.
func closeTree(ctx context.Context, a *app.App, loc Location, retire bool, ready func(CloseResult)) (CloseResult, error) {
	res := CloseResult{Tree: loc.Tree, Stopped: []Stopped{}, Tabs: []string{}}
	mgr, err := NewManager(a)
	if err != nil {
		return res, err
	}
	stopped, err := mgr.Release(ctx, loc.Tree)
	res.Stopped = append(res.Stopped, stopped...)
	recs := readTabs(a.Paths.CacheDir, loc.Tree)
	var found recorded
	if err == nil {
		found = findRecorded(ctx, recs, retire, &res)
	}
	reason := ""
	if retire {
		reason = "worktree removal"
	}
	if err = auditClose(a, loc.Tree, loc.Project, reason, res, err); err != nil {
		return res, err
	}
	if found.keep {
		if ready != nil {
			ready(res)
		}
		return res, nil
	}
	settle := func(failed []string) error {
		return writeTabs(a.Paths.CacheDir, loc.Tree, tabsIn(recs, append(slices.Clone(found.unreached), failed...)))
	}
	err = shutTabs(ctx, a, found.targets, func() {
		if ready != nil {
			ready(res)
		}
	}, settle)
	// The instances are stopped: tabs left open do not keep a removal from going ahead.
	if retire && err != nil {
		res.Warnings = append(res.Warnings, "close the tabs: "+err.Error())
		err = nil
	}
	return res, err
}

// RetireProject is Retire for a whole project about to be removed (`ade
// project remove`): every instance of the project stops, `keep` ones and
// those a workspace holds included, since their trees go, and the tabs ADE
// recorded for its worktrees and for its services tab close, except the
// caller's own. An error means the instances could not be stopped, so the
// project must stay; a tab that does not close is a warning. CloseResult.Tree
// is the project directory.
func RetireProject(ctx context.Context, a *app.App, project string) (CloseResult, error) {
	root := a.Root
	if resolved, err := resolve(root); err == nil {
		root = resolved // as Locate and LocateProject record trees
	}
	dir := filepath.Join(root, project)
	res := CloseResult{Tree: dir, Stopped: []Stopped{}, Tabs: []string{}}
	mgr, err := NewManager(a)
	if err != nil {
		return res, err
	}
	down, err := mgr.Down(ctx, project, DownOptions{Force: true})
	res.Stopped = append(res.Stopped, down.Stopped...)
	files, recs := projectTabRecords(a.Paths.CacheDir, dir)
	var found recorded
	if err == nil {
		found = findRecorded(ctx, recs, true, &res)
	}
	if err = auditClose(a, dir, project, "project removal", res, err); err != nil {
		return res, err
	}
	if found.keep {
		return res, nil
	}
	// The trees go with the project: no record is kept for a later close.
	settle := func([]string) error {
		var errs []error
		for _, f := range files {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	if err := shutTabs(ctx, a, found.targets, nil, settle); err != nil {
		res.Warnings = append(res.Warnings, "close the tabs: "+err.Error())
	}
	return res, nil
}

// projectTabRecords returns the record files of the trees under dir (the
// project's worktrees, and dir itself for its services tab) and their tabs.
func projectTabRecords(cacheDir, dir string) (files []string, recs []tabRecord) {
	entries, _ := os.ReadDir(tabRecordDir(cacheDir)) // none recorded yet: nothing to close
	for _, e := range entries {
		file := filepath.Join(tabRecordDir(cacheDir), e.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var r tabRecords
		if json.Unmarshal(data, &r) != nil || r.Tree != dir && !strings.HasPrefix(r.Tree, dir+string(filepath.Separator)) {
			continue
		}
		files = append(files, file)
		for _, t := range r.Tabs {
			if !slices.Contains(recs, t) {
				recs = append(recs, t)
			}
		}
	}
	return files, recs
}

// recorded is what findRecorded learned about the recorded tabs.
type recorded struct {
	targets   []sessionTabs
	unreached []string // live sessions whose tabs could not be listed
	keep      bool     // zellij is not on PATH: the tabs may be open, the record stays
}

// findRecorded looks up the tabs of recs (findTabs) and adds what it found to
// res; with keepOwn the caller's own tab is left out and named in KeptTab.
func findRecorded(ctx context.Context, recs []tabRecord, keepOwn bool, res *CloseResult) recorded {
	var f recorded
	// Without zellij on PATH no session can be asked about its tabs: they may
	// well be open, so the record stays for a close that can reach them.
	if _, err := exec.LookPath("zellij"); err != nil && len(recs) > 0 {
		f.keep = true
		res.Warnings = append(res.Warnings, "zellij is not on PATH: the workspace's tabs were left open")
		return f
	}
	var warn []string
	f.targets, f.unreached, warn = findTabs(ctx, runZellij, recs, ownPane(os.Getenv))
	res.Warnings = append(res.Warnings, warn...)
	if keepOwn {
		f.targets, res.KeptTab = keepOwnTab(f.targets)
	}
	for _, t := range f.targets {
		res.Tabs = append(res.Tabs, t.names...)
	}
	return f
}

// auditClose records a close as workspace.close; err is the close's own
// error, returned unless the audit fails where the close did not.
func auditClose(a *app.App, target, project, reason string, res CloseResult, err error) error {
	entry := audit.Entry{
		Action: "workspace.close", Target: target, Project: project, Outcome: "ok",
		Params: map[string]any{"stopped": len(res.Stopped), "tabs": res.Tabs},
	}
	if reason != "" {
		entry.Params["reason"] = reason
	}
	if err != nil {
		entry.Outcome, entry.Error = "error", err.Error()
	}
	if aerr := a.Audit.Append(entry); aerr != nil && err == nil {
		return aerr
	}
	return err
}

// shutTabs focuses each session's home tab and closes targets (closeTabs);
// ready, when set, runs right before the first tab closes.
func shutTabs(ctx context.Context, a *app.App, targets []sessionTabs, ready func(), settle func([]string) error) error {
	var homeFile string
	if len(targets) > 0 {
		ade, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate ade executable: %w", err)
		}
		if homeFile, err = WriteHomeLayout(a.Paths.CacheDir, ade); err != nil {
			return err
		}
	}
	if ready != nil {
		ready()
	}
	return closeTabs(ctx, runZellij, targets, homeFile, settle)
}

// keepOwnTab takes the caller's own tab out of targets and returns its name.
func keepOwnTab(targets []sessionTabs) ([]sessionTabs, string) {
	var out []sessionTabs
	kept := ""
	for _, t := range targets {
		if t.own {
			last := len(t.ids) - 1
			kept = t.names[last]
			t.ids, t.names, t.own = t.ids[:last], t.names[:last], false
		}
		if len(t.ids) > 0 {
			out = append(out, t)
		}
	}
	return out, kept
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
// simply not found. A live session whose tabs cannot be listed is a warning
// and is returned in unreached: its tabs may still be open.
func findTabs(ctx context.Context, run zellijRunner, recs []tabRecord, own pane) (out []sessionTabs, unreached, warn []string) {
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
		return nil, nil, nil
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
	for _, session := range sessions {
		if zellij.SessionState(listing, session) != zellij.Live {
			continue
		}
		data, err := run(ctx, "--session", session, "action", "list-tabs", "--json")
		var tabs []struct {
			Name  string `json:"name"`
			TabID int    `json:"tab_id"`
		}
		if err == nil {
			err = json.Unmarshal([]byte(data), &tabs)
		}
		if err != nil {
			warn = append(warn, fmt.Sprintf("list the tabs of session %s: %v", session, err))
			unreached = append(unreached, session)
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
			st.own = true
		}
		if len(st.ids) > 0 {
			out = append(out, st)
		}
	}
	return out, unreached, warn
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
// settle runs with the sessions where a tab may still be open, before the
// caller's own tab closes (that ends the caller), and again with its session
// added when that close fails.
func closeTabs(ctx context.Context, run zellijRunner, targets []sessionTabs, homeFile string, settle func(failed []string) error) error {
	var (
		errs   []error
		failed []string
		own    *sessionTabs
	)
	for i := range targets {
		t := &targets[i]
		if _, err := focusTab(ctx, run, t.session, zellij.HomeTab, homeFile); err != nil {
			errs = append(errs, fmt.Errorf("session %s: show the home tab: %w", t.session, err))
			failed = append(failed, t.session)
			continue // closing the last tab would end the session
		}
		ids := t.ids
		if t.own {
			ids, own = ids[:len(ids)-1], t
		}
		for _, id := range ids {
			if _, err := run(ctx, "--session", t.session, "action", "close-tab-by-id", strconv.Itoa(id)); err != nil {
				errs = append(errs, err)
				if !slices.Contains(failed, t.session) {
					failed = append(failed, t.session)
				}
			}
		}
	}
	if err := settle(failed); err != nil {
		errs = append(errs, err)
	}
	if own != nil {
		if _, err := run(ctx, "--session", own.session, "action", "close-tab-by-id", strconv.Itoa(own.ids[len(own.ids)-1])); err != nil {
			errs = append(errs, err)
			if !slices.Contains(failed, own.session) {
				if err := settle(append(failed, own.session)); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}
