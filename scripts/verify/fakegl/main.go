// Command fakegl serves the in-process fake GitLab (internal/forge/gitlab/fake)
// on a local port for scripts/verify/capture-mr.sh: the sandbox group has no CI
// and its review threads change with every run, the fake has both failed jobs
// and a fixed set of threads. It prints the base URL on its first line and
// serves until it is terminated.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

func main() {
	tier := flag.String("tier", "free", "licence level to emulate: free | premium")
	template := flag.Bool("mr-template", false, "give the api project a merge request template (Start work captures)")
	latency := flag.Duration("latency", 0, "delay every response (shows loading states)")
	carried := flag.Bool("carried", false, "leave an open issue in Sprint 41 (Home's Previous sprint section; needs tracker.iterations.overlap)")
	activity := flag.Bool("agile", false, "add the history the standup and sprint-review captures need")
	review := flag.String("review", "", "MR refs (group/project!N, comma separated) to list under Reviews requested for the signed-in user")
	flag.Parse()
	t := fake.Free
	switch *tier {
	case "free":
	case "premium":
		t = fake.Premium
	default:
		fmt.Fprintf(os.Stderr, "fakegl: unknown tier %q\n", *tier)
		os.Exit(2)
	}
	var opts []fake.Option
	if *latency > 0 {
		opts = append(opts, fake.WithLatency(*latency))
	}
	srv := fake.New(t, opts...)
	defer srv.Close()
	if *activity {
		seedAgile(srv)
	}
	if *carried {
		two := 2
		_, _ = srv.AddIssue(fake.IssueSpec{
			Title: "Finish the export audit trail", Milestone: "Sprint 41", Weight: &two,
			Created: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), Labels: []string{"type::task", "app::api"},
		})
	}
	for _, r := range strings.Split(*review, ",") {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		ref, err := forge.ParseRef(r)
		if err == nil {
			err = srv.RequestReview(ref, "sandbox-dev")
		}
		if err != nil {
			srv.Close()
			fmt.Fprintln(os.Stderr, "fakegl:", err)
			os.Exit(2) //nolint:gocritic // the server is closed explicitly above
		}
	}
	if *template {
		srv.AddFile(fake.GroupPath+"/api", ".gitlab/merge_request_templates/Default.md",
			"## Summary\n\nWhat changes, and why.\n\n## Testing\n\n- [ ] Tests added or updated\n\n/label ~\"type::story\"\n/assign_reviewer @reviewer-rae\n")
	}
	fmt.Println(srv.URL)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}

// seedAgile adds activity on the day before the fake's clock (2026-11-10, so a
// standup on the 11th has a yesterday) and a sprint with scope added mid-way,
// a kept-open done issue, a closed epic'd hotfix, and older sprints for velocity.
func seedAgile(srv *fake.Server) {
	day := func(d, hm string) time.Time {
		t, _ := time.Parse("2006-01-02 15:04", d+" "+hm)
		return t
	}
	api := fake.GroupPath + "/api"
	mr := func(iid int) forge.Ref { return forge.Ref{Project: api, IID: iid, Kind: forge.KindMR} }
	srv.AddEvent(forge.Event{Action: "pushed to", Branch: "1-rate-limit-the-export-endpoint", At: day("2026-11-10", "10:05")})
	srv.AddEvent(forge.Event{Action: "commented on", TargetType: "Note", Target: mr(1), Title: "Rate-limit the export endpoint", At: day("2026-11-10", "11:00")})
	srv.AddEvent(forge.Event{Action: "commented on", TargetType: "Note", Target: mr(1), Title: "Rate-limit the export endpoint", At: day("2026-11-10", "11:10")})
	srv.AddEvent(forge.Event{Action: "opened", TargetType: "MergeRequest", Target: forge.Ref{Project: fake.GroupPath + "/web", IID: 1, Kind: forge.KindMR}, Title: "Draft: Paginate the audit log view", At: day("2026-11-10", "14:20")})
	tracker := fake.GroupPath + "/tracker"
	_ = srv.SetClosedAt(forge.Ref{Project: tracker, IID: 4}, day("2026-11-10", "16:00"))

	// Premium-only fields (weight, epic) are ignored on Free.
	five, three := 5, 3
	hot, _ := srv.AddIssue(fake.IssueSpec{
		Title: "Hotfix export crash on empty tenants", Milestone: "Sprint 42", Weight: &five, Epic: "Self-serve exports",
		Assigned: true, Created: day("2026-11-05", "09:00"), Closed: ptr(day("2026-11-09", "15:00")), Labels: []string{"type::bug", "app::api"},
	})
	_ = srv.AddSprintEvent(hot, "Sprint 42", "add", day("2026-11-05", "09:30"))
	tune, _ := srv.AddIssue(fake.IssueSpec{
		Title: "Tune the export worker pool", Milestone: "Sprint 42", Weight: &three, Assigned: true,
		Created: day("2026-10-20", "09:00"), Labels: []string{"type::story", "app::api", "workflow::done"},
	})
	_ = srv.AddLabelEvent(tune, "workflow::done", "add", day("2026-11-09", "11:00"))

	// Merged work with known assignment times, for the sprint review's cycle time:
	// #4 was assigned on the 3rd and merged on the 5th; #10 was assigned on the 4th,
	// merged on the 9th. #12 (the hotfix) has an MR but no assignment note, so its
	// cycle time starts at its creation.
	assign := func(iid int, when string) {
		_ = srv.AddSystemNote(forge.Ref{Project: tracker, IID: iid}, "assigned to @sandbox-dev", day(when, "09:00"))
	}
	assign(4, "2026-11-03")
	assign(10, "2026-11-04")
	_, _ = srv.AddMergedMR(api, "Remove the legacy CSV export path", forge.Ref{Project: tracker, IID: 4}, day("2026-11-03", "10:00"), day("2026-11-05", "17:00"))
	_, _ = srv.AddMergedMR(api, "Upgrade export worker dependencies", forge.Ref{Project: tracker, IID: 10}, day("2026-11-04", "11:00"), day("2026-11-09", "13:30"))
	_, _ = srv.AddMergedMR(api, "Hotfix export crash on empty tenants", hot, day("2026-11-06", "09:00"), day("2026-11-09", "14:30"))

	srv.AddMilestone("Sprint 39", "closed", "2026-09-21", "2026-10-02")
	srv.AddMilestone("Sprint 40", "closed", "2026-10-05", "2026-10-16")
	six := 6
	_, _ = srv.AddIssue(fake.IssueSpec{Title: "Older export work", Milestone: "Sprint 39", Weight: &six, Created: day("2026-09-10", "09:00"), Closed: ptr(day("2026-10-01", "12:00"))})
	for _, t := range []string{"Tidy export docs", "Rename export flags"} {
		_, _ = srv.AddIssue(fake.IssueSpec{Title: t, Milestone: "Sprint 40", Created: day("2026-09-30", "09:00"), Closed: ptr(day("2026-10-14", "12:00"))})
	}
}

func ptr[T any](v T) *T { return &v }
