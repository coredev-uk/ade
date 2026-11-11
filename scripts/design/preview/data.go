package main

// Synthetic data mirroring the GitLab sandbox (scripts/sandbox/gitlab-seed.sh).

var stages = []string{"assigned", "started", "branch", "draft", "ci", "review", "merged"}

type item struct {
	ref     int
	title   string
	typ     string // story | bug
	apps    []string
	stage   int // index into stages
	court   string
	why     string
	mr      string
	threads int
	sprint  string
	blocked bool
	triage  bool
	moved   string // date it last moved
}

var myWork = []item{
	{ref: 1, title: "Rate-limit the export endpoint", typ: "story", apps: []string{"api"}, stage: 5, court: "you", why: "2 threads to answer", mr: "api!1", threads: 2, sprint: "42", moved: "11 NOV"},
	{ref: 2, title: "Paginate the audit log view", typ: "bug", apps: []string{"web"}, stage: 3, court: "you", why: "draft MR · mark ready", mr: "web!1", sprint: "42", moved: "11 NOV"},
	{ref: 3, title: "Retry failed webhook deliveries", typ: "story", apps: []string{"api"}, stage: 0, court: "you", why: "start work", sprint: "42", moved: "09 NOV"},
	{ref: 4, title: "Remove the legacy CSV export path", typ: "story", apps: []string{"api"}, stage: 0, court: "you", why: "start work", sprint: "42", moved: "09 NOV"},
	{ref: 5, title: "Export timestamps ignore tenant timezone", typ: "bug", apps: []string{"api"}, stage: 1, court: "blocked", why: "workflow::blocked", sprint: "42", blocked: true, moved: "10 NOV"},
	{ref: 11, title: "Exports page sometimes shows stale totals", court: "you", why: "needs triage", sprint: "42", triage: true, moved: "11 NOV"},
	{ref: 8, title: "Scheduled exports", typ: "story", apps: []string{"api", "web"}, stage: 0, court: "you", why: "start work · 2 repos", sprint: "43", moved: "08 NOV"},
}

var sprintOpen = []item{
	{ref: 6, title: "Billing export times out for large tenants", typ: "bug", apps: []string{"api"}, court: "unassigned", sprint: "42"},
	{ref: 7, title: "Dark mode for the export dashboard", typ: "story", apps: []string{"web"}, court: "unassigned", sprint: "43"},
}

type thread struct{ where, body, who, ago string }

var threads = []thread{
	{"general", "Is a burst of 50 too high for small tenants? Support suggested 20.", "coredev-uk", "2h"},
	{"general", "Please add a test for the 429 path, including the Retry-After header.", "coredev-uk", "2h"},
}

type note struct{ sev, kind, title, ago string }

var inbox = []note{
	{"warn", "git.update.done", "pulled 4 repos · 1 skipped (web/main dirty)", "40s"},
	{"warn", "git.pull.skipped", "web/main has local changes, left untouched", "40s"},
	{"info", "mr.comment", "new thread on api!1: \"add a test for the 429 path\"", "2h"},
	{"success", "task.done", "api.test passed in 412-rate-limit-export (38s)", "3h"},
	{"error", "ci.failed", "web!1 pipeline failed: lint (eslint, 3 errors)", "yesterday"},
}

// planned writes for "Start work" on #3
var plan = []struct{ step, detail string }{
	{"child issue", "ade-demo-env/api · \"Retry failed webhook deliveries\" · type::story"},
	{"link", "relates_to tracker#3 + \"Parent: ade-demo-env/tracker#3\""},
	{"sprint", "Sprint 42 copied from parent"},
	{"branch", "3-retry-failed-webhook-deliveries from origin/main"},
	{"draft MR", "\"Draft: Retry failed webhook deliveries\" · Closes #3"},
	{"label", "tracker#3 workflow → in progress"},
	{"worktree", "~/ade/demo-gl/api/3-retry-failed-webhook-deliveries"},
	{"workspace", "zellij tab \"api#3\" · layout code"},
}
