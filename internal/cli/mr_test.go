package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/review"
)

const (
	apiPath  = fake.GroupPath + "/api"
	webPath  = fake.GroupPath + "/web"
	renameMR = apiPath + "!2"
	rateMR   = apiPath + "!1"
)

// cliEnv is an app wired to a fake GitLab: one configured forge, so refs
// resolve without any registered project.
type cliEnv struct {
	t   *testing.T
	srv *fake.Server
	app *app.App
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	srv := fake.New(fake.Free)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	log, err := audit.Open(dir, "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Forges = map[string]config.Forge{"sandbox": {Kind: "gitlab", URL: srv.URL}}
	a := &app.App{Paths: paths.Paths{CacheDir: dir}, Config: &cfg, Audit: log, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })

	old := review.Dial
	review.Dial = func(_ context.Context, _ string, f config.Forge) (review.Backend, error) {
		return gitlab.New(f.URL, fake.Token), nil
	}
	t.Cleanup(func() { review.Dial = old })
	return &cliEnv{t: t, srv: srv, app: a}
}

// run executes `ade <args>` below root (mr or review) and returns stdout.
func (e *cliEnv) run(newRoot func() *cobra.Command, args ...string) (string, error) {
	e.t.Helper()
	cmd := newRoot()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	ctx := context.WithValue(context.Background(), ctxKey{}, e.app)
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func (e *cliEnv) mr(args ...string) (string, error)     { return e.run(newMRCmd, args...) }
func (e *cliEnv) review(args ...string) (string, error) { return e.run(newReviewCmd, args...) }

func (e *cliEnv) mustMR(args ...string) string {
	e.t.Helper()
	out, err := e.mr(args...)
	if err != nil {
		e.t.Fatalf("ade mr %v: %v\n%s", args, err, out)
	}
	return out
}

func (e *cliEnv) mustReview(args ...string) string {
	e.t.Helper()
	out, err := e.review(args...)
	if err != nil {
		e.t.Fatalf("ade review %v: %v\n%s", args, err, out)
	}
	return out
}

func asJSON(t *testing.T) {
	t.Helper()
	jsonOut = true
	t.Cleanup(func() { jsonOut = false })
}

func ref(s string) forge.Ref {
	r, err := forge.ParseRef(s)
	if err != nil {
		panic(err)
	}
	return r
}

func (e *cliEnv) audited(action string) []audit.Entry {
	e.t.Helper()
	es, err := e.app.Audit.Query(audit.Filter{Action: action})
	if err != nil {
		e.t.Fatal(err)
	}
	return es
}

func TestMRShowAndDiff(t *testing.T) {
	e := newCLIEnv(t)
	out := e.mustMR("show", rateMR)
	for _, want := range []string{rateMR + " · Rate-limit the export endpoint", "opened", "pipeline: success", "threads: 2 unresolved, 1 resolved", "unresolved:", "Is a burst of 50 too high"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "assigned to") {
		t.Errorf("system note listed as a thread:\n%s", out)
	}

	diff := e.mustMR("diff", renameMR)
	for _, want := range []string{"rename from limit/bucket.go", "rename to limit/token_bucket.go", "--- a/limit/bucket.go", "+++ b/limit/token_bucket.go", "@@ -1,7 +1,11 @@"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff lacks %q:\n%s", want, diff)
		}
	}
	if names := e.mustMR("diff", renameMR, "--name-only"); names != "limit/token_bucket.go\n" {
		t.Errorf("--name-only = %q", names)
	}
	if added := e.mustMR("diff", rateMR); !strings.Contains(added, "--- /dev/null") {
		t.Errorf("new file not shown against /dev/null:\n%s", added)
	}

	asJSON(t)
	var view struct {
		MR      forge.MR           `json:"mr"`
		Threads []forge.Discussion `json:"threads"`
	}
	if err := json.Unmarshal([]byte(e.mustMR("show", rateMR)), &view); err != nil {
		t.Fatal(err)
	}
	if view.MR.Ref.String() != rateMR || view.MR.Unresolved != 2 || len(view.Threads) != 3 { // system note excluded
		t.Errorf("show --json = %+v", view)
	}
	var d forge.Diff
	if err := json.Unmarshal([]byte(e.mustMR("diff", renameMR, "--name-only")), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0].Diff != "" || d.Files[0].NewPath != "limit/token_bucket.go" || d.HeadSHA == "" {
		t.Errorf("diff --name-only --json = %+v", d)
	}
}

