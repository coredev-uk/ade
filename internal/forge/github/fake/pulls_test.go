package fake_test

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// checkPatch verifies a unified-diff patch is internally consistent: each
// hunk header's line counts match the hunk body.
func checkPatch(t *testing.T, name, patch string) {
	t.Helper()
	lines := strings.Split(patch, "\n")
	if hunkHeader.FindStringSubmatch(lines[0]) == nil {
		t.Errorf("%s: patch does not start with a hunk header: %q", name, lines[0])
		return
	}
	var oldN, newN, wantOld, wantNew int
	flush := func() {
		if oldN != wantOld || newN != wantNew {
			t.Errorf("%s: hunk counts -%d +%d, header says -%d +%d", name, oldN, newN, wantOld, wantNew)
		}
	}
	for i, ln := range lines {
		if m := hunkHeader.FindStringSubmatch(ln); m != nil {
			if i > 0 {
				flush()
			}
			oldN, newN = 0, 0
			wantOld = countOr(m[2], 1)
			wantNew = countOr(m[4], 1)
			continue
		}
		switch ln[:min(1, len(ln))] {
		case "+":
			newN++
		case "-":
			oldN++
		default:
			oldN++
			newN++
		}
	}
	flush()
}

func countOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, _ := strconv.Atoi(s)
	return n
}

func TestListPulls(t *testing.T) {
	s := newServer(t)
	for _, tc := range []struct {
		name, path string
		want       []int // in response order
	}{
		{"open, newest first", api + "/pulls", []int{5, 4, 2}},
		{"all", api + "/pulls?state=all", []int{5, 4, 2, 6}},
		{"closed includes merged", api + "/pulls?state=closed", []int{6}},
		{"ascending", api + "/pulls?direction=asc", []int{2, 4, 5}},
		{"by head", api + "/pulls?head=ade-demo:feature/retry-budget", []int{5}},
		{"head with escaped slash", api + "/pulls?head=" + "ade-demo%3Afeature%2Fretry-budget", []int{5}},
		{"head of another owner", api + "/pulls?head=elsewhere:feature/retry-budget", nil},
		{"by base", api + "/pulls?base=1-rate-limit-the-export-endpoint", []int{4}},
		{"web all", web + "/pulls?state=all", []int{2, 4}},
		{"tracker has none", tracker + "/pulls?state=all", nil},
		{"sorted by updated", api + "/pulls?sort=updated&direction=desc", []int{5, 4, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := numbers(t, get(t, s, tc.path).arr(t))
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s = %v, want %v", tc.path, got, tc.want)
			}
		})
	}

	// Lists carry no merged/mergeable keys, as on GitHub; a single PR does.
	for _, p := range get(t, s, api+"/pulls?state=all").arr(t) {
		for _, k := range []string{"merged", "mergeable", "mergeable_state", "additions"} {
			if has(p, k) {
				t.Errorf("list item #%v has %q", p.(map[string]any)["number"], k)
			}
		}
		if _, ok := p.(map[string]any)["draft"].(bool); !ok {
			t.Errorf("list item lacks draft: %v", p)
		}
	}
	expect(t, get(t, s, api+"/pulls/1"), 404) // an issue, not a PR
	expect(t, get(t, s, api+"/pulls/99"), 404)
}

