package review_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/review"
)

// renameDiff mirrors the sandbox's renamed file: limit/bucket.go moved to
// limit/token_bucket.go with edits. Old lines run 1-7, new lines 1-11.
func renameDiff() forge.Diff {
	return forge.Diff{
		BaseSHA: "base", StartSHA: "start", HeadSHA: "head",
		Files: []forge.DiffFile{
			{
				OldPath: "limit/bucket.go", NewPath: "limit/token_bucket.go", Renamed: true,
				Diff: "@@ -1,7 +1,11 @@\n package limit\n \n-// Bucket is a per-tenant token bucket.\n+// Bucket refills continuously.\n type Bucket struct {\n \tRate  int\n \tBurst int\n+\tTokens float64\n }\n+\n+// Allow reports whether a token is free.\n+func (b *Bucket) Allow() bool { return b.Tokens >= 1 }\n",
			},
			{
				OldPath: "api/handler.go", NewPath: "api/handler.go",
				Diff: "@@ -10,3 +10,4 @@ func Export() {\n \tx := 1\n+\ty := 2\n \treturn\n@@ -40,2 +41,2 @@ func Other() {\n-\told()\n+\tnew()\n \tdone()\n",
			},
			{OldPath: "big.bin", NewPath: "big.bin"}, // diff text withheld
		},
	}
}

func TestLineFor(t *testing.T) {
	d := renameDiff()
	tests := []struct {
		name         string
		path         string
		line         int
		old          bool
		oldL, newL   int
		oldP, newP   string
		wantErr      error
		wantErrMatch string
	}{
		{name: "added line", path: "limit/token_bucket.go", line: 3, oldP: "limit/bucket.go", newP: "limit/token_bucket.go", newL: 3},
		{name: "added line at the end", path: "limit/token_bucket.go", line: 11, oldP: "limit/bucket.go", newP: "limit/token_bucket.go", newL: 11},
		// Unchanged lines carry both numbers, which differ after the added Tokens line.
		{name: "context line after an insertion", path: "limit/token_bucket.go", line: 8, oldP: "limit/bucket.go", newP: "limit/token_bucket.go", oldL: 7, newL: 8},
		{name: "context line before any change", path: "limit/token_bucket.go", line: 1, oldP: "limit/bucket.go", newP: "limit/token_bucket.go", oldL: 1, newL: 1},
		{name: "found by old path too", path: "limit/bucket.go", line: 4, oldP: "limit/bucket.go", newP: "limit/token_bucket.go", oldL: 4, newL: 4},
		{name: "removed line (old side)", path: "limit/token_bucket.go", line: 3, old: true, oldP: "limit/bucket.go", newP: "limit/token_bucket.go", oldL: 3},
		{name: "old side of a context line", path: "limit/token_bucket.go", line: 7, old: true, oldP: "limit/bucket.go", newP: "limit/token_bucket.go", oldL: 7, newL: 8},
		{name: "second hunk added", path: "api/handler.go", line: 41, oldP: "api/handler.go", newP: "api/handler.go", newL: 41},
		{name: "second hunk context", path: "api/handler.go", line: 42, oldP: "api/handler.go", newP: "api/handler.go", oldL: 41, newL: 42},
		{name: "second hunk removed", path: "api/handler.go", line: 40, old: true, oldP: "api/handler.go", newP: "api/handler.go", oldL: 40},
		{name: "past the file", path: "limit/token_bucket.go", line: 12, wantErr: review.ErrNotInDiff},
		{name: "between hunks", path: "api/handler.go", line: 20, wantErr: review.ErrNotInDiff},
		{name: "before the first hunk", path: "api/handler.go", line: 9, wantErr: review.ErrNotInDiff},
		{name: "old line that was never there", path: "limit/token_bucket.go", line: 9, old: true, wantErr: review.ErrNotInDiff},
		{name: "unknown file", path: "nope.go", line: 1, wantErr: review.ErrNoFile},
		{name: "withheld diff", path: "big.bin", line: 1, wantErr: review.ErrNotInDiff},
		{name: "line zero", path: "limit/token_bucket.go", line: 0, wantErr: review.ErrNotInDiff},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			locate := review.LineFor
			if tc.old {
				locate = review.OldLineFor
			}
			got, err := locate(d, tc.path, tc.line)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := forge.DiffPosition{
				BaseSHA: "base", StartSHA: "start", HeadSHA: "head",
				OldPath: tc.oldP, NewPath: tc.newP, OldLine: tc.oldL, NewLine: tc.newL,
			}
			if got != want {
				t.Errorf("position = %+v, want %+v", got, want)
			}
		})
	}
}

func TestLineForErrorListsCommentableLines(t *testing.T) {
	_, err := review.LineFor(renameDiff(), "api/handler.go", 20)
	if err == nil || !errors.Is(err, review.ErrNotInDiff) {
		t.Fatal(err)
	}
	if want := "commentable: 10-12, 41-42"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q lacks %q", err, want)
	}
}

func TestLines(t *testing.T) {
	f := renameDiff().Files[0]
	ls := review.Lines(f)
	if len(ls) != 12 {
		t.Fatalf("%d lines, want 12", len(ls))
	}
	if ls[2] != (review.Line{Old: 3, Kind: '-', Text: "// Bucket is a per-tenant token bucket."}) {
		t.Errorf("removed line = %+v", ls[2])
	}
	if ls[3] != (review.Line{New: 3, Kind: '+', Text: "// Bucket refills continuously."}) {
		t.Errorf("added line = %+v", ls[3])
	}
	if ls[1] != (review.Line{Old: 2, New: 2, Kind: ' '}) { // blank context line (" ")
		t.Errorf("blank context = %+v", ls[1])
	}
	// A new file: "-0,0" must not number anything on the old side.
	nf := review.Lines(forge.DiffFile{Diff: "@@ -0,0 +1,2 @@\n+a\n+b\n"})
	if len(nf) != 2 || nf[0] != (review.Line{New: 1, Kind: '+', Text: "a"}) || nf[1].New != 2 {
		t.Errorf("new file lines = %+v", nf)
	}
	// A "\ No newline" marker is not a line.
	if n := len(review.Lines(forge.DiffFile{Diff: "@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n"})); n != 2 {
		t.Errorf("no-newline marker counted: %d lines", n)
	}
}