func TestMRReplyResolveApproveReady(t *testing.T) {
	e := newCLIEnv(t)
	c := gitlab.New(e.srv.URL, fake.Token)
	ds, _ := c.Discussions(context.Background(), ref(rateMR))
	var thread string
	for _, d := range ds {
		if d.Resolvable && !d.Resolved {
			thread = d.ID
			break
		}
	}

	if out := e.mustMR("reply", rateMR, thread, "Will do."); !strings.Contains(out, "replied") {
		t.Errorf("reply output = %q", out)
	}
	e.mustMR("resolve", rateMR, thread)
	if mr, _ := c.MR(context.Background(), ref(rateMR)); mr.Unresolved != 1 {
		t.Errorf("unresolved = %d after resolve", mr.Unresolved)
	}
	e.mustMR("resolve", rateMR, thread, "--undo")
	if mr, _ := c.MR(context.Background(), ref(rateMR)); mr.Unresolved != 2 {
		t.Errorf("unresolved = %d after --undo", mr.Unresolved)
	}

	e.mustMR("approve", rateMR)
	if a, _ := e.srv.Approvers(ref(rateMR)); len(a) == 0 {
		t.Error("not approved")
	}
	e.mustMR("unapprove", rateMR)
	if a, _ := e.srv.Approvers(ref(rateMR)); len(a) != 0 {
		t.Errorf("still approved by %v", a)
	}

	e.mustMR("ready", webPath+"!1")
	if mr, _ := c.MR(context.Background(), ref(webPath+"!1")); mr.Draft {
		t.Error("still draft after ready")
	}
	e.mustMR("draft", webPath+"!1")
	if mr, _ := c.MR(context.Background(), ref(webPath+"!1")); !mr.Draft {
		t.Error("not draft after draft")
	}

	// Body from stdin.
	cmd := newMRCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("from stdin\n"))
	cmd.SetArgs([]string{"reply", rateMR, thread, "-"})
	if err := cmd.ExecuteContext(context.WithValue(context.Background(), ctxKey{}, e.app)); err != nil {
		t.Fatal(err)
	}
	ds, _ = c.Discussions(context.Background(), ref(rateMR))
	for _, d := range ds {
		if d.ID == thread && d.Notes[len(d.Notes)-1].Body != "from stdin" {
			t.Errorf("last note = %q", d.Notes[len(d.Notes)-1].Body)
		}
	}

	for _, want := range []string{"forge.thread.reply", "forge.thread.resolve", "forge.approve", "forge.unapprove", "forge.mr.draft"} {
		if len(e.audited(want)) == 0 {
			t.Errorf("no %s audit entry", want)
		}
	}
}

func TestMRWritesHonourDryRun(t *testing.T) {
	e := newCLIEnv(t)
	out := e.mustMR("approve", rateMR, "--dry-run")
	if !strings.Contains(out, "dry-run: would approve") {
		t.Errorf("output = %q", out)
	}
	e.mustMR("--dry-run", "merge", rateMR) // dry-run needs no --yes
	e.mustMR("ready", webPath+"!1", "--dry-run")
	e.mustMR("retry", webPath+"!1", "--dry-run")
	if a, _ := e.srv.Approvers(ref(rateMR)); len(a) != 0 {
		t.Errorf("dry run approved: %v", a)
	}
	if merged, _, _, _ := e.srv.MergeState(ref(rateMR)); merged {
		t.Error("dry run merged")
	}
	if n, _ := e.srv.Retries(ref(webPath + "!1")); n != 0 {
		t.Error("dry run retried")
	}
	if title, _ := e.srv.Title(ref(webPath + "!1")); !strings.HasPrefix(title, "Draft:") {
		t.Errorf("dry run changed the title to %q", title)
	}
	es := e.audited("forge.*")
	if len(es) != 4 {
		t.Fatalf("audit entries = %+v", es)
	}
	for _, en := range es {
		if !en.DryRun || en.Outcome != "skipped" {
			t.Errorf("entry %s not recorded as a skipped dry run: %+v", en.Action, en)
		}
	}
	asJSON(t)
	var a action
	if err := json.Unmarshal([]byte(e.mustMR("unapprove", rateMR, "--dry-run")), &a); err != nil || !a.DryRun || a.Action != "unapprove" {
		t.Errorf("json = %+v, %v", a, err)
	}
}

