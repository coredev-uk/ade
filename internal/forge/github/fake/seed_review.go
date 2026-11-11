package fake

import (
	"fmt"
	"maps"
	"strings"
	"time"
)

const (
	bucketGo = "package limit\n\n// Bucket is a per-tenant token bucket.\ntype Bucket struct {\n\tRate  int\n\tBurst int\n}\n"

	tokenBucketGo = "package limit\n\n// TokenBucket is a per-tenant token bucket.\n//\n// It refills Rate tokens per minute up to Burst.\n" +
		"type TokenBucket struct {\n\tRate  int\n\tBurst int\n}\n\nfunc (b TokenBucket) Capacity() int { return b.Burst }\n"

	// tokenBucketPatch is written out rather than generated: the rename keeps
	// the file's context lines, which diffCommits' whole-file patches do not.
	tokenBucketPatch = "@@ -1,7 +1,11 @@\n package limit\n \n" +
		"-// Bucket is a per-tenant token bucket.\n-type Bucket struct {\n" +
		"+// TokenBucket is a per-tenant token bucket.\n+//\n+// It refills Rate tokens per minute up to Burst.\n+type TokenBucket struct {\n" +
		" \tRate  int\n \tBurst int\n }\n+\n+func (b TokenBucket) Capacity() int { return b.Burst }"

	pagerTS = "export interface Page<T> {\n  items: T[];\n  nextCursor?: string;\n}\n"

	headerOld = "export function header(name: string): string {\n  return name.slice(0, 8);\n}\n"
	headerNew = "export function header(name: string): string {\n  return name;\n}\n"
)

type prSpec struct {
	title, body, author string
	head, base          string
	draft               bool
	reviewers           []string
	add                 map[string]string // files the head branch adds or changes
	remove              []string          // files the head branch deletes
	files               []*prFile         // explicit diff, when diffCommits is too crude
	created, updated    time.Time
	merged              time.Time // zero while open
}

// seedPR adds a branch with one commit and a PR for it.
func (st *state) seedPR(rp *repo, s prSpec) *item {
	base := rp.resolveRef(s.base)
	files := cloneFiles(base.Files)
	for _, p := range s.remove {
		delete(files, p)
	}
	maps.Copy(files, s.add)
	head := st.commit(rp, s.title, s.created, files, base.SHA)
	rp.Branches[s.head] = head.SHA

	it := st.addItem(rp, s.title, s.body, s.author, s.created)
	it.Updated = s.created.Add(time.Duration(it.Number) * time.Hour)
	if !s.updated.IsZero() {
		it.Updated = s.updated
	}
	p := &pull{
		Draft: s.draft, Head: s.head, Base: s.base, HeadSHA: head.SHA, BaseSHA: base.SHA,
		Reviewers: s.reviewers, Files: diffCommits(base, head),
	}
	if s.files != nil {
		p.Files = s.files
	}
	it.PR = p
	if !s.merged.IsZero() {
		merge := st.commit(rp, fmt.Sprintf("Merge pull request #%d from %s", it.Number, s.head), s.merged, files, base.SHA, head.SHA)
		p.Merged, p.MergedAt, p.MergeSHA = true, s.merged, merge.SHA
		it.State, it.Closed, it.Updated = "closed", s.merged, s.merged
		delete(rp.Branches, s.head)
		rp.Branches[s.base] = merge.SHA
	}
	return it
}

func (st *state) addComment(it *item, login, body string, at time.Time) *comment {
	c := &comment{ID: st.id(), User: login, Body: body, Created: at, Updated: at}
	it.Comments = append(it.Comments, c)
	it.Updated = at
	return c
}

func (st *state) addReview(p *pull, login, state, body string, at time.Time) *review {
	r := &review{ID: st.id(), User: login, Body: body, State: state, CommitID: p.HeadSHA}
	if state != statePending {
		r.Submitted = at
	}
	p.Reviews = append(p.Reviews, r)
	return r
}

// addThread opens a review thread on the right side of a file.
func (st *state) addThread(p *pull, path string, line int, first *reviewComment) *thread {
	t := &thread{ID: st.id(), Path: path, Line: line, Side: "RIGHT"}
	st.appendToThread(t, first)
	p.Threads = append(p.Threads, t)
	return t
}

