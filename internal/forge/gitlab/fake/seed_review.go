package fake

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coredev-uk/ade/internal/forge"
)

// seedReview gives the seeded MRs diffs and CI jobs and adds api!2, the
// stacked MR scripts/sandbox/gitlab-seed.sh creates: it targets api!1's branch
// and moves limit/bucket.go to limit/token_bucket.go with edits, so its diff
// holds a renamed file.
func (s *state) seedReview(api *project) {
	rate := s.findMR(api, 1)
	rate.Diff = &mrDiff{
		Base: "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d", Start: "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d", Head: rate.SHA,
		Files: []diffFile{{
			Old: "limit/bucket.go", New: "limit/bucket.go", NewFile: true,
			Diff: "@@ -0,0 +1,7 @@\n+package limit\n+\n+// Bucket is a per-tenant token bucket.\n+type Bucket struct {\n+\tRate  int // tokens per minute\n+\tBurst int\n+}\n",
		}},
	}
	rate.Commits = []commit{
		{SHA: "9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c", Title: "Add the token bucket", Message: "Add the token bucket\n\nOne bucket per tenant.", Author: "Sandbox Dev"},
		{SHA: rate.SHA, Title: "Expose Burst", Message: "Expose Burst", Author: "Sandbox Dev"},
	}
	for _, m := range s.mrs {
		switch {
		case m.Project.Path == GroupPath+"/web" && m.IID == 1:
			m.Title = "Draft: " + m.Title
			m.Diff = &mrDiff{
				Base: "2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e", Start: "2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e", Head: m.SHA,
				Files: []diffFile{{
					Old: "src/audit/pager.ts", New: "src/audit/pager.ts", NewFile: true,
					Diff: "@@ -0,0 +1,4 @@\n+export interface Page<T> {\n+  items: T[];\n+  nextCursor?: string;\n+}\n",
				}},
			}
			m.Pipeline.Jobs = s.jobs("build:success:41", "lint:failed:12:eslint: 2 errors, 0 warnings\\nsrc/audit/pager.ts:3 no-unused-vars", "test:skipped:0")
		case m.Project.Path == GroupPath+"/web" && m.IID == 2:
			m.Diff = &mrDiff{
				Base: "3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f", Start: "3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f", Head: m.SHA,
				Files: []diffFile{{
					Old: "src/preview/header.css", New: "src/preview/header.css", NewFile: true,
					Diff: "@@ -0,0 +1,4 @@\n+.preview th {\n+  overflow: hidden;\n+  text-overflow: ellipsis;\n+}\n",
				}},
			}
		case m == rate:
			m.Pipeline.Jobs = s.jobs("build:success:38", "test:success:64")
		}
	}

	now := s.clock
	s.mrs = append(s.mrs, &mr{
		ID: s.id(), IID: 2, Project: api, Title: "Rename bucket to token bucket",
		Desc:   "Stacked on !1: renames Bucket's file and tracks tokens explicitly.",
		State:  "opened",
		Source: "rename-bucket-to-token-bucket", Target: rate.Source, SHA: "7f6e5d4c3b2a19080f7e6d5c4b3a291807f6e5d4",
		Author: s.me, Assignees: []*user{s.me},
		Created: now.Add(-6 * time.Hour), Updated: now.Add(-6 * time.Hour),
		Pipeline: &pipeline{ID: 9003, Status: "success", Updated: now.Add(-5 * time.Hour), Jobs: s.jobs("build:success:35", "test:success:58")},
		Diff: &mrDiff{
			Base: rate.SHA, Start: rate.SHA, Head: "7f6e5d4c3b2a19080f7e6d5c4b3a291807f6e5d4",
			Files: []diffFile{{
				Old: "limit/bucket.go", New: "limit/token_bucket.go", Renamed: true,
				Diff: "@@ -1,7 +1,11 @@\n package limit\n \n-// Bucket is a per-tenant token bucket.\n+// Bucket is a per-tenant token bucket that refills continuously.\n type Bucket struct {\n \tRate  int // tokens per minute\n \tBurst int\n+\tTokens float64\n }\n+\n+// Allow reports whether a token is free.\n+func (b *Bucket) Allow() bool { return b.Tokens >= 1 }\n",
			}},
		},
	})
	stacked := s.mrs[len(s.mrs)-1]
	s.threads[s.ref(api, stacked.IID, forge.KindMR)] = []*discussion{{
		ID: fmt.Sprintf("%040x", s.id()),
		Notes: []*note{{
			ID: s.id(), Author: s.findUser("reviewer-rae"), Created: now.Add(-3 * time.Hour), Resolvable: true,
			Body: "Should Tokens refill on a timer or lazily on Allow?",
		}},
	}}
}

// jobs builds a pipeline's jobs from "name:status:seconds[:trace]" specs, where
// \n in a trace is a newline. A job's stage is its name.
func (s *state) jobs(specs ...string) []*job {
	out := make([]*job, 0, len(specs))
	for _, sp := range specs {
		f := strings.SplitN(sp, ":", 4)
		secs, _ := strconv.ParseFloat(f[2], 64)
		j := &job{ID: s.id(), Name: f[0], Stage: f[0], Status: f[1], Duration: secs}
		if len(f) == 4 {
			j.Trace = strings.ReplaceAll(f[3], `\n`, "\n") + "\n"
		} else {
			j.Trace = fmt.Sprintf("Running with gitlab-runner\n$ make %s\nJob succeeded\n", f[0])
		}
		out = append(out, j)
	}
	return out
}

// diffLine is one line of a unified diff with its numbers in the old and new
// file; 0 marks a side the line is not on (added lines have no old number).
type diffLine struct{ Old, New int }

// lines numbers every line of the hunk text. Context lines carry both
// numbers, which is how GitLab positions an unchanged line.
func (f diffFile) lines() []diffLine {
	var out []diffLine
	oldN, newN := 0, 0
	for _, l := range strings.Split(strings.TrimSuffix(f.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			// "@@ -a[,b] +c[,d] @@"
			fields := strings.Fields(l)
			if len(fields) < 3 {
				continue
			}
			oldN = hunkStart(fields[1])
			newN = hunkStart(fields[2])
		case strings.HasPrefix(l, `\`):
		case strings.HasPrefix(l, "+"):
			out = append(out, diffLine{New: newN})
			newN++
		case strings.HasPrefix(l, "-"):
			out = append(out, diffLine{Old: oldN})
			oldN++
		default:
			out = append(out, diffLine{Old: oldN, New: newN})
			oldN++
			newN++
		}
	}
	return out
}

// hunkStart reads the first line number of "-a,b" or "+c,d".
func hunkStart(f string) int {
	f = strings.TrimLeft(f, "+-")
	if i := strings.IndexByte(f, ','); i >= 0 {
		f = f[:i]
	}
	n, _ := strconv.Atoi(f)
	return n
}

// anchors reports whether a position with these lines names a diff line: the
// numbers must match a line exactly (both set for an unchanged line).
func (f diffFile) anchors(oldLine, newLine int) bool {
	for _, l := range f.lines() {
		if l.Old == oldLine && l.New == newLine {
			return true
		}
	}
	return false
}
