//go:build live

package tui

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/app"
	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/domain"
	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab"
	"github.com/coredev-uk/ade/internal/forgesync"
	"github.com/coredev-uk/ade/internal/notify"
	"github.com/coredev-uk/ade/internal/paths"
	"github.com/coredev-uk/ade/internal/review"
)

// The MR page's AI summary against the live sandbox (api!2 of the Free GitLab
// group) and a real omp: one paid call under the per-call cap, then a free
// cache hit in a fresh session. Spend is a few thousandths of a dollar (haiku).
//
//	set -a; . ./.env; set +a; go test -tags live -run TestLiveMRPageSummary -v ./internal/tui
func TestLiveMRPageSummary(t *testing.T) {
	tok := os.Getenv("GITLAB_PAT")
	if tok == "" {
		t.Skip("GITLAB_PAT is not set")
	}
	if _, err := exec.LookPath("omp"); err != nil {
		t.Skip("omp is not installed")
	}
	group := os.Getenv("ADE_SANDBOX_GROUP")
	if group == "" {
		group = "ade-demo-env"
	}
	settleTimeout = 5 * time.Minute
	t.Cleanup(func() { settleTimeout = 10 * time.Second })
	ctx := context.Background()
	api := gitlab.New("https://gitlab.com", tok)
	ref := forge.Ref{Project: group + "/api", IID: 2, Kind: forge.KindMR}
	mr, err := api.MR(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	me, err := api.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.AI.Mode, cfg.AI.Profile = "auto", "sub"
	cfg.AI.Profiles["sub"] = config.AIProfile{
		Driver: "omp",
		Models: map[string]string{"explain": "anthropic/claude-haiku-4-5", "draft": "anthropic/claude-haiku-4-5", "review": "anthropic/claude-haiku-4-5"},
	}
	cfg.Budget.CallUSD = 0.05
	a := &app.App{Paths: paths.Paths{CacheDir: t.TempDir()}, Config: &cfg, Bus: events.NewBus(), RecordsDir: t.TempDir()}
	t.Cleanup(func() { a.Close() })
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	a.Audit = log
	st, err := a.Cache()
	if err != nil {
		t.Fatal(err)
	}

	session := func() *mrEnv {
		res := forgesync.Result{Project: "demo", Forge: "sandbox-gitlab", Tracker: group + "/tracker", Me: me, Items: []domain.WorkItem{}, Reviews: []forge.MR{mr}}
		m := model{
			ctx: ctx, app: a, cache: st, g: unicodeGlyphs, w: 140, h: 44,
			nc:      newNotifCenter(notify.NewInbox(st)),
			reviews: review.NewStore(st), be: &mrBackends{},
			dial: func(context.Context, forge.Ref, string) (review.Backend, error) { return api, nil },
		}
		m.setResults([]forgesync.Result{res})
		m.homeLoaded = true
		return &mrEnv{t: t, api: api, m: m, log: log, store: m.reviews}
	}

	e := session()
	e.open(ref.String())
	e.want("Summary", "AI · claude-haiku-4-5")
	text := e.m.mr.data.ai[aiKey{kind: aiSummary}].text
	if len(text) < 40 {
		t.Fatalf("summary text = %q", text)
	}
	e.press("d")
	e.want("Description", "d shows the AI summary")

	es := e.audited("ai.summarize")
	if len(es) != 1 || es[0].AI == nil || es[0].AI.CacheHit || es[0].AI.CostUSD <= 0 || es[0].AI.CostUSD > 0.05 {
		t.Fatalf("first summary audit: %+v", es)
	}
	t.Logf("first summary: $%.4f, %d chars", es[0].AI.CostUSD, len(text))

	// A new session (nothing in memory): the artifact cache answers for free.
	e = session()
	e.open(ref.String())
	e.want("Summary", "cached, $0")
	if got := e.m.mr.data.ai[aiKey{kind: aiSummary}].text; got != text {
		t.Errorf("the cached summary differs:\n%q\n%q", got, text)
	}
	es = e.audited("ai.summarize")
	if len(es) != 2 || es[1].AI == nil || !es[1].AI.CacheHit || es[1].AI.CostUSD != 0 {
		t.Fatalf("repeat summary audit: %+v", es)
	}
	if strings.Count(e.screen(), "Summary") == 0 {
		t.Error("no Summary on screen")
	}
}
