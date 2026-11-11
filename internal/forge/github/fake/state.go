package fake

import (
	"crypto/sha1" //nolint:gosec // git object ids, not security
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// viewer is the login the token authenticates as.
const viewer = "demo-user"

const (
	statePending   = "PENDING"
	stateApproved  = "APPROVED"
	stateChanges   = "CHANGES_REQUESTED"
	stateCommented = "COMMENTED"
	stateDismissed = "DISMISSED"
)

type user struct {
	ID    int64
	Login string
	Name  string
	Type  string // User, Bot, Organization
}

type label struct {
	ID          int64
	Name        string
	Color       string
	Description string
}

type milestone struct {
	ID          int64
	Number      int
	Title       string
	Description string
	State       string // open, closed
	DueOn       time.Time
	Created     time.Time
	Updated     time.Time
	Closed      time.Time
}

// itemKey names an issue or PR across repos of the owner.
type itemKey struct {
	Repo   string
	Number int
}

// item is an issue or, when PR is set, a pull request; GitHub shares one
// number space and one issue facet between the two.
type item struct {
	ID          int64
	Number      int
	Title       string
	Body        string
	State       string // open, closed
	StateReason string
	Author      string
	Labels      []string
	Assignees   []string
	Milestone   int // milestone number, 0 for none
	Created     time.Time
	Updated     time.Time
	Closed      time.Time
	Comments    []*comment
	Events      []*issueEvent
	Parent      *itemKey
	Subs        []itemKey
	PR          *pull
}

type comment struct {
	ID      int64
	User    string
	Body    string
	Created time.Time
	Updated time.Time
}

type issueEvent struct {
	ID        int64
	Event     string
	Actor     string
	Created   time.Time
	Label     string
	Milestone string
	Assignee  string
}

type pull struct {
	Draft     bool
	Merged    bool
	MergedAt  time.Time
	MergeSHA  string
	Head      string // head branch name
	Base      string // base branch name
	HeadSHA   string
	BaseSHA   string
	Reviewers []string
	Files     []*prFile
	Reviews   []*review
	Threads   []*thread

	AutoMerge   *autoMerge
	MergeParams map[string]any
	Publishes   int
}

type autoMerge struct {
	Method  string // MERGE, SQUASH, REBASE
	Enabled time.Time
}

type prFile struct {
	SHA       string
	Filename  string
	Previous  string
	Status    string // added, removed, modified, renamed
	Patch     string
	Additions int
	Deletions int
}

type review struct {
	ID        int64
	User      string
	Body      string
	State     string
	CommitID  string
	Submitted time.Time
}

type thread struct {
	ID       int64
	Path     string
	Line     int
	StartLn  int
	Side     string // LEFT, RIGHT
	Resolved bool
	Comments []*reviewComment
}

type reviewComment struct {
	ID        int64
	User      string
	Body      string
	Review    *review
	Created   time.Time
	Updated   time.Time
	InReplyTo int64
}

type commit struct {
	SHA     string
	Tree    string
	Parents []string
	Message string
	Created time.Time
	Files   map[string]string
}

type feedEvent struct {
	ID      int64
	Type    string
	Actor   string
	Created time.Time
	Payload map[string]any
}

type run struct {
	ID         int64
	Name       string
	HeadBranch string
	HeadSHA    string
	Status     string
	Conclusion string
	Number     int
	Attempt    int
	SuiteID    int64
	Created    time.Time
	Updated    time.Time
	Jobs       []*job
	Reruns     int
}

type job struct {
	ID         int64
	Name       string
	Status     string
	Conclusion string
	Started    time.Time
	Completed  time.Time
	Log        string
}

type repo struct {
	ID         int64
	Owner      string
	Name       string
	Default    string
	Archived   bool
	Milestones []*milestone
	Labels     []*label
	Items      []*item
	Feed       []*feedEvent
	Commits    map[string]*commit
	Branches   map[string]string // branch name -> commit sha
	Runs       []*run
	NextNumber int
}

type state struct {
	clock  time.Time
	tick   int
	nextID int64
	owner  string
	users  []*user
	repos  []*repo
}

// now is the fake clock: the configured time plus one second per call, so
// every mutation gets a distinct, later timestamp.
func (st *state) now() time.Time {
	st.tick++
	return st.clock.Add(time.Duration(st.tick) * time.Second)
}

// id allocates a global id, unique across every kind of object.
func (st *state) id() int64 {
	st.nextID++
	return st.nextID
}

// nodeID renders an opaque GraphQL id for kind prefix and numeric id.
func nodeID(prefix string, id int64) string {
	return prefix + "_kgDO" + strconv.FormatInt(id, 36)
}

// parseNode inverts nodeID, reporting whether s is a node id of that kind.
func parseNode(prefix, s string) (int64, bool) {
	rest, ok := strings.CutPrefix(s, prefix+"_kgDO")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 36, 64)
	return id, err == nil
}