func TestPullShape(t *testing.T) {
	s := newServer(t)
	p := get(t, s, api+"/pulls/2").obj(t)
	if str(t, p, "title") != "Rate-limit the export endpoint" || str(t, p, "state") != "open" || p["draft"] != false || p["merged"] != false || p["mergeable"] != true || str(t, p, "mergeable_state") != "clean" {
		t.Errorf("api#2 = %v", p)
	}
	if str(t, p, "body") != "Closes #1\n\nPart of ade-demo/tracker#1." || str(t, p, "user", "login") != "demo-user" {
		t.Errorf("body/user = %v / %v", p["body"], p["user"])
	}
	if str(t, p, "head", "ref") != "1-rate-limit-the-export-endpoint" || str(t, p, "base", "ref") != "main" || len(str(t, p, "head", "sha")) != 40 || str(t, p, "head", "repo", "full_name") != "ade-demo/api" || str(t, p, "base", "sha") == "" {
		t.Errorf("head/base = %v / %v", p["head"], p["base"])
	}
	if got := pluck[string](t, at(t, p, "requested_reviewers").([]any), "login"); !slices.Equal(got, []string{"teammate"}) {
		t.Errorf("requested_reviewers = %v", got)
	}
	if p["merged_at"] != nil || p["auto_merge"] != nil {
		t.Errorf("open PR has merged_at/auto_merge: %v %v", p["merged_at"], p["auto_merge"])
	}
	// The base of api#4 is the head branch of api#2.
	four := get(t, s, api+"/pulls/4").obj(t)
	if str(t, four, "base", "ref") != "1-rate-limit-the-export-endpoint" || str(t, four, "base", "sha") != str(t, p, "head", "sha") {
		t.Errorf("api#4 base = %v, want api#2's head", four["base"])
	}

	five := get(t, s, api+"/pulls/5").obj(t)
	if str(t, five, "user", "login") != "teammate" || str(t, five, "requested_reviewers", 0, "login") != "demo-user" || five["mergeable"] != true {
		t.Errorf("api#5 = %v", five)
	}

	draft := get(t, s, web+"/pulls/2").obj(t)
	if draft["draft"] != true || str(t, draft, "mergeable_state") != "draft" || str(t, draft, "head", "ref") != "1-paginate-the-audit-log-view" {
		t.Errorf("web#2 = %v", draft)
	}

	merged := get(t, s, web+"/pulls/4").obj(t)
	if merged["merged"] != true || str(t, merged, "state") != "closed" || str(t, merged, "merged_at") != "2026-10-28T12:00:00Z" || str(t, merged, "body") != "Closes #3" || merged["mergeable"] != false {
		t.Errorf("web#4 = %v", merged)
	}
	if str(t, merged, "head", "ref") != "3-fix-truncated-column-headers-in-export-preview" {
		t.Errorf("web#4 head = %v", merged["head"])
	}
	// The merged PR's head branch is gone, its commits are not.
	if slices.Contains(s.Branches("ade-demo/web"), "3-fix-truncated-column-headers-in-export-preview") {
		t.Error("merged head branch still exists")
	}
	expect(t, get(t, s, web+"/commits/"+str(t, merged, "head", "sha")), 200)
}

func TestPullFiles(t *testing.T) {
	s := newServer(t)

	files := get(t, s, api+"/pulls/2/files").arr(t)
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	f := files[0]
	if str(t, f, "filename") != "limit/bucket.go" || str(t, f, "status") != "added" || num(t, f, "additions") != 7 || num(t, f, "deletions") != 0 || num(t, f, "changes") != 7 || str(t, f, "sha") == "" {
		t.Errorf("api#2 file = %v", f)
	}
	if !strings.HasPrefix(str(t, f, "patch"), "@@ -0,0 +1,7 @@\n+package limit\n") || has(f, "previous_filename") {
		t.Errorf("patch = %q", f.(map[string]any)["patch"])
	}

	ren := get(t, s, api+"/pulls/4/files").arr(t)
	if len(ren) != 1 || str(t, ren[0], "status") != "renamed" || str(t, ren[0], "filename") != "limit/token_bucket.go" || str(t, ren[0], "previous_filename") != "limit/bucket.go" || !strings.HasPrefix(str(t, ren[0], "patch"), "@@ -1,7 +1,11 @@\n") {
		t.Errorf("api#4 files = %v", ren)
	}

	// Every seeded patch is a well-formed diff whose counts match its fields.
	for _, repo := range []string{api, web} {
		for _, p := range get(t, s, repo+"/pulls?state=all").arr(t) {
			n := num(t, p, "number")
			for _, f := range get(t, s, fmt.Sprintf("%s/pulls/%d/files", repo, n)).arr(t) {
				name := fmt.Sprintf("%s#%d %s", repo, n, str(t, f, "filename"))
				checkPatch(t, name, str(t, f, "patch"))
				var add, del int
				for _, ln := range strings.Split(str(t, f, "patch"), "\n") {
					switch {
					case strings.HasPrefix(ln, "+"):
						add++
					case strings.HasPrefix(ln, "-"):
						del++
					}
				}
				if add != num(t, f, "additions") || del != num(t, f, "deletions") {
					t.Errorf("%s: additions/deletions %v/%v, patch has %d/%d", name, f.(map[string]any)["additions"], f.(map[string]any)["deletions"], add, del)
				}
			}
		}
	}
	if got := get(t, s, web+"/pulls/2/files").arr(t); len(got) != 1 || str(t, got[0], "filename") != "src/audit/pager.ts" {
		t.Errorf("web#2 files = %v", got)
	}
	if got := get(t, s, api+"/pulls/5/files").arr(t); len(got) != 1 {
		t.Errorf("api#5 files = %v", got)
	}
	expect(t, get(t, s, api+"/pulls/1/files"), 404)
}

