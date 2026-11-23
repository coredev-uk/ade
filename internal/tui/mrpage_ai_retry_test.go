package tui

import (
	"errors"
	"testing"

	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

// After a failure the screen says "A retries": the retry must ask past the
// cache, or a cached unusable (or recently cut-short) answer comes back for
// ever.
func TestRetryAfterAFailureAsksPastTheCache(t *testing.T) {
	e := newMREnv(t, fake.Free)
	e.enableAI("auto")
	ref := mrRef("api", 2)
	e.open(ref.String())
	d := e.m.mr.data
	k := aiKey{kind: aiReview}
	d.ai = map[aiKey]*aiText{k: {l: load{st: loadFailed, err: errors.New("the model's answer was not usable")}}}
	_ = e.m.aiReviewKey()
	if st := d.ai[k]; st == nil || !st.refresh || !st.l.busy() {
		t.Fatalf("A after a failure must refresh: %+v", st)
	}

	// A first ask does not refresh: the cache is the point.
	d.ai = map[aiKey]*aiText{}
	_ = e.m.aiReviewKey()
	if st := d.ai[k]; st == nil || st.refresh {
		t.Fatalf("a first A must use the cache: %+v", st)
	}

	// The same for the thread keys.
	e.press("2")
	id := d.threadList()[0].d.ID
	tk := aiKey{kind: aiReply, id: id}
	d.ai = map[aiKey]*aiText{tk: {l: load{st: loadFailed, err: errors.New("x")}}}
	_ = e.m.threadAIKey("d", d.threadList()[0])
	if st := d.ai[tk]; st == nil || !st.refresh {
		t.Fatalf("d after a failure must refresh: %+v", st)
	}
}