func TestMRMerge(t *testing.T) {
	e := newCLIEnv(t)
	out, err := e.mr("merge", rateMR)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("merge without --yes: err = %v, out %q", err, out)
	}
	if merged, _, _, _ := e.srv.MergeState(ref(rateMR)); merged {
		t.Fatal("merged without --yes")
	}
	if len(e.audited("forge.mr.merge")) != 0 {
		t.Error("a refused merge was audited as a forge write")
	}

	out = e.mustMR("merge", rateMR, "--yes", "--squash", "--remove-source-branch")
	if !strings.Contains(out, "merged "+rateMR) {
		t.Errorf("output = %q", out)
	}
	merged, _, params, _ := e.srv.MergeState(ref(rateMR))
	if !merged || params["squash"] != true || params["should_remove_source_branch"] != true || params["sha"] != "cba12b9f93fb6e59bd5f3e1adc4097f24360460c" {
		t.Errorf("merged %v params %v: the reviewed head SHA must be pinned", merged, params)
	}
	if _, err := e.mr("merge", rateMR, "--yes"); err == nil || !strings.Contains(err.Error(), "merged, not open") {
		t.Errorf("second merge err = %v", err)
	}
	// A draft cannot merge: the forge's refusal surfaces.
	if _, err := e.mr("merge", webPath+"!1", "--yes"); err == nil {
		t.Error("draft MR merged")
	}
	es := e.audited("forge.mr.merge")
	if len(es) != 2 || es[0].Outcome != "ok" || es[1].Outcome != "error" {
		t.Errorf("merge audit = %+v", es)
	}
}

func TestMRRetry(t *testing.T) {
	e := newCLIEnv(t)
	if out := e.mustMR("retry", webPath+"!1"); !strings.Contains(out, "retried pipeline #9002") {
		t.Errorf("output = %q", out)
	}
	if n, _ := e.srv.Retries(ref(webPath + "!1")); n != 1 {
		t.Errorf("retries = %d", n)
	}
	// An MR without a pipeline is skipped, not an error.
	out := e.mustMR("retry", webPath+"!2")
	if !strings.Contains(out, "no pipeline") {
		t.Errorf("output = %q", out)
	}
	es := e.audited("forge.pipeline.retry")
	if len(es) != 2 || es[1].Outcome != "skipped" || es[1].Error != "no pipeline" {
		t.Errorf("audit = %+v", es)
	}
}

func TestMRRefAndForgeErrors(t *testing.T) {
	e := newCLIEnv(t)
	for _, bad := range []string{"nope", apiPath + "#1", apiPath + "!x"} {
		if _, err := e.mr("show", bad); err == nil {
			t.Errorf("ref %q accepted", bad)
		}
	}
	if _, err := e.mr("show", rateMR, "--forge", "missing"); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Errorf("unknown forge err = %v", err)
	}
	if out := e.mustMR("show", rateMR, "--forge", "sandbox"); !strings.Contains(out, rateMR) {
		t.Errorf("explicit forge: %q", out)
	}
	// Two forges and no project to say which: ask.
	e.app.Config.Forges["other"] = config.Forge{Kind: "gitlab", URL: e.srv.URL}
	if _, err := e.mr("show", rateMR); err == nil || !strings.Contains(err.Error(), "--forge") {
		t.Errorf("ambiguous forge err = %v", err)
	}
	if _, err := e.mr("show", apiPath+"!99"); err == nil {
		t.Error("unknown MR accepted")
	}
}