func TestCreatePull(t *testing.T) {
	s := newServer(t)
	main := get(t, s, api+"/commits/main").obj(t)
	mainSHA := str(t, main, "sha")
	if len(mainSHA) != 40 {
		t.Fatalf("main = %v", main)
	}
	if str(t, get(t, s, api+"/commits/"+mainSHA).obj(t), "sha") != mainSHA {
		t.Error("commit by sha differs from commit by branch")
	}
	if r := get(t, s, api+"/commits/nonexistent"); r.code != 422 && r.code != 404 {
		t.Errorf("unknown commit = %d", r.code)
	}

	// Branch off main: a PR with no new commits is refused as on GitHub.
	ref := expect(t, call(t, s, "POST", api+"/git/refs", map[string]any{"ref": "refs/heads/feature/x", "sha": mainSHA}), 201).obj(t)
	if str(t, ref, "ref") != "refs/heads/feature/x" || str(t, ref, "object", "sha") != mainSHA || str(t, ref, "object", "type") != "commit" {
		t.Errorf("created ref = %v", ref)
	}
	body := map[string]any{"title": "Feature X", "head": "feature/x", "base": "main", "body": "Closes #1"}
	e := expect(t, call(t, s, "POST", api+"/pulls", body), 422).obj(t)
	if str(t, e, "message") != "Validation Failed" || str(t, e, "errors", 0, "message") != "No commits between main and feature/x" || str(t, e, "errors", 0, "code") != "custom" || str(t, e, "errors", 0, "resource") != "PullRequest" {
		t.Errorf("no-commit error = %v", e)
	}

	// An empty commit moves the branch; the PR now opens with no files.
	tree := str(t, get(t, s, api+"/git/commits/"+mainSHA).obj(t), "tree", "sha")
	if tree == "" {
		t.Fatal("commit has no tree")
	}
	c := expect(t, call(t, s, "POST", api+"/git/commits", map[string]any{"message": "Start work", "tree": tree, "parents": []string{mainSHA}}), 201).obj(t)
	newSHA := str(t, c, "sha")
	if len(newSHA) != 40 || newSHA == mainSHA || str(t, c, "tree", "sha") != tree || str(t, c, "parents", 0, "sha") != mainSHA {
		t.Fatalf("created commit = %v", c)
	}
	expect(t, call(t, s, "POST", api+"/git/commits", map[string]any{"message": "x", "tree": "deadbeef", "parents": []string{mainSHA}}), 422)
	expect(t, call(t, s, "POST", api+"/git/commits", map[string]any{"message": "x", "tree": tree, "parents": []string{"deadbeef"}}), 422)
	got := get(t, s, api+"/git/commits/"+newSHA).obj(t)
	if str(t, got, "message") != "Start work" || str(t, got, "tree", "sha") != tree {
		t.Errorf("git commit = %v", got)
	}
	expect(t, get(t, s, api+"/git/commits/"+strings.Repeat("0", 40)), 404)
	// Not yet reachable from the branch.
	if str(t, get(t, s, api+"/git/ref/heads/feature/x").obj(t), "object", "sha") != mainSHA {
		t.Error("branch moved before PATCH")
	}
	moved := expect(t, call(t, s, "PATCH", api+"/git/refs/heads/feature/x", map[string]any{"sha": newSHA}), 200).obj(t)
	if str(t, moved, "object", "sha") != newSHA {
		t.Errorf("moved ref = %v", moved)
	}

	body["head"] = "ade-demo:feature/x"
	body["draft"] = true
	pr := expect(t, call(t, s, "POST", api+"/pulls", body), 201).obj(t)
	if num(t, pr, "number") != 7 || pr["draft"] != true || str(t, pr, "head", "sha") != newSHA || str(t, pr, "base", "ref") != "main" || str(t, pr, "user", "login") != "demo-user" || str(t, pr, "state") != "open" || str(t, pr, "mergeable_state") != "draft" {
		t.Errorf("created PR = %v", pr)
	}
	if files := get(t, s, api+"/pulls/7/files").arr(t); len(files) != 0 {
		t.Errorf("files of an empty-commit PR = %v", files)
	}
	if got := numbers(t, get(t, s, api+"/pulls?head=ade-demo:feature/x").arr(t)); !slices.Equal(got, []int{7}) {
		t.Errorf("head filter = %v", got)
	}
	if d, err := s.IsDraft(prRef("ade-demo/api", 7)); err != nil || !d {
		t.Errorf("IsDraft = %v, %v", d, err)
	}

	dup := expect(t, call(t, s, "POST", api+"/pulls", body), 422).obj(t)
	if str(t, dup, "message") != "Validation Failed" || str(t, dup, "errors", 0, "message") != "A pull request already exists for ade-demo:feature/x." || str(t, dup, "errors", 0, "code") != "custom" {
		t.Errorf("duplicate = %v", dup)
	}
	bad := expect(t, call(t, s, "POST", api+"/pulls", map[string]any{"title": "T", "head": "no-such", "base": "main"}), 422).obj(t)
	if str(t, bad, "errors", 0, "field") != "head" || str(t, bad, "errors", 0, "code") != "invalid" {
		t.Errorf("missing head = %v", bad)
	}
	badBase := expect(t, call(t, s, "POST", api+"/pulls", map[string]any{"title": "T", "head": "feature/x", "base": "no-such"}), 422).obj(t)
	if str(t, badBase, "errors", 0, "field") != "base" {
		t.Errorf("missing base = %v", badBase)
	}
	expect(t, call(t, s, "POST", api+"/pulls", map[string]any{"head": "feature/x", "base": "main"}), 422)

	open := call(t, s, "POST", api+"/pulls", map[string]any{"title": "From existing", "head": "feature/retry-budget", "base": "main"})
	if open.code != 422 || !strings.Contains(string(open.body), "already exists") {
		t.Errorf("PR over an existing open head = %d %s", open.code, open.body)
	}
}

