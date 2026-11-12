package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/config"
	"github.com/coredev-uk/ade/internal/forge"
)

// capServer answers every chat request. Each entry of replies is the answer
// to one request; caps records the max_tokens (or max_completion_tokens) of
// each. The last reply repeats.
func capServer(t *testing.T, replies []string, caps *[]float64) *httptest.Server {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		c, _ := body["max_tokens"].(float64)
		if c == 0 {
			c, _ = body["max_completion_tokens"].(float64)
		}
		*caps = append(*caps, c)
		io.WriteString(w, replies[min(n, len(replies)-1)])
		n++
	}))
	t.Cleanup(srv.Close)
	return srv
}

func openAIReply(text, finish string) string {
	return fmt.Sprintf(`{"model":"m","choices":[{"message":{"content":%q},"finish_reason":%q}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, text, finish)
}

func anthropicReply(text, stop string) string {
	return fmt.Sprintf(`{"model":"m","stop_reason":%q,"content":[{"type":"text","text":%q}],"usage":{"input_tokens":10,"output_tokens":5}}`, stop, text)
}

func TestOpenAICompatLengthStopRetriesOnceWithDoubleTheCap(t *testing.T) {
	var caps []float64
	srv := capServer(t, []string{openAIReply("adds an", "length"), openAIReply("adds an endpoint for tailwind.", "stop")}, &caps)
	d := newOpenAI(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client())
	r, err := d.Complete(context.Background(), Call{Model: "m", MaxTokens: 200})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "adds an endpoint for tailwind." || r.Truncated {
		t.Fatalf("the retried answer should win: %+v", r)
	}
	if len(caps) != 2 || caps[0] != 200 || caps[1] != 400 {
		t.Fatalf("caps sent = %v, want [200 400]", caps)
	}
	if r.Usage.In != 20 || r.Usage.Out != 10 {
		t.Fatalf("both attempts are paid for: %+v", r.Usage)
	}
}

func TestOpenAICompatStillTruncatedAfterRetryIsReportedNotRetriedAgain(t *testing.T) {
	var caps []float64
	srv := capServer(t, []string{openAIReply("adds an", "length")}, &caps)
	d := newOpenAI(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client())
	r, err := d.Complete(context.Background(), Call{Model: "m", MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || r.Text != "adds an" || len(caps) != 2 {
		t.Fatalf("want a truncated answer after exactly one retry: %+v, caps %v", r, caps)
	}
}

func TestOpenAICompatLengthStopWithoutACapIsNotRetried(t *testing.T) {
	var caps []float64
	srv := capServer(t, []string{openAIReply("adds an", "length")}, &caps)
	d := newOpenAI(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client())
	r, err := d.Complete(context.Background(), Call{Model: "m"})
	if err != nil || !r.Truncated || len(caps) != 1 {
		t.Fatalf("no cap was sent, so there is nothing to double: %+v %v %v", r, err, caps)
	}
}

func TestOpenAICompatSwitchesToMaxCompletionTokensWhenRefused(t *testing.T) {
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body)
		if _, ok := body["max_tokens"]; ok {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}}`)
			return
		}
		io.WriteString(w, openAIReply("done", "stop"))
	}))
	defer srv.Close()
	d := newOpenAI(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client())
	for range 2 {
		r, err := d.Complete(context.Background(), Call{Model: "o", MaxTokens: 600})
		if err != nil || r.Text != "done" {
			t.Fatalf("%+v %v", r, err)
		}
	}
	// One refused request, then the retry; the second call goes straight out.
	if len(seen) != 3 || seen[1]["max_completion_tokens"] != float64(600) || seen[2]["max_completion_tokens"] != float64(600) {
		t.Fatalf("requests: %+v", seen)
	}
}

func TestAnthropicMaxTokensStopRetriesOnceWithDoubleTheCap(t *testing.T) {
	var caps []float64
	srv := capServer(t, []string{anthropicReply("adds an", "max_tokens"), anthropicReply("adds an endpoint.", "end_turn")}, &caps)
	d := newAnthropic(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client())
	r, err := d.Complete(context.Background(), Call{Model: "m", MaxTokens: 200})
	if err != nil || r.Truncated || r.Text != "adds an endpoint." {
		t.Fatalf("%+v %v", r, err)
	}
	if len(caps) != 2 || caps[0] != 200 || caps[1] != 400 {
		t.Fatalf("caps sent = %v, want [200 400]", caps)
	}
}

func TestAnthropicStillTruncatedAfterRetryIsReported(t *testing.T) {
	var caps []float64
	srv := capServer(t, []string{anthropicReply("adds an", "max_tokens")}, &caps)
	d := newAnthropic(config.AIProfile{BaseURL: srv.URL}, "k", srv.Client())
	r, err := d.Complete(context.Background(), Call{Model: "m"})
	if err != nil || !r.Truncated {
		t.Fatalf("%+v %v", r, err)
	}
	if len(caps) != 2 || caps[0] != 1024 || caps[1] != 2048 {
		t.Fatalf("the default cap doubles too: %v", caps)
	}
}

