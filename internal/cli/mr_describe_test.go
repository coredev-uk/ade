package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge/github"
	ghfake "github.com/coredev-uk/ade/internal/forge/github/fake"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/review"
)

// newGitHubCLIEnv is cliEnv over the fake GitHub: one configured forge, so the
// refs resolve without any registered project. Its srv is nil; the fake is
// returned beside it.
func newGitHubCLIEnv(t *testing.T) (*cliEnv, *ghfake.Server) {
	t.Helper()
	srv := ghfake.New()
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	log, err := audit.Open(dir, "user")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Forges = map[string]config.Forge{"hub": {Kind: "github", URL: srv.URL}}
	a := &app.App{Paths: paths.Paths{CacheDir: dir}, Config: &cfg, Audit: log, Bus: events.NewBus()}
	t.Cleanup(func() { a.Close() })
	old := review.Dial
	review.Dial = func(_ context.Context, _ string, f config.Forge) (review.Backend, error) {
		return github.New(f.URL, ghfake.Token), nil
	}
	t.Cleanup(func() { review.Dial = old })
	return &cliEnv{t: t, app: a}, srv
}

func bodyFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runIn is e.mr with stdin supplied.
func (e *cliEnv) mrIn(stdin string, args ...string) (string, error) {
	e.t.Helper()
	return e.run(func() *cobra.Command {
		cmd := newMRCmd()
		cmd.SetIn(strings.NewReader(stdin))
		return cmd
	}, args...)
}

func TestMRDescribeWritesYourTextAuditedAsManual(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	titleBefore, _ := e.srv.Title(r)
	const text = "## What\nA per-tenant token bucket.\n\nCloses #1\n"
	out := e.mustMR("describe", rateMR, "--body-file", bodyFile(t, text), "--yes")
	if !strings.Contains(out, rateMR+": description updated") || !strings.Contains(out, "+ A per-tenant token bucket.") {
		t.Errorf("output = %q", out)
	}
	d, n, _ := e.srv.Description(r)
	if d != strings.TrimSpace(text) || n != 1 {
		t.Fatalf("description %q after %d writes", d, n)
	}
	if title, _ := e.srv.Title(r); title != titleBefore {
		t.Errorf("the title changed: %q -> %q", titleBefore, title)
	}
	es := e.audited("forge.mr.description")
	if len(es) != 1 || es[0].Item != rateMR || es[0].Outcome != "ok" || es[0].DryRun ||
		es[0].Params["source"] != "manual" || es[0].Params["chars"] != float64(len(strings.TrimSpace(text))) {
		t.Fatalf("audit: %+v", es)
	}
	if strings.Contains(es[0].Error+toJSON(t, es[0]), "token bucket") {
		t.Error("the text itself must not be logged, only its length")
	}

	// The same text again is no write.
	out = e.mustMR("describe", rateMR, "--body-file", bodyFile(t, text), "--yes")
	if !strings.Contains(out, "already has this description") {
		t.Errorf("output = %q", out)
	}
	if _, n, _ := e.srv.Description(r); n != 1 {
		t.Errorf("an unchanged description was written again (%d writes)", n)
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMRDescribeDryRunPreviewsADiffAndWritesNothing(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	before, _, _ := e.srv.Description(r)
	out := e.mustMR("--dry-run", "describe", rateMR, "--body-file", bodyFile(t, "Brand new text"))
	for _, want := range []string{rateMR + ": description", "- ", "+ Brand new text", "dry-run: would replace the description of " + rateMR} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lacks %q:\n%s", want, out)
		}
	}
	if d, n, _ := e.srv.Description(r); d != before || n != 0 {
		t.Fatalf("a dry run wrote: %q (%d writes)", d, n)
	}
	es := e.audited("forge.mr.description")
	if len(es) != 1 || !es[0].DryRun || es[0].Outcome != "skipped" || es[0].Params["source"] != "manual" {
		t.Fatalf("a dry run is still audited as skipped: %+v", es)
	}
	asJSON(t)
	var a describeAction
	if err := json.Unmarshal([]byte(e.mustMR("--dry-run", "describe", rateMR, "--body-file", bodyFile(t, "Brand new text"))), &a); err != nil || !a.DryRun || a.Applied || a.Action != "describe" {
		t.Errorf("json = %+v, %v", a, err)
	}
}

func TestMRDescribeAsksFirstAndAScriptMustSayYes(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	before, _, _ := e.srv.Description(r)
	_, err := e.mr("describe", rateMR, "--body-file", bodyFile(t, "Brand new text"))
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitNeedsConfirm || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if d, n, _ := e.srv.Description(r); d != before || n != 0 {
		t.Fatal("wrote without a confirmation")
	}
	if len(e.audited("forge.mr.description")) != 0 {
		t.Error("nothing was attempted, so nothing is audited")
	}
}