func TestMerge(t *testing.T) {
	s := newServer(t)
	head := str(t, get(t, s, api+"/pulls/5").obj(t), "head", "sha")
	put := func(path string, body map[string]any) resp { return call(t, s, "PUT", path+"/merge", body) }

	stale := expect(t, put(api+"/pulls/5", map[string]any{"sha": strings.Repeat("a", 40)}), 409).obj(t)
	if str(t, stale, "message") != "Head branch was modified. Review and try the merge again." {
		t.Errorf("stale sha = %v", stale)
	}
	expect(t, put(api+"/pulls/5", map[string]any{"sha": head, "merge_method": "fast-forward"}), 422)
	if merged, _, _, _ := s.MergeState(prRef("ade-demo/api", 5)); merged {
		t.Fatal("rejected merges changed state")
	}

	draft := expect(t, put(web+"/pulls/2", map[string]any{"merge_method": "merge"}), 405).obj(t)
	if str(t, draft, "message") != "Pull Request is not mergeable" {
		t.Errorf("draft merge = %v", draft)
	}
	expect(t, put(api+"/pulls/6", map[string]any{}), 405) // already merged
	expect(t, put(api+"/pulls/1", map[string]any{}), 404) // an issue
	expect(t, put(api+"/pulls/99", map[string]any{}), 404)

	r := expect(t, put(api+"/pulls/5", map[string]any{"sha": head, "merge_method": "squash", "commit_title": "Squash it", "commit_message": "details"}), 200).obj(t)
	if r["merged"] != true || len(str(t, r, "sha")) != 40 || str(t, r, "message") == "" {
		t.Fatalf("merge = %v", r)
	}
	pr := get(t, s, api+"/pulls/5").obj(t)
	if pr["merged"] != true || str(t, pr, "state") != "closed" || pr["merged_at"] == nil || pr["closed_at"] == nil || str(t, pr, "merge_commit_sha") != str(t, r, "sha") {
		t.Errorf("after merge = %v", pr)
	}
	merged, auto, params, err := s.MergeState(prRef("ade-demo/api", 5))
	if err != nil || !merged || auto {
		t.Errorf("MergeState = %v %v %v", merged, auto, err)
	}
	want := map[string]any{"sha": head, "merge_method": "squash", "commit_title": "Squash it", "commit_message": "details"}
	if fmt.Sprint(params) != fmt.Sprint(want) {
		t.Errorf("merge params = %v, want %v", params, want)
	}
	expect(t, put(api+"/pulls/5", map[string]any{}), 405)

	// main advanced to the merge commit and now holds the PR's file.
	ref := get(t, s, api+"/git/matching-refs/heads/main").arr(t)
	if len(ref) != 1 || str(t, ref[0], "object", "sha") != str(t, r, "sha") {
		t.Errorf("main = %v, want the merge commit", ref)
	}
	raw := call(t, s, "GET", api+"/contents/webhook/budget.go?ref=main", nil, "Accept", "application/vnd.github.raw+json")
	if raw.code != 200 || !strings.Contains(string(raw.body), "type Budget struct") {
		t.Errorf("merged file = %d %s", raw.code, raw.body)
	}
	evs := pluck[string](t, get(t, s, api+"/issues/5/events").arr(t), "event")
	if !slices.Contains(evs, "merged") || !slices.Contains(evs, "closed") {
		t.Errorf("events = %v", evs)
	}
	// Closed PRs leave the open list.
	if got := numbers(t, get(t, s, api+"/pulls").arr(t)); !slices.Equal(got, []int{4, 2}) {
		t.Errorf("open PRs = %v", got)
	}
}

