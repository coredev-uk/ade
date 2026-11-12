package review

import (
	"context"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/audit"
	"github.com/coredev-uk/ade/internal/forge"
)

// textWriter records description writes; the rest of MRWriter is never called.
type textWriter struct {
	forge.MRWriter
	set []string
}

func (w *textWriter) MRCommits(context.Context, forge.Ref) ([]forge.Commit, error) { return nil, nil }

func (w *textWriter) SetMRDescription(_ context.Context, _ forge.Ref, d string) error {
	w.set = append(w.set, d)
	return nil
}

func TestSetDescriptionIsAuditedWithoutTheText(t *testing.T) {
	log, err := audit.Open(t.TempDir(), "user")
	if err != nil {
		t.Fatal(err)
	}
	mr := forge.Ref{Project: "grp/api", IID: 2, Kind: forge.KindMR}
	w := &textWriter{}
	if err := Audited(w, log, false).SetDescription(context.Background(), mr, "secret-ish body", "ai-describe"); err != nil {
		t.Fatal(err)
	}
	es, _ := log.Query(audit.Filter{Action: "forge.mr.description"})
	if len(w.set) != 1 || len(es) != 1 || es[0].Item != mr.String() || es[0].Params["chars"] != float64(len("secret-ish body")) || es[0].Params["source"] != "ai-describe" {
		t.Fatalf("writes=%v audit=%+v", w.set, es)
	}
	if strings.Contains(es[0].Error, "secret") || es[0].Outcome != "ok" {
		t.Fatalf("entry = %+v", es[0])
	}

	// A dry run writes nothing and records that it would have.
	if err := Audited(w, log, true).SetDescription(context.Background(), mr, "x", "ai-describe"); err != nil || len(w.set) != 1 {
		t.Fatalf("dry run wrote: %v %v", err, w.set)
	}
	es, _ = log.Query(audit.Filter{Action: "forge.mr.description"})
	if len(es) != 2 || !es[1].DryRun || es[1].Outcome != "skipped" {
		t.Fatalf("dry-run audit: %+v", es)
	}
}

func TestSetDescriptionOnADriverWithoutTextSupportIsAnError(t *testing.T) {
	err := Audited(&struct{ forge.MRWriter }{}, nil, false).SetDescription(context.Background(), forge.Ref{Project: "g/p", IID: 1, Kind: forge.KindMR}, "x", "ai-describe")
	if err == nil || !strings.Contains(err.Error(), "cannot update merge request descriptions") {
		t.Fatalf("err = %v", err)
	}
}