func (st *state) user(login string) *user {
	for _, u := range st.users {
		if strings.EqualFold(u.Login, login) {
			return u
		}
	}
	return nil
}

func (st *state) userByID(id int64) *user {
	for _, u := range st.users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func (st *state) repo(owner, name string) *repo {
	if !strings.EqualFold(owner, st.owner) {
		return nil
	}
	for _, r := range st.repos {
		if strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return nil
}

func (st *state) repoNamed(name string) *repo { return st.repo(st.owner, name) }

func (st *state) lookup(k itemKey) (*repo, *item) {
	rp := st.repoNamed(k.Repo)
	if rp == nil {
		return nil, nil
	}
	return rp, rp.item(k.Number)
}

// itemByID finds an issue or PR by its global REST id.
func (st *state) itemByID(id int64) (*repo, *item) {
	for _, rp := range st.repos {
		for _, it := range rp.Items {
			if it.ID == id {
				return rp, it
			}
		}
	}
	return nil, nil
}

func (rp *repo) item(n int) *item {
	for _, it := range rp.Items {
		if it.Number == n {
			return it
		}
	}
	return nil
}

func (rp *repo) milestone(n int) *milestone {
	for _, m := range rp.Milestones {
		if m.Number == n {
			return m
		}
	}
	return nil
}

func (rp *repo) label(name string) *label {
	for _, l := range rp.Labels {
		if strings.EqualFold(l.Name, name) {
			return l
		}
	}
	return nil
}

// ensureLabel returns the repo's label of that name, creating it as GitHub
// does when an issue is labelled with an unknown name.
func (st *state) ensureLabel(rp *repo, name string) *label {
	if l := rp.label(name); l != nil {
		return l
	}
	l := &label{ID: st.id(), Name: name, Color: "ededed"}
	rp.Labels = append(rp.Labels, l)
	return l
}

// addItem appends a new issue or PR shell with the next number.
func (st *state) addItem(rp *repo, title, body, author string, created time.Time) *item {
	rp.NextNumber++
	it := &item{
		ID: st.id(), Number: rp.NextNumber, Title: title, Body: body, State: "open",
		Author: author, Created: created, Updated: created,
	}
	rp.Items = append(rp.Items, it)
	return it
}

func (st *state) addEvent(it *item, event, actor string, at time.Time) *issueEvent {
	ev := &issueEvent{ID: st.id(), Event: event, Actor: actor, Created: at}
	it.Events = append(it.Events, ev)
	return ev
}

// addFeed records a repo activity-feed event.
func (st *state) addFeed(rp *repo, typ, actor string, at time.Time, payload map[string]any) {
	rp.Feed = append(rp.Feed, &feedEvent{ID: st.id(), Type: typ, Actor: actor, Created: at, Payload: payload})
}

func (st *state) setLabels(rp *repo, it *item, actor string, add []string, at time.Time) []string {
	var added []string
	for _, name := range add {
		l := st.ensureLabel(rp, name)
		if slices.Contains(it.Labels, l.Name) {
			continue
		}
		it.Labels = append(it.Labels, l.Name)
		st.addEvent(it, "labeled", actor, at).Label = l.Name
		added = append(added, l.Name)
	}
	return added
}

func gitSHA(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00"))) //nolint:gosec // git object ids
	return hex.EncodeToString(sum[:])
}

// blobSHA is git's object id for a file's content.
func blobSHA(content string) string {
	sum := sha1.Sum([]byte("blob " + strconv.Itoa(len(content)) + "\x00" + content)) //nolint:gosec // git object ids
	return hex.EncodeToString(sum[:])
}

// treeSHA is the id of a tree holding exactly these files.
func treeSHA(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	parts := []string{"tree"}
	for _, p := range paths {
		parts = append(parts, p, blobSHA(files[p]))
	}
	return gitSHA(parts...)
}

// isAncestor reports whether ancestor is sha or reachable from it through parents.
func (rp *repo) isAncestor(ancestor, sha string) bool {
	if sha == ancestor {
		return true
	}
	c := rp.Commits[sha]
	if c == nil {
		return false
	}
	return slices.ContainsFunc(c.Parents, func(p string) bool { return rp.isAncestor(ancestor, p) })
}

// resolveRef maps a branch name or commit sha to a commit of the repo.
func (rp *repo) resolveRef(ref string) *commit {
	if sha, ok := rp.Branches[ref]; ok {
		return rp.Commits[sha]
	}
	return rp.Commits[ref]
}

func (rp *repo) runsFor(sha string) []*run {
	var out []*run
	for _, r := range rp.Runs {
		if r.HeadSHA == sha {
			out = append(out, r)
		}
	}
	return out
}

func (rp *repo) run(id int64) *run {
	for _, r := range rp.Runs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (rp *repo) job(id int64) (*run, *job) {
	for _, r := range rp.Runs {
		for _, j := range r.Jobs {
			if j.ID == id {
				return r, j
			}
		}
	}
	return nil, nil
}

// checksFailing reports whether any check on the sha has failed.
func (rp *repo) checksFailing(sha string) bool {
	for _, r := range rp.runsFor(sha) {
		for _, j := range r.Jobs {
			if j.Conclusion == "failure" || j.Conclusion == "timed_out" {
				return true
			}
		}
	}
	return false
}

// mergeable reports GitHub's mergeable and mergeable_state for an open PR.
func (rp *repo) mergeable(it *item) (bool, string) {
	p := it.PR
	switch {
	case p.Merged || it.State == "closed":
		return false, "unknown"
	case p.Draft:
		return true, "draft"
	case rp.checksFailing(p.HeadSHA):
		return true, "unstable"
	}
	return true, "clean"
}

// visible reports whether the viewer can see a comment: comments of another
// user's pending review are hidden.
func (c *reviewComment) visible() bool {
	return c.Review.State != statePending || c.Review.User == viewer
}

func (t *thread) visible() []*reviewComment {
	var out []*reviewComment
	for _, c := range t.Comments {
		if c.visible() {
			out = append(out, c)
		}
	}
	return out
}

func (p *pull) visibleReviews() []*review {
	var out []*review
	for _, r := range p.Reviews {
		if r.State != statePending || r.User == viewer {
			out = append(out, r)
		}
	}
	return out
}

func (p *pull) review(id int64) *review {
	for _, r := range p.visibleReviews() {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (p *pull) pendingOf(login string) *review {
	for _, r := range p.Reviews {
		if r.State == statePending && r.User == login {
			return r
		}
	}
	return nil
}

func (p *pull) thread(id int64) *thread {
	for _, t := range p.Threads {
		if t.ID == id && len(t.visible()) > 0 {
			return t
		}
	}
	return nil
}

// comment finds a review comment the viewer can see.
func (p *pull) comment(id int64) (*thread, *reviewComment) {
	for _, t := range p.Threads {
		for _, c := range t.Comments {
			if c.ID == id && c.visible() {
				return t, c
			}
		}
	}
	return nil, nil
}

// dropReview deletes a review and its comments; threads left empty go too.
func (p *pull) dropReview(r *review) {
	p.Reviews = slices.DeleteFunc(p.Reviews, func(x *review) bool { return x == r })
	for _, t := range p.Threads {
		t.Comments = slices.DeleteFunc(t.Comments, func(c *reviewComment) bool { return c.Review == r })
	}
	p.Threads = slices.DeleteFunc(p.Threads, func(t *thread) bool { return len(t.Comments) == 0 })
}

// approvers lists users whose latest verdict (approve, request changes or
// dismissal) is an approval.
func (p *pull) approvers() []string {
	verdict := map[string]string{}
	var order []string
	for _, r := range p.Reviews {
		switch r.State {
		case stateApproved, stateChanges, stateDismissed:
			if _, seen := verdict[r.User]; !seen {
				order = append(order, r.User)
			}
			verdict[r.User] = r.State
		}
	}
	var out []string
	for _, u := range order {
		if verdict[u] == stateApproved {
			out = append(out, u)
		}
	}
	return out
}

// diffLines returns the old-side and new-side line numbers a unified-diff
// patch covers, which is where review comments may be anchored.
func diffLines(patch string) (left, right map[int]bool) {
	left, right = map[int]bool{}, map[int]bool{}
	var oldN, newN int
	for _, ln := range strings.Split(patch, "\n") {
		if m := hunkRE.FindStringSubmatch(ln); m != nil {
			oldN, _ = strconv.Atoi(m[1])
			newN, _ = strconv.Atoi(m[2])
			continue
		}
		switch {
		case strings.HasPrefix(ln, "+"):
			right[newN] = true
			newN++
		case strings.HasPrefix(ln, "-"):
			left[oldN] = true
			oldN++
		case strings.HasPrefix(ln, " "):
			left[oldN], right[newN] = true, true
			oldN++
			newN++
		}
	}
	return left, right
}

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// countPatch counts the added and removed lines of a patch.
func countPatch(patch string) (add, del int) {
	for _, ln := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(ln, "+"):
			add++
		case strings.HasPrefix(ln, "-"):
			del++
		}
	}
	return add, del
}

// addedPatch is the patch of a new file with the given content.
func addedPatch(content string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	return fmt.Sprintf("@@ -0,0 +1,%d @@\n+%s", len(lines), strings.Join(lines, "\n+"))
}

// removedPatch is the patch of a deleted file with the given content.
func removedPatch(content string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	return fmt.Sprintf("@@ -1,%d +0,0 @@\n-%s", len(lines), strings.Join(lines, "\n-"))
}

// modifiedPatch replaces a whole file: every old line goes, every new line
// comes. Crude, but a valid hunk comments can be anchored to.
func modifiedPatch(oldC, newC string) string {
	o := strings.Split(strings.TrimSuffix(oldC, "\n"), "\n")
	n := strings.Split(strings.TrimSuffix(newC, "\n"), "\n")
	return fmt.Sprintf("@@ -1,%d +1,%d @@\n-%s\n+%s", len(o), len(n), strings.Join(o, "\n-"), strings.Join(n, "\n+"))
}

// diffCommits lists the files that differ between two commits.
func diffCommits(base, head *commit) []*prFile {
	var out []*prFile
	paths := make([]string, 0, len(head.Files))
	for p := range head.Files {
		paths = append(paths, p)
	}
	for p := range base.Files {
		if _, ok := head.Files[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		oldC, hadOld := base.Files[p]
		newC, hasNew := head.Files[p]
		f := &prFile{Filename: p}
		switch {
		case !hadOld:
			f.Status, f.Patch, f.SHA = "added", addedPatch(newC), blobSHA(newC)
		case !hasNew:
			f.Status, f.Patch, f.SHA = "removed", removedPatch(oldC), blobSHA(oldC)
		case oldC != newC:
			f.Status, f.Patch, f.SHA = "modified", modifiedPatch(oldC, newC), blobSHA(newC)
		default:
			continue
		}
		f.Additions, f.Deletions = countPatch(f.Patch)
		out = append(out, f)
	}
	return out
}

var (
	closingRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[:\s]+((?:[\w.-]+/[\w.-]+)?)#(\d+)`)
	mentionRE = regexp.MustCompile(`(?:^|[^\w./-])((?:[\w.-]+/[\w.-]+)?)#(\d+)\b`)
)

// refsIn extracts the issues a body points at: closing keywords when
// closing, any mention otherwise. A bare #n means the body's own repo.
func (st *state) refsIn(re *regexp.Regexp, own *repo, body string) []itemKey {
	var out []itemKey
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.Atoi(m[2])
		name := own.Name
		if m[1] != "" {
			owner, rn, _ := strings.Cut(m[1], "/")
			if !strings.EqualFold(owner, st.owner) {
				continue
			}
			name = rn
		}
		out = append(out, itemKey{Repo: name, Number: n})
	}
	return out
}