func TestChecks(t *testing.T) {
	s := newServer(t)
	headOf := func(repo string, n int) string {
		return str(t, get(t, s, fmt.Sprintf("%s/pulls/%d", repo, n)).obj(t), "head", "sha")
	}
	apiHead, webHead := headOf(api, 2), headOf(web, 2)

	cr := get(t, s, api+"/commits/"+apiHead+"/check-runs").obj(t)
	names := pluck[string](t, at(t, cr, "check_runs").([]any), "name")
	slices.Sort(names)
	if num(t, cr, "total_count") != 2 || !slices.Equal(names, []string{"lint", "test"}) {
		t.Fatalf("api check-runs = %v", cr)
	}
	for _, c := range at(t, cr, "check_runs").([]any) {
		if str(t, c, "status") != "completed" || str(t, c, "conclusion") != "success" || str(t, c, "html_url") == "" || str(t, c, "started_at") == "" || str(t, c, "completed_at") == "" || str(t, c, "app", "slug") != "github-actions" || num(t, c, "check_suite", "id") == 0 {
			t.Errorf("check run = %v", c)
		}
	}

	// A branch name works for the commit ref, like a sha.
	byBranch := get(t, s, web+"/commits/1-paginate-the-audit-log-view/check-runs").obj(t)
	if num(t, byBranch, "total_count") != 3 {
		t.Errorf("web check-runs = %v", byBranch)
	}
	var lint map[string]any
	for _, c := range at(t, byBranch, "check_runs").([]any) {
		if str(t, c, "name") == "lint" {
			lint = c.(map[string]any)
		}
	}
	if lint["conclusion"] != "failure" {
		t.Errorf("lint check = %v", lint)
	}
	// check_run id == job id.
	jobs := get(t, s, web+"/actions/runs/9002/jobs").obj(t)
	if num(t, jobs, "total_count") != 3 {
		t.Fatalf("jobs = %v", jobs)
	}
	var jobIDs, checkIDs []int
	for _, j := range at(t, jobs, "jobs").([]any) {
		jobIDs = append(jobIDs, num(t, j, "id"))
		if num(t, j, "run_id") != 9002 || str(t, j, "workflow_name") != "ci" || str(t, j, "html_url") == "" {
			t.Errorf("job = %v", j)
		}
	}
	for _, c := range at(t, byBranch, "check_runs").([]any) {
		checkIDs = append(checkIDs, num(t, c, "id"))
	}
	slices.Sort(jobIDs)
	slices.Sort(checkIDs)
	if !slices.Equal(jobIDs, checkIDs) || jobIDs[0] != 9201 {
		t.Errorf("job ids %v != check-run ids %v", jobIDs, checkIDs)
	}
	if got := get(t, s, api+"/commits/"+apiHead+"/check-runs?check_name=lint").obj(t); num(t, got, "total_count") != 1 {
		t.Errorf("check_name filter = %v", got)
	}
	if r := get(t, s, api+"/commits/"+headOf(api, 4)+"/check-runs").obj(t); num(t, r, "total_count") != 0 || len(at(t, r, "check_runs").([]any)) != 0 {
		t.Errorf("PR without checks = %v", r)
	}
	if r := get(t, s, api+"/commits/zzz/check-runs"); r.code != 422 && r.code != 404 {
		t.Errorf("unknown commit = %d", r.code)
	}

	// No commit statuses exist: GitHub reports pending with total_count 0.
	st := get(t, s, web+"/commits/"+webHead+"/status").obj(t)
	if str(t, st, "state") != "pending" || num(t, st, "total_count") != 0 || len(at(t, st, "statuses").([]any)) != 0 {
		t.Errorf("status = %v", st)
	}

	runs := get(t, s, web+"/actions/runs?head_sha="+webHead).obj(t)
	if num(t, runs, "total_count") != 1 || num(t, runs, "workflow_runs", 0, "id") != 9002 || str(t, runs, "workflow_runs", 0, "conclusion") != "failure" || str(t, runs, "workflow_runs", 0, "head_sha") != webHead || str(t, runs, "workflow_runs", 0, "name") != "ci" {
		t.Errorf("runs = %v", runs)
	}
	if r := get(t, s, web+"/actions/runs?head_sha="+apiHead).obj(t); num(t, r, "total_count") != 0 {
		t.Errorf("runs for a foreign sha = %v", r)
	}
	if r := get(t, s, api+"/actions/runs").obj(t); num(t, r, "total_count") != 1 || num(t, r, "workflow_runs", 0, "id") != 9001 {
		t.Errorf("api runs = %v", r)
	}
	expect(t, get(t, s, web+"/actions/runs/1/jobs"), 404)

	logs := get(t, s, web+"/actions/jobs/9202/logs")
	if logs.code != 200 || !strings.HasPrefix(logs.h.Get("Content-Type"), "text/plain") || !strings.Contains(string(logs.body), "eslint: 2 errors") {
		t.Errorf("lint log = %d %q", logs.code, logs.body)
	}
	if ok := get(t, s, web+"/actions/jobs/9201/logs"); ok.code != 200 || strings.Contains(string(ok.body), "eslint") {
		t.Errorf("test log = %d %q", ok.code, ok.body)
	}
	expect(t, get(t, s, web+"/actions/jobs/1/logs"), 404)

	t.Run("rerun failed jobs", func(t *testing.T) {
		if s.Reruns("ade-demo/web", 9002) != 0 {
			t.Fatal("rerun count starts non-zero")
		}
		expect(t, call(t, s, "POST", api+"/actions/runs/9001/rerun-failed-jobs", nil), 403) // success: nothing failed
		expect(t, call(t, s, "POST", web+"/actions/runs/1/rerun-failed-jobs", nil), 404)
		r := expect(t, call(t, s, "POST", web+"/actions/runs/9002/rerun-failed-jobs", nil), 201)
		if len(r.obj(t)) != 0 {
			t.Errorf("body = %s, want {}", r.body)
		}
		if s.Reruns("ade-demo/web", 9002) != 1 {
			t.Errorf("Reruns = %d", s.Reruns("ade-demo/web", 9002))
		}
		state := map[string][2]any{}
		for _, j := range at(t, get(t, s, web+"/actions/runs/9002/jobs").obj(t), "jobs").([]any) {
			state[str(t, j, "name")] = [2]any{j.(map[string]any)["status"], j.(map[string]any)["conclusion"]}
		}
		if state["lint"] != [2]any{"queued", nil} || state["test"] != [2]any{"completed", "success"} || state["build"] != [2]any{"completed", "success"} {
			t.Errorf("job states = %v", state)
		}
		run := get(t, s, web+"/actions/runs?head_sha="+webHead).obj(t)
		if str(t, run, "workflow_runs", 0, "status") != "in_progress" || at(t, run, "workflow_runs", 0, "conclusion") != nil {
			t.Errorf("run = %v", run)
		}
		// The check run mirrors the queued job.
		cr := get(t, s, web+"/commits/"+webHead+"/check-runs?status=queued").obj(t)
		if num(t, cr, "total_count") != 1 || str(t, cr, "check_runs", 0, "name") != "lint" || at(t, cr, "check_runs", 0, "conclusion") != nil || at(t, cr, "check_runs", 0, "completed_at") != nil {
			t.Errorf("queued check runs = %v", cr)
		}
		// Only completed runs can be retried.
		e := expect(t, call(t, s, "POST", web+"/actions/runs/9002/rerun-failed-jobs", nil), 403).obj(t)
		if str(t, e, "message") != "This workflow run cannot be retried" || s.Reruns("ade-demo/web", 9002) != 1 {
			t.Errorf("second rerun = %v (count %d)", e, s.Reruns("ade-demo/web", 9002))
		}
	})
}