func TestMRDescribeReadsStdinAndRefusesWhatItShould(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	if _, err := e.mrIn("From a pipe\n", "describe", rateMR, "--body-file", "-", "--yes"); err != nil {
		t.Fatal(err)
	}
	if d, _, _ := e.srv.Description(r); d != "From a pipe" {
		t.Errorf("description %q", d)
	}
	for name, args := range map[string][]string{
		"neither flag": {"describe", rateMR},
		"both flags":   {"describe", rateMR, "--edit", "--body-file", "x"},
		"empty text":   {"describe", rateMR, "--body-file", bodyFile(t, " \n"), "--yes"},
		"missing file": {"describe", rateMR, "--body-file", filepath.Join(t.TempDir(), "none.md"), "--yes"},
		"an issue":     {"describe", fakeIssueRef(), "--body-file", bodyFile(t, "x"), "--yes"},
	} {
		_, n0, _ := e.srv.Description(r)
		if _, err := e.mr(args...); err == nil {
			t.Errorf("%s: no error", name)
		}
		if _, n, _ := e.srv.Description(r); n != n0 {
			t.Errorf("%s: wrote", name)
		}
	}
	// A merged MR keeps its description.
	e.mustMR("merge", rateMR, "--yes")
	if _, err := e.mr("describe", rateMR, "--body-file", bodyFile(t, "Too late"), "--yes"); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Errorf("merged: %v", err)
	}
}

func fakeIssueRef() string { return apiPath + "#1" }

func TestMRDescribeWarnsAboutQuickActionsOnGitLabAndSendsThemAsWritten(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	text := "Closes #1\n/assign @sandbox-dev\n```\n/close\n```\n/label ~bug"
	out := e.mustMR("describe", rateMR, "--body-file", bodyFile(t, text), "--yes")
	if !strings.Contains(out, "warning: /assign, /close, /label run as quick actions when GitLab saves this description") {
		t.Errorf("no quick-action warning:\n%s", out)
	}
	if d, _, _ := e.srv.Description(r); d != text {
		t.Errorf("manual text must not be stripped: %q", d)
	}
	asJSON(t)
	var a describeAction
	if err := json.Unmarshal([]byte(e.mustMR("--dry-run", "describe", rateMR, "--body-file", bodyFile(t, text+"\n  /ready"))), &a); err != nil || strings.Join(a.QuickActions, " ") != "/assign /close /label /ready" {
		t.Errorf("json = %+v, %v", a, err)
	}
}

func TestMRDescribeEditSeedsTheEditorWithTheCurrentDescription(t *testing.T) {
	e := newCLIEnv(t)
	r := ref(rateMR)
	before, _, _ := e.srv.Description(r)
	script := filepath.Join(t.TempDir(), "editor.sh")
	seen := filepath.Join(t.TempDir(), "seen.md")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nfile=\"$(for a in \"$@\"; do last=$a; done; echo \"$last\")\"\ncp \"$file\" '"+seen+"'\nprintf '\\nAdded by hand\\n' >> \"$file\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", script)
	t.Setenv("EDITOR", script)
	t.Setenv("VSCODE_IPC_HOOK_CLI", "")
	t.Setenv("TERM_PROGRAM", "")
	out := e.mustMR("describe", rateMR, "--edit", "--yes")
	got, err := os.ReadFile(seen)
	if err != nil || strings.TrimSpace(string(got)) != strings.TrimSpace(before) {
		t.Fatalf("the editor was seeded with %q, want the current description %q (%v)", got, before, err)
	}
	d, n, _ := e.srv.Description(r)
	if n != 1 || !strings.HasSuffix(d, "Added by hand") || !strings.HasPrefix(d, strings.TrimSpace(before)) {
		t.Fatalf("description %q after %d writes\n%s", d, n, out)
	}
	if es := e.audited("forge.mr.description"); len(es) != 1 || es[0].Params["source"] != "manual" {
		t.Errorf("audit: %+v", es)
	}
}

func TestMRDescribeOnGitHub(t *testing.T) {
	e, srv := newGitHubCLIEnv(t)
	const pr = "ade-demo/api!2"
	r := ref(pr)
	before, _, err := srv.Description(r)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := e.mr("--dry-run", "describe", pr, "--body-file", bodyFile(t, "/close\nHello GitHub")); err != nil || strings.Contains(out, "quick action") || !strings.Contains(out, "dry-run: would replace") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if d, n, _ := srv.Description(r); d != before || n != 0 {
		t.Fatal("a dry run wrote")
	}
	out := e.mustMR("describe", pr, "--body-file", bodyFile(t, "## What\nHello GitHub\n"), "--yes")
	if !strings.Contains(out, pr+": description updated") {
		t.Errorf("output = %q", out)
	}
	d, n, _ := srv.Description(r)
	if d != "## What\nHello GitHub" || n != 1 {
		t.Fatalf("body %q after %d writes", d, n)
	}
	es := e.audited("forge.mr.description")
	if len(es) != 2 || !es[0].DryRun || es[1].DryRun || es[1].Params["source"] != "manual" || es[1].Outcome != "ok" {
		t.Fatalf("audit: %+v", es)
	}
}