func TestReviewRoundTrip(t *testing.T) {
	e := newCLIEnv(t)
	c := gitlab.New(e.srv.URL, fake.Token)
	ctx := context.Background()
	before, _ := c.Discussions(ctx, ref(renameMR))

	e.mustReview("add", renameMR, "--path", "limit/token_bucket.go", "--line", "3", "--body", "Mention refill.", "--severity", "nit")
	e.mustReview("add", renameMR, "--path", "limit/token_bucket.go", "--line", "5", "--body", "Why int?")
	e.mustReview("add", renameMR, "--path", "limit/bucket.go", "--line", "3", "--old", "--body", "Keep the old wording.")
	e.mustReview("add", renameMR, "--path", "limit/token_bucket.go", "--line", "7", "--body", "float64 is lossy.")
	e.mustReview("add", renameMR, "--path", "limit/token_bucket.go", "--line", "10", "--body", "Document the unit.")

	// Nothing is on the forge yet; the drafts are listed without network.
	if n, _ := e.srv.PendingDrafts(ref(renameMR)); n != 0 {
		t.Fatalf("%d draft notes before submit", n)
	}
	requests := len(e.srv.Requests())
	list := e.mustReview("list", renameMR)
	if got := len(e.srv.Requests()); got != requests {
		t.Error("review list went to the network")
	}
	if strings.Count(list, "\n") != 5 || !strings.Contains(list, "limit/token_bucket.go:3 new") || !strings.Contains(list, "limit/token_bucket.go:3 old") || !strings.Contains(list, "nit") {
		t.Errorf("list:\n%s", list)
	}

	// Rejected adds leave the pending review alone.
	if out, err := e.review("add", renameMR, "--path", "limit/token_bucket.go", "--line", "40", "--body", "x"); err == nil || !strings.Contains(err.Error(), "outside the diff hunks") {
		t.Errorf("out-of-hunk add: err = %v, out %q", err, out)
	}
	if _, err := e.review("add", renameMR, "--path", "limit/token_bucket.go", "--line", "3", "--body", "x", "--severity", "scary"); err == nil {
		t.Error("bad severity accepted")
	}
	if out := e.mustReview("add", renameMR, "--path", "limit/token_bucket.go", "--line", "9", "--body", "dry", "--dry-run"); !strings.Contains(out, "dry-run") {
		t.Errorf("dry-run add = %q", out)
	}

	asJSON(t)
	var ds []review.Draft
	if err := json.Unmarshal([]byte(e.mustReview("list", renameMR)), &ds); err != nil || len(ds) != 5 {
		t.Fatalf("list --json = %+v, %v", ds, err)
	}
	if ds[2].Side != review.SideOld || ds[2].Path != "limit/token_bucket.go" || ds[2].OldPath != "limit/bucket.go" {
		t.Errorf("old-side draft = %+v", ds[2])
	}
	jsonOut = false

	// A dry-run submit changes nothing.
	e.mustReview("submit", renameMR, "--dry-run")
	if n, _ := e.srv.PendingDrafts(ref(renameMR)); n != 0 {
		t.Fatal("dry-run submit uploaded drafts")
	}

	out := e.mustReview("submit", renameMR, "--approve", "--summary", "Mostly nits.")
	if !strings.Contains(out, "submitted 6 comments") || !strings.Contains(out, "approved") {
		t.Errorf("submit output = %q", out)
	}
	if n, _ := e.srv.Publishes(ref(renameMR)); n != 1 {
		t.Errorf("publishes = %d, want one batch", n)
	}
	after, _ := c.Discussions(ctx, ref(renameMR))
	if len(after) != len(before)+6 {
		t.Fatalf("discussions %d -> %d, want +6 (5 line comments and the summary)", len(before), len(after))
	}
	var renamed int
	for _, d := range after[len(before):] {
		if d.Position != nil {
			renamed++
			if d.Position.Path != "limit/token_bucket.go" || d.Position.OldPath != "limit/bucket.go" {
				t.Errorf("position = %+v", d.Position)
			}
		}
	}
	if renamed != 5 {
		t.Errorf("%d positioned threads, want 5", renamed)
	}
	if a, _ := e.srv.Approvers(ref(renameMR)); len(a) != 1 {
		t.Errorf("approvers = %v", a)
	}
	if out := e.mustReview("list", renameMR); !strings.Contains(out, "no pending review") {
		t.Errorf("list after submit:\n%s", out)
	}
	if es := e.audited("review.submit"); len(es) != 2 || !es[0].DryRun || es[1].Outcome != "ok" || es[1].Params["published"] != float64(6) {
		t.Errorf("review.submit audit = %+v", es)
	}
	for action, want := range map[string]int{"forge.draftnote.add": 6, "forge.draftnote.publish": 1, "forge.approve": 1} {
		if es := e.audited(action); len(es) != want {
			t.Errorf("%s audit entries = %d, want %d (the dry run must add none)", action, len(es), want)
		}
	}

	if _, err := e.review("submit", renameMR); err == nil || !strings.Contains(err.Error(), "nothing to submit") {
		t.Errorf("empty submit err = %v", err)
	}
}

