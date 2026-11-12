package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge"
)

func describeInput(current string) DescribeInput {
	return DescribeInput{
		MR: forge.MR{
			Ref: forge.Ref{Project: "grp/api", IID: 2, Kind: forge.KindMR}, Title: "Rename bucket", SourceBranch: "rename", TargetBranch: "main",
			HeadSHA: "abc123", Description: current,
		},
		Diff:         forge.Diff{HeadSHA: "abc123", Files: []forge.DiffFile{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,1 +1,2 @@\n x\n+y\n"}}},
		Commits:      []forge.Commit{{SHA: "1", Title: "Add the token bucket", Message: "Add the token bucket\n\nOne bucket per tenant."}, {SHA: "2", Title: "Expose Burst", Message: "Expose Burst"}},
		TemplateName: "Default",
		Template:     "## What\n\n## Why\n\n## Testing\n",
	}
}

func TestDescribePromptHoldsTemplateCommitsDiffAndTheLinkLinesOnly(t *testing.T) {
	j := DescribeJob("sandbox", describeInput("Old prose the model should not see.\n\nCloses grp/tracker#9\nSomething else"))
	for _, want := range []string{
		`Template "Default"`, "## Testing", "- Add the token bucket", "One bucket per tenant.", "- Expose Burst",
		"+y", "Head: abc123", "Closes grp/tracker#9",
	} {
		if !strings.Contains(j.Prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, j.Prompt)
		}
	}
	if strings.Contains(j.Prompt, "Old prose") || strings.Contains(j.Prompt, "Something else") {
		t.Errorf("the current description's prose must not be in the prompt (it would change the cache key after an apply):\n%s", j.Prompt)
	}
	if j.Feature != "describe" || j.Role != RoleDraft {
		t.Errorf("job = %+v", j)
	}
}

func TestDescribeIsCachedUntilTheHeadTheCommitsOrTheTemplateChange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, err := e.svc.Run(ctx, DescribeJob("sandbox", describeInput("Closes #1")))
	if err != nil || first.CacheHit {
		t.Fatalf("first: %+v %v", first, err)
	}
	// Applying the draft changes the MR's description; asking again is still a hit.
	second, err := e.svc.Run(ctx, DescribeJob("sandbox", describeInput("Closes #1\n\n## What\nrewritten")))
	if err != nil || !second.CacheHit || e.drv.calls != 1 {
		t.Fatalf("an applied draft must not make the next ask a miss: %+v %v calls=%d", second, err, e.drv.calls)
	}
	in := describeInput("Closes #1")
	in.Commits = append(in.Commits, forge.Commit{SHA: "3", Title: "Fix the burst"})
	if res, _ := e.svc.Run(ctx, DescribeJob("sandbox", in)); res.CacheHit || e.drv.calls != 2 {
		t.Fatalf("a new commit must be a miss (calls=%d)", e.drv.calls)
	}
}

func TestDescribeRefusedByACapSendsNothing(t *testing.T) {
	e := newEnv(t)
	e.conf.Budget.DailyUSD = 0.01
	e.log.Append(auditSpend(0.0099))
	_, err := e.svc.Run(context.Background(), DescribeJob("sandbox", describeInput("")))
	if _, ok := AsStop(err); !ok || e.drv.calls != 0 {
		t.Fatalf("want a budget stop without a call, got %v (calls %d)", err, e.drv.calls)
	}
}

func TestPrepareDescription(t *testing.T) {
	for _, c := range []struct {
		name, draft, current, want string
	}{
		{"plain", "## What\nx", "", "## What\nx"},
		{"a wrapping code fence goes", "```markdown\n## What\nx\n```", "", "## What\nx"},
		{"a fence inside stays", "## What\n```go\nx := 1\n```", "", "## What\n```go\nx := 1\n```"},
		{"quick actions are stripped", "## What\nx\n/assign_reviewer @bob\n/label ~bug", "", "## What\nx"},
		{"dropped links are put back", "## What\nx", "Closes grp/tracker#9\nParent: grp/tracker#3\nprose", "## What\nx\n\nCloses grp/tracker#9\nParent: grp/tracker#3"},
		{"kept links are not doubled", "## What\nx\nCloses grp/tracker#9", "Closes grp/tracker#9", "## What\nx\nCloses grp/tracker#9"},
	} {
		if got := PrepareDescription(c.draft, c.current); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