func TestParseOMPReportsALengthStop(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens"} {
		out := `{"type":"turn_end","message":{"role":"assistant","stopReason":"` + reason + `","content":[{"type":"text","text":"adds an"}],"usage":{}}}` + "\n"
		r, err := parseOMP([]byte(out))
		if err != nil || !r.Truncated || r.Text != "adds an" {
			t.Errorf("%s: %+v %v", reason, r, err)
		}
	}
	r, _ := parseOMP([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"ok"}],"usage":{}}}` + "\n"))
	if r.Truncated {
		t.Fatalf("a normal stop is not truncated: %+v", r)
	}
}

func TestTruncatedAnswerIsFiledButNeverCached(t *testing.T) {
	e := newEnv(t)
	e.drv.resp.Text = "adds an"
	e.drv.resp.Truncated = true
	ctx := context.Background()
	first, err := e.svc.Run(ctx, summarizeJob("abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || first.Text != "adds an" || first.Artifact == "" || first.CutShort() == "" {
		t.Fatalf("first run: %+v", first)
	}
	if strings.Contains(first.Text, "cut short") {
		t.Errorf("the note must not be part of the text: %q", first.Text)
	}
	if a, err := e.svc.Records.ReadArtifact(first.Artifact); err != nil || !strings.Contains(a.Body, TruncatedNote) {
		t.Errorf("the record should say it was cut short: %v %q", err, a.Body)
	}
	// The same prompt is not bought again by itself, only on a refresh.
	if _, err := e.svc.Run(ctx, summarizeJob("abc123")); !errors.Is(err, ErrCutShortRecently) || e.drv.calls != 1 {
		t.Fatalf("an unchanged prompt cut short a moment ago: err=%v calls=%d", err, e.drv.calls)
	}
	job := summarizeJob("abc123")
	job.Refresh = true
	second, err := e.svc.Run(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHit || e.drv.calls != 2 {
		t.Fatalf("a refresh must ask again: hit=%v calls=%d", second.CacheHit, e.drv.calls)
	}
	// Rebuilding the index from the artifacts must not bring it back either.
	if n, err := e.svc.Cache.Reindex(); err != nil || n != 0 {
		t.Fatalf("reindex found %d answers (%v), want none", n, err)
	}
	es := e.entries("ai.summarize")
	if len(es) != 2 || es[0].Params["truncated"] != true {
		t.Fatalf("the audit entry should say it was cut short: %+v", es)
	}
}

func TestRetryTruncatedKeepsTheFragmentWhenTheRetryFails(t *testing.T) {
	calls := 0
	r, err := retryTruncated(context.Background(), Call{MaxTokens: 10}, func(context.Context, Call) (Response, error) {
		calls++
		if calls == 1 {
			return Response{Text: "adds an", Truncated: true, CostUSD: 0.5}, nil
		}
		return Response{}, fmt.Errorf("boom")
	})
	if err != nil || !r.Truncated || r.Text != "adds an" || r.CostUSD != 0.5 {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = retryTruncated(context.Background(), Call{MaxTokens: 10}, func(context.Context, Call) (Response, error) {
		calls++
		if calls == 3 {
			return Response{Truncated: true}, nil
		}
		return Response{}, &StopError{Source: "gateway", Message: "budget"}
	})
	if _, ok := AsStop(err); !ok {
		t.Fatalf("a budget stop on the retry must surface: %v", err)
	}
}

func TestSmallCapJobsLeaveRoomForReasoning(t *testing.T) {
	ref := forge.Ref{Project: "grp/issues", IID: 3, Kind: forge.KindIssue}
	in := ClassifyInput{
		Issue:  forge.Issue{Ref: ref, Title: "t"},
		Labels: config.Labels{Type: config.LabelFacet{Pattern: "type::{value}"}},
	}
	classify, err := ClassifyJob("sandbox", in)
	if err != nil {
		t.Fatal(err)
	}
	mr := forge.MR{Ref: forge.Ref{Project: "grp/api", IID: 1, Kind: forge.KindMR}}
	for name, j := range map[string]Job{
		"classify":      classify,
		"sprint-parent": SprintParentJob("p", "s", ref, "facts"),
		"thread":        ThreadSummaryJob("sandbox", mr, forge.Discussion{ID: "d1"}, forge.Diff{}),
	} {
		if j.MaxTokens < 600 {
			t.Errorf("%s cap %d is too small for a reasoning model", name, j.MaxTokens)
		}
	}
}