func TestReviewRemove(t *testing.T) {
	e := newCLIEnv(t)
	asJSON(t)
	var d review.Draft
	if err := json.Unmarshal([]byte(e.mustReview("add", renameMR, "--path", "limit/token_bucket.go", "--line", "3", "--body", "x")), &d); err != nil || d.ID == "" {
		t.Fatalf("add --json = %+v, %v", d, err)
	}
	if _, err := e.review("rm", renameMR, "nope"); err == nil {
		t.Error("removing an unknown draft succeeded")
	}
	e.mustReview("rm", renameMR, d.ID)
	var ds []review.Draft
	if err := json.Unmarshal([]byte(e.mustReview("list", renameMR)), &ds); err != nil || len(ds) != 0 {
		t.Errorf("after rm: %+v, %v", ds, err)
	}
}

// seedPrefixedItem caches a work item owning rateMR and writes the project's
// manifest snapshot with commit_prefix = "GL-{parent}".
func (e *cliEnv) seedPrefixedItem(t *testing.T, tmpl string) {
	t.Helper()
	root := t.TempDir()
	e.app.Root = root
	dir := filepath.Join(root, "acme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	man := "name = \"acme\"\nforge = \"sandbox\"\n[tracker]\nproject = \"" + fake.GroupPath + "/tracker\"\n" +
		"[repos.api]\npath = \"" + apiPath + "\"\n[conventions]\ncommit_prefix = \"" + tmpl + "\"\n"
	snap := "source = \"test\"\nmanifest = '''\n" + man + "'''\n"
	if err := os.WriteFile(filepath.Join(dir, ".project.toml"), []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	wi := domain.WorkItem{
		Ref: forge.Ref{Project: fake.GroupPath + "/tracker", IID: 7, Kind: forge.KindIssue},
		MRs: []forge.MR{{Ref: ref(rateMR)}},
	}
	raw, err := json.Marshal(wi)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.app.Cache()
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]any{"project": "acme", "forge": "sandbox"})
	if _, err := st.DB.Exec(`INSERT INTO sync_state (project, json) VALUES ('acme', ?)`, string(state)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO work_items (project, ref, json) VALUES ('acme', ?, ?)`, wi.Ref.String(), string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestMRMergeCommitPrefix(t *testing.T) {
	e := newCLIEnv(t)
	e.seedPrefixedItem(t, "GL-{parent}")
	title := "GL-7 Rate-limit the export endpoint"

	out := e.mustMR("merge", rateMR, "--dry-run", "--squash")
	for _, want := range []string{"commit message:", "See merge request " + rateMR, title, "squash message:"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out)
		}
	}
	if merged, _, _, _ := e.srv.MergeState(ref(rateMR)); merged {
		t.Fatal("dry run merged")
	}

	e.mustMR("merge", rateMR, "--yes", "--squash")
	_, _, params, _ := e.srv.MergeState(ref(rateMR))
	msg, _ := params["merge_commit_message"].(string)
	if !strings.HasPrefix(msg, "Merge branch '") || !strings.Contains(msg, "\n\n"+title+"\n") || !strings.HasSuffix(msg, "See merge request "+rateMR) {
		t.Errorf("merge_commit_message = %q", msg)
	}
	if params["squash_commit_message"] != title {
		t.Errorf("squash_commit_message = %v, want %q", params["squash_commit_message"], title)
	}
}

func TestMRMergeWithoutPrefixSendsNoMessages(t *testing.T) {
	e := newCLIEnv(t)
	e.seedPrefixedItem(t, "")
	e.mustMR("merge", rateMR, "--yes", "--squash")
	_, _, params, _ := e.srv.MergeState(ref(rateMR))
	for _, k := range []string{"merge_commit_message", "squash_commit_message"} {
		if _, ok := params[k]; ok {
			t.Errorf("%s sent without a commit prefix: %v", k, params)
		}
	}
}
