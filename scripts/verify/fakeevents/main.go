// Command fakeevents serves the in-process fake GitLab (internal/forge/gitlab/fake)
// and changes it on command, so scripts/verify/gaps-events.sh can run the real
// `ade sync` before and after a change and read the notifications it records.
//
// It prints the base URL on its first line, then reads one step name per line
// from stdin, applies it, and prints "ok" (or "error: ..."). Steps act on
// api!1, the sandbox user's MR reviewed by reviewer-rae:
//
//	canceled      the head pipeline is canceled
//	changes       rae requests changes
//	thread        rae opens a new thread
//	reply         rae replies in the thread the thread step opened
//	mine          the sandbox user comments (new thread and a reply), approves
//	approve-rae   rae approves
//	merge-rae     rae merges
//	merge-me      the sandbox user merges
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
)

const (
	me  = "sandbox-dev"
	rae = "reviewer-rae"
)

func main() {
	srv := fake.New(fake.Free)
	defer srv.Close()
	fmt.Println(srv.URL)

	mr := forge.Ref{Project: fake.GroupPath + "/api", IID: 1, Kind: forge.KindMR}
	// The thread the thread step opened: where a reply lands.
	thread := ""
	note := func(disc, author, body string) error {
		id, err := srv.AddNote(mr, disc, author, body)
		if thread == "" && disc == "" {
			thread = id
		}
		return err
	}
	steps := map[string]func() error{
		"canceled":    func() error { return srv.SetPipelineStatus(mr, "canceled") },
		"changes":     func() error { return srv.SetReviewState(mr, rae, "requested_changes") },
		"thread":      func() error { return note("", rae, "One more thing.") },
		"approve-rae": func() error { return srv.ApproveAs(mr, rae) },
		"merge-rae":   func() error { return srv.MergeMRAs(mr, rae) },
		"merge-me":    func() error { return srv.MergeMRAs(mr, me) },
	}
	steps["reply"] = func() error {
		if thread == "" {
			return fmt.Errorf("no thread to reply in yet: run the thread step first")
		}
		return note(thread, rae, "A reply.")
	}
	steps["mine"] = func() error {
		for _, f := range []func() error{
			func() error { return note("", me, "A note to self.") },
			func() error { return srv.ApproveAs(mr, me) },
		} {
			if err := f(); err != nil {
				return err
			}
		}
		return nil
	}

	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		name := strings.TrimSpace(in.Text())
		step, ok := steps[name]
		if !ok {
			fmt.Printf("error: unknown step %q\n", name)
			continue
		}
		if err := step(); err != nil {
			fmt.Printf("error: %v\n", err)
			continue
		}
		fmt.Println("ok")
	}
}