func TestGitData(t *testing.T) {
	s := newServer(t)

	refs := get(t, s, api+"/git/matching-refs/heads/").arr(t)
	want := []string{"refs/heads/1-rate-limit-the-export-endpoint", "refs/heads/feature/retry-budget", "refs/heads/main", "refs/heads/rename-bucket-to-token-bucket"}
	if got := pluck[string](t, refs, "ref"); !slices.Equal(got, want) {
		t.Errorf("api refs = %v, want %v", got, want)
	}
	if got := s.Branches("ade-demo/api"); !slices.Equal(got, []string{"1-rate-limit-the-export-endpoint", "feature/retry-budget", "main", "rename-bucket-to-token-bucket"}) {
		t.Errorf("Branches = %v", got)
	}
	if got := pluck[string](t, get(t, s, api+"/git/matching-refs/heads/feature/").arr(t), "ref"); !slices.Equal(got, []string{"refs/heads/feature/retry-budget"}) {
		t.Errorf("prefix feature/ = %v", got)
	}
	if got := get(t, s, api+"/git/matching-refs/heads/zzz").arr(t); len(got) != 0 {
		t.Errorf("no match = %v", got)
	}
	if got := s.Branches("ade-demo/web"); !slices.Equal(got, []string{"1-paginate-the-audit-log-view", "main"}) {
		t.Errorf("web Branches = %v", got)
	}

	mainSHA := str(t, get(t, s, api+"/commits/main").obj(t), "sha")
	for _, tc := range []struct {
		name string
		body map[string]any
		want string // message
		code int
	}{
		{"duplicate", map[string]any{"ref": "refs/heads/main", "sha": mainSHA}, "Reference already exists", 422},
		{"unknown sha", map[string]any{"ref": "refs/heads/y", "sha": strings.Repeat("0", 40)}, "Object does not exist", 422},
		{"created", map[string]any{"ref": "refs/heads/1-issue/sub", "sha": mainSHA}, "", 201},
	} {
		r := call(t, s, "POST", api+"/git/refs", tc.body)
		if r.code != tc.code || (tc.want != "" && str(t, r.obj(t), "message") != tc.want) {
			t.Errorf("%s = %d %s", tc.name, r.code, r.body)
		}
	}
	if !slices.Contains(s.Branches("ade-demo/api"), "1-issue/sub") {
		t.Error("created branch missing from Branches")
	}
	expect(t, call(t, s, "DELETE", api+"/git/refs/heads/1-issue/sub", nil), 204)
	if slices.Contains(s.Branches("ade-demo/api"), "1-issue/sub") {
		t.Error("deleted branch still listed")
	}
	gone := expect(t, call(t, s, "DELETE", api+"/git/refs/heads/1-issue/sub", nil), 422).obj(t)
	if str(t, gone, "message") != "Reference does not exist" {
		t.Errorf("second delete = %v", gone)
	}
	expect(t, get(t, s, api+"/git/ref/heads/1-issue/sub"), 404)

	// Branch updates must fast-forward unless forced.
	e := expect(t, call(t, s, "PATCH", api+"/git/refs/heads/feature/retry-budget", map[string]any{"sha": mainSHA}), 422).obj(t)
	if str(t, e, "message") != "Update is not a fast forward" {
		t.Errorf("non-ff = %v", e)
	}
	expect(t, call(t, s, "PATCH", api+"/git/refs/heads/feature/retry-budget", map[string]any{"sha": mainSHA, "force": true}), 200)
	expect(t, call(t, s, "PATCH", api+"/git/refs/heads/nope", map[string]any{"sha": mainSHA}), 422)
	expect(t, call(t, s, "PATCH", api+"/git/refs/heads/main", map[string]any{"sha": strings.Repeat("0", 40)}), 422)
}

