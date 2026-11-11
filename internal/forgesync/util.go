package forgesync

import (
	"context"
	"regexp"
	"strconv"
	"sync"

	"github.com/coredev-uk/ade/internal/forge"
)

// concurrency caps simultaneous requests per forge.
const concurrency = 4

// parallel runs fn(0..n-1) with at most concurrency in flight. The first
// error cancels the rest and is returned.
func parallel(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
		sem   = make(chan struct{}, concurrency)
	)
loop:
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				once.Do(func() { first = err; cancel() })
			}
		}()
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

var (
	// closesRe matches GitLab's closing keywords followed by #iid or
	// group/project#iid.
	closesRe = regexp.MustCompile(`(?i)\b(?:clos(?:e[sd]?|ing)|fix(?:e[sd]|ing)?|resolv(?:e[sd]?|ing))[ \t:]+((?:[\w.-]+(?:/[\w.-]+)+)?)#(\d+)`)
	// branchRe matches the "<iid>-slug" branch convention.
	branchRe = regexp.MustCompile(`^(\d+)-`)
)

// mrTargets lists the issues an MR works on: those its description closes
// ("Closes #3", "Fixes group/proj#3") and the one its source branch is named
// after ("3-add-limiter"), in the MR's own project.
func mrTargets(mr forge.MR) []forge.Ref {
	var out []forge.Ref
	add := func(project string, iid int) {
		if project == "" {
			project = mr.Ref.Project
		}
		ref := forge.Ref{Project: project, IID: iid, Kind: forge.KindIssue}
		for _, x := range out {
			if x == ref {
				return
			}
		}
		out = append(out, ref)
	}
	for _, m := range closesRe.FindAllStringSubmatch(mr.Description, -1) {
		if iid, err := strconv.Atoi(m[2]); err == nil && iid > 0 {
			add(m[1], iid)
		}
	}
	if m := branchRe.FindStringSubmatch(mr.SourceBranch); m != nil {
		if iid, err := strconv.Atoi(m[1]); err == nil && iid > 0 {
			add("", iid)
		}
	}
	return out
}
