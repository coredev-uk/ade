package workspace

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/coredev-uk/ade/internal/events"
	"github.com/coredev-uk/ade/internal/readiness"
)

// Event kinds the task runner publishes (PLAN 6.8).
const (
	KindTaskReady   = "task.ready"   // a service passed its readiness probe
	KindTaskDone    = "task.done"    // a oneshot exited 0
	KindTaskFailed  = "task.failed"  // a node failed to start or become ready
	KindTaskBlocked = "task.blocked" // a requirement failed, so the node never started
	KindTaskExited  = "task.exited"  // a running service stopped on its own
)

// ResultEvent turns an Up result into the event the inbox records. Failures
// are errors, so their pop-ups stay until dismissed. ok is false for a node
// that was only reused from another workspace: nothing happened.
//
// A pop-up shows the title, then the subject beneath it, so the title says
// what happened in a few words and the subject carries what is new: the tree a
// node runs in, or the cause and the way out. The raw error and the log path
// go to the detail the notification centre shows.
func ResultEvent(project string, r Result) (e events.Event, ok bool) {
	switch r.Outcome {
	case OutcomeReady:
		if r.Shared {
			return events.Event{}, false
		}
		if r.Kind == "oneshot" {
			e = events.New(KindTaskDone, events.Success, r.Node+" finished")
		} else {
			title := r.Node + " ready"
			if r.Port > 0 {
				title += fmt.Sprintf(" on :%d", r.Port)
			}
			e = events.New(KindTaskReady, events.Success, title)
		}
		e.Subject = treeLabel(r.Tree)
	case OutcomeFailed:
		cause, fix := explain(r.Detail, r.Port)
		e = events.New(KindTaskFailed, events.Error, r.Node+" failed to start")
		e.Subject = cause + "\n" + fix
	case OutcomeBlocked:
		dep := blockedDep(r.Detail)
		e = events.New(KindTaskBlocked, events.Warn, r.Node+" waits for "+dep)
		e.Subject = dep + " did not come up\nfix it, then run ade ws up"
	default:
		return events.Event{}, false
	}
	e.Project = project
	e.Detail = strings.TrimSpace(r.Detail + "\nlog: " + r.Log)
	return e, true
}

// treeLabel is "repo/tree" for a worktree path: what a pop-up says about where
// a node runs.
func treeLabel(tree string) string {
	parts := strings.Split(strings.Trim(tree, "/"), "/")
	if len(parts) < 2 {
		return tree
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

var (
	timeoutRE = regexp.MustCompile(`timeout (\d+\w*)`)
	statusRE  = regexp.MustCompile(`exit status (\d+)`)
	blockedRE = regexp.MustCompile(`^blocked: (\S+) not ready`)
)

// explain reads a failed node's detail into a short cause and the way out.
func explain(detail string, port int) (cause, fix string) {
	const rerun = "then run ade ws up"
	switch {
	case strings.Contains(detail, "address already in use"):
		if port > 0 {
			return fmt.Sprintf("port %d is already in use", port), "free the port, " + rerun
		}
		return "its port is already in use", "free the port, " + rerun
	case timeoutRE.MatchString(detail):
		return "not ready within " + timeoutRE.FindStringSubmatch(detail)[1], "read the log, fix it, " + rerun
	case statusRE.MatchString(detail):
		return "exited with status " + statusRE.FindStringSubmatch(detail)[1], "read the log, fix it, " + rerun
	}
	return firstLine(detail), "read the log, fix it, " + rerun
}

// blockedDep names the requirement a blocked result waits for.
func blockedDep(detail string) string {
	if m := blockedRE.FindStringSubmatch(detail); m != nil {
		return m[1]
	}
	return "a requirement"
}

// ExitEvent reports a service that stopped while something was following it.
func ExitEvent(project, node, logPath string) events.Event {
	detail := "log: " + logPath
	subject := "read the log\nrestart with ade ws up"
	if line := readiness.LastLine(logPath); line != "" {
		subject = "last log line: " + line + "\nrestart with ade ws up"
		detail = "last log line: " + line + "\n" + detail
	}
	e := events.New(KindTaskExited, events.Warn, node+" exited")
	e.Project, e.Subject, e.Detail = project, subject, detail
	return e
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}

// BlockedEvent reports a running service whose requirement died: it keeps
// running, but nothing it depends on answers any more.
func BlockedEvent(r Row) events.Event {
	e := events.New(KindTaskBlocked, events.Warn, r.Node+" is blocked by "+r.BlockedBy)
	e.Project = r.Project
	e.Subject = r.BlockedBy + " stopped\nstart it again with ade ws up"
	e.Detail = "log: " + r.Log
	return e
}