func (st *state) appendToThread(t *thread, c *reviewComment) {
	c.ID = st.id()
	if len(t.Comments) > 0 {
		c.InReplyTo = t.Comments[0].ID
	}
	t.Comments = append(t.Comments, c)
}

// addRun stores a workflow run. Jobs are (id, name, conclusion) triples; an
// empty conclusion means the job is still running.
func (st *state) addRun(rp *repo, r *run, jobs ...jobSpec) {
	r.Attempt, r.SuiteID = 1, st.id()
	if r.Updated.IsZero() {
		r.Updated = r.Created
	}
	started := r.Created.Add(5 * time.Second)
	for _, j := range jobs {
		jb := &job{ID: j.id, Name: j.name, Status: "completed", Conclusion: j.conclusion, Started: started}
		jb.Completed = started.Add(90 * time.Second)
		jb.Log = jobLog(j.name, j.conclusion, started)
		r.Jobs = append(r.Jobs, jb)
		r.Updated = jb.Completed
	}
	rp.Runs = append(rp.Runs, r)
}

type jobSpec struct {
	id               int64
	name, conclusion string
}

func jobLog(name, conclusion string, at time.Time) string {
	ts := func(sec int) string {
		return at.Add(time.Duration(sec) * time.Second).Format("2006-01-02T15:04:05.0000000Z")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s ##[group]Run npm run %s\n", ts(1), name)
	fmt.Fprintf(&b, "%s > export-%s@0.1.0 %s\n", ts(2), name, name)
	if conclusion == "failure" {
		if name == "lint" {
			fmt.Fprintf(&b, "%s src/audit/pager.ts\n%s   3:1  error  Unexpected any  @typescript-eslint/no-explicit-any\n", ts(3), ts(3))
			fmt.Fprintf(&b, "%s   9:7  error  'cursor' is assigned a value but never used  no-unused-vars\n", ts(3))
			fmt.Fprintf(&b, "%s eslint: 2 errors\n", ts(4))
		}
		fmt.Fprintf(&b, "%s ##[error]Process completed with exit code 1.\n", ts(5))
		return b.String()
	}
	fmt.Fprintf(&b, "%s ok\n%s ##[endgroup]\n", ts(3), ts(4))
	return b.String()
}

func (st *state) seedAPI(rp *repo) {
	ago := func(d time.Duration) time.Time { return st.clock.Add(-d) }
	main := rp.Commits[rp.Branches[rp.Default]]
	main.Files["Dockerfile"] = "FROM debian:12\n"

	st.seedIssue(rp, issueSpec{
		title: "Rate-limit the export endpoint", body: "Parent: ade-demo/tracker#1",
		labels: []string{"type:story"}, milestone: 2, assigned: true, created: ago(9 * day),
	})
	pr := st.seedPR(rp, prSpec{
		title: "Rate-limit the export endpoint", author: viewer,
		body: "Closes #1\n\nPart of ade-demo/tracker#1.",
		head: "1-rate-limit-the-export-endpoint", base: "main", reviewers: []string{"teammate"},
		add: map[string]string{"limit/bucket.go": bucketGo}, created: ago(7 * day), updated: ago(2 * day),
	})
	st.seedIssue(rp, issueSpec{
		title: "Upgrade export worker dependencies", body: "Parent: ade-demo/tracker#10",
		labels: []string{"type:story"}, milestone: 2, assigned: true, created: ago(9 * day), closed: ago(5 * day),
	})
	st.seedPR(rp, prSpec{
		title: "Rename bucket to token bucket", author: viewer, body: "Renames Bucket to TokenBucket.",
		head: "rename-bucket-to-token-bucket", base: "1-rate-limit-the-export-endpoint",
		add: map[string]string{"limit/token_bucket.go": tokenBucketGo}, remove: []string{"limit/bucket.go"},
		files: []*prFile{{
			SHA: blobSHA(tokenBucketGo), Filename: "limit/token_bucket.go", Previous: "limit/bucket.go",
			Status: "renamed", Patch: tokenBucketPatch, Additions: 6, Deletions: 2,
		}},
		created: ago(3 * day),
	})
	st.seedPR(rp, prSpec{
		title: "Add retry budget to webhook sender", author: "teammate", body: "Adds a retry budget to the webhook sender.",
		head: "feature/retry-budget", base: "main", reviewers: []string{viewer},
		add:     map[string]string{"webhook/budget.go": "package webhook\n\n// Budget caps retries per tenant.\ntype Budget struct{ Max int }\n"},
		created: ago(2 * day),
	})
	st.seedPR(rp, prSpec{
		title: "Bump the worker base image", author: viewer, body: "Moves the worker image to debian 13.",
		head: "bump-worker-base-image", base: "main",
		add: map[string]string{"Dockerfile": "FROM debian:13\n"}, created: ago(8 * day), merged: ago(6 * day),
	})

	p := pr.PR
	st.addComment(pr, "teammate", "Thanks for picking this up - the burst number needs a decision before we merge.", ago(3*day))
	rv1 := st.addReview(p, "teammate", stateCommented, "A few comments on the bucket.", ago(3*day))
	reply := st.addReview(p, viewer, stateCommented, "", ago(2*day))
	rc := func(login, body string, rv *review, at time.Time) *reviewComment {
		return &reviewComment{User: login, Body: body, Review: rv, Created: at, Updated: at}
	}
	t1 := st.addThread(p, "limit/bucket.go", 6, rc("teammate", "Is a burst of 50 too high for small tenants? Support suggested 20.", rv1, ago(3*day)))
	st.appendToThread(t1, rc(viewer, "Good point, I'll check with support before changing it.", reply, ago(2*day)))
	st.addThread(p, "limit/bucket.go", 3, rc("teammate", "Please add a test for the 429 path, including the Retry-After header.", rv1, ago(3*day)))
	t3 := st.addThread(p, "limit/bucket.go", 5, rc("teammate", "Nit: rename Bucket.Rate to PerMinute for clarity.", rv1, ago(3*day)))
	t3.Resolved = true
	// Another user's unsubmitted review: the viewer must never see it.
	hidden := st.addReview(p, "reviewer-bot", statePending, "", ago(1*day))
	st.addThread(p, "limit/bucket.go", 2, rc("reviewer-bot", "Draft: blank line after the package clause?", hidden, ago(1*day)))

	st.addRun(rp, &run{
		ID: 9001, Name: "ci", HeadBranch: p.Head, HeadSHA: p.HeadSHA, Status: "completed", Conclusion: "success",
		Number: 1, Created: ago(7 * day),
	}, jobSpec{9101, "test", "success"}, jobSpec{9102, "lint", "success"})
}

func (st *state) seedWeb(rp *repo) {
	ago := func(d time.Duration) time.Time { return st.clock.Add(-d) }
	main := rp.Commits[rp.Branches[rp.Default]]
	main.Files["src/export/preview.ts"] = headerOld

	st.seedIssue(rp, issueSpec{
		title: "Paginate the audit log view", body: "Parent: ade-demo/tracker#2",
		labels: []string{"type:bug"}, milestone: 2, assigned: true, created: ago(9 * day),
	})
	pr := st.seedPR(rp, prSpec{
		title: "Paginate the audit log view", author: viewer,
		body: "Closes #1\n\nPart of ade-demo/tracker#2.",
		head: "1-paginate-the-audit-log-view", base: "main", draft: true,
		add: map[string]string{"src/audit/pager.ts": pagerTS}, created: ago(6 * day), updated: ago(1 * day),
	})
	st.seedIssue(rp, issueSpec{
		title: "Fix truncated column headers in export preview", body: "Parent: ade-demo/tracker#9",
		labels: []string{"type:bug"}, milestone: 1, assigned: true, created: ago(20 * day), closed: ago(14 * day),
	})
	st.seedPR(rp, prSpec{
		title: "Fix truncated column headers in export preview", author: viewer, body: "Closes #3",
		head: "3-fix-truncated-column-headers-in-export-preview", base: "main",
		add: map[string]string{"src/export/preview.ts": headerNew}, created: ago(16 * day), merged: ago(14 * day),
	})
	p := pr.PR
	st.addRun(rp, &run{
		ID: 9002, Name: "ci", HeadBranch: p.Head, HeadSHA: p.HeadSHA, Status: "completed", Conclusion: "failure",
		Number: 1, Created: ago(1 * day),
	}, jobSpec{9201, "test", "success"}, jobSpec{9202, "lint", "failure"}, jobSpec{9203, "build", "success"})
}