func TestContents(t *testing.T) {
	s := newServer(t)

	f := get(t, s, api+"/contents/.github/pull_request_template.md").obj(t)
	if str(t, f, "type") != "file" || str(t, f, "encoding") != "base64" || str(t, f, "name") != "pull_request_template.md" || str(t, f, "path") != ".github/pull_request_template.md" || str(t, f, "sha") == "" {
		t.Fatalf("file = %v", f)
	}
	dec, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(str(t, f, "content"), "\n", ""))
	if err != nil || !strings.Contains(string(dec), "Checklist") {
		t.Errorf("decoded content = %q, %v", dec, err)
	}
	raw := call(t, s, "GET", api+"/contents/.github/pull_request_template.md", nil, "Accept", "application/vnd.github.raw+json")
	if raw.code != 200 || string(raw.body) != string(dec) || !strings.Contains(raw.h.Get("Content-Type"), "raw") {
		t.Errorf("raw = %d %q (%s)", raw.code, raw.body, raw.h.Get("Content-Type"))
	}
	for _, repo := range []string{tracker, web} {
		expect(t, get(t, s, repo+"/contents/.github/pull_request_template.md"), 200)
	}

	dir := get(t, s, api+"/contents/.github").arr(t)
	if len(dir) != 1 || str(t, dir[0], "type") != "file" || str(t, dir[0], "name") != "pull_request_template.md" || str(t, dir[0], "path") != ".github/pull_request_template.md" || str(t, dir[0], "sha") != str(t, f, "sha") {
		t.Errorf(".github listing = %v", dir)
	}
	root := get(t, s, api+"/contents").arr(t)
	types := map[string]string{}
	for _, e := range root {
		types[str(t, e, "name")] = str(t, e, "type")
	}
	if types[".github"] != "dir" || types["README.md"] != "file" || types["Dockerfile"] != "file" {
		t.Errorf("root = %v", types)
	}
	// ref picks a branch: bucket.go exists on the PR branch only.
	expect(t, get(t, s, api+"/contents/limit/bucket.go"), 404)
	b := get(t, s, api+"/contents/limit/bucket.go?ref=1-rate-limit-the-export-endpoint").obj(t)
	if str(t, b, "name") != "bucket.go" {
		t.Errorf("branch file = %v", b)
	}
	sha := str(t, get(t, s, api+"/commits/1-rate-limit-the-export-endpoint").obj(t), "sha")
	expect(t, get(t, s, api+"/contents/limit/bucket.go?ref="+sha), 200)
	if d := get(t, s, api+"/contents/limit?ref=1-rate-limit-the-export-endpoint").arr(t); len(d) != 1 || str(t, d[0], "name") != "bucket.go" {
		t.Errorf("limit dir = %v", d)
	}
	nf := expect(t, get(t, s, api+"/contents/nope.txt"), 404).obj(t)
	if str(t, nf, "message") != "Not Found" {
		t.Errorf("missing = %v", nf)
	}
	expect(t, get(t, s, api+"/contents/README.md?ref=no-such-branch"), 404)
}
