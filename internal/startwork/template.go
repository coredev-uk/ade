package startwork

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/coredev-uk/ade/internal/forge"
)

// TemplateDir is where GitLab keeps merge request templates. They are read
// from the repo's default branch (PLAN 6.3, step 4).
const TemplateDir = ".gitlab/merge_request_templates"

// QuickAction is one forge quick action found in an MR template. The forge
// runs these the moment the MR is created, which is how a template ends up
// assigning people, so the dialog lists them and each can be stripped.
type QuickAction struct {
	Name    string   `json:"name"` // "assign_reviewer"
	Args    string   `json:"args,omitempty"`
	Line    string   `json:"line"`    // the template line, verbatim
	Summary string   `json:"summary"` // "request review from @a @b"
	People  []string `json:"people,omitempty"`
}

// knownActions are the quick actions GitLab documents for merge requests. A
// line like "/usr/bin/env" is prose, not a command, so only these count.
var knownActions = map[string]bool{
	"assign": true, "unassign": true, "reassign": true,
	"assign_reviewer": true, "reviewer": true, "reassign_reviewer": true, "unassign_reviewer": true, "request_review": true,
	"label": true, "unlabel": true, "relabel": true,
	"milestone": true, "remove_milestone": true,
	"draft": true, "ready": true, "wip": true,
	"target_branch": true, "merge": true, "approve": true, "unapprove": true, "rebase": true,
	"copy_metadata": true, "title": true, "due": true, "remove_due_date": true, "estimate": true, "spend": true,
	"subscribe": true, "unsubscribe": true, "squash": true, "lock_discussion": true, "unlock_discussion": true,
	"create_merge_request": true, "relate": true, "close": true, "reopen": true, "submit_review": true,
}

var actionRe = regexp.MustCompile(`^/(\w+)(?:[ \t]+(.*))?$`)

// ParseQuickActions lists the quick actions of body in order, ignoring fenced
// code blocks.
func ParseQuickActions(body string) []QuickAction {
	var out []QuickAction
	eachLine(body, func(line string, fenced bool) bool {
		if fenced {
			return true
		}
		if qa, ok := parseAction(line); ok {
			out = append(out, qa)
		}
		return true
	})
	return out
}

func parseAction(line string) (QuickAction, bool) {
	m := actionRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil || !knownActions[m[1]] {
		return QuickAction{}, false
	}
	qa := QuickAction{Name: m[1], Args: strings.TrimSpace(m[2]), Line: strings.TrimSpace(line)}
	for f := range strings.FieldsSeq(qa.Args) {
		if strings.HasPrefix(f, "@") {
			qa.People = append(qa.People, f)
		}
	}
	qa.Summary = summarize(qa)
	return qa, true
}

func summarize(qa QuickAction) string {
	who := strings.Join(qa.People, " ")
	switch qa.Name {
	case "assign", "reassign":
		return "assign " + orArgs(who, qa.Args)
	case "assign_reviewer", "reviewer", "reassign_reviewer", "request_review":
		return "request review from " + orArgs(who, qa.Args)
	case "unassign_reviewer":
		return "remove reviewers"
	case "label", "relabel":
		return "add labels " + strings.ReplaceAll(qa.Args, `"`, "")
	case "unlabel":
		return "remove labels " + strings.ReplaceAll(qa.Args, `"`, "")
	case "milestone":
		return "set milestone " + qa.Args
	case "draft", "wip":
		return "mark draft"
	case "ready":
		return "mark ready"
	case "merge":
		return "merge when the pipeline succeeds"
	case "approve":
		return "approve the MR"
	}
	if qa.Args == "" {
		return "run /" + qa.Name
	}
	return "run /" + qa.Name + " " + qa.Args
}

func orArgs(who, args string) string {
	if who != "" {
		return who
	}
	return args
}

// StripQuickActions removes the quick-action lines named in names (every
// action when names contains "*"). Stripping only changes this MR's body; the
// template in the repo is untouched.
func StripQuickActions(body string, names []string) string {
	if len(names) == 0 {
		return body
	}
	all := false
	set := map[string]bool{}
	for _, n := range names {
		if n == "*" {
			all = true
		}
		set[strings.TrimPrefix(n, "/")] = true
	}
	var kept []string
	eachLine(body, func(line string, fenced bool) bool {
		if !fenced {
			if qa, ok := parseAction(line); ok && (all || set[qa.Name]) {
				return true
			}
		}
		kept = append(kept, line)
		return true
	})
	return strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n"
}

// eachLine calls fn for every line with whether it sits inside a fenced code
// block (the fence lines themselves count as fenced).
func eachLine(body string, fn func(line string, fenced bool) bool) {
	fenced := false
	for line := range strings.SplitSeq(strings.TrimRight(body, "\n"), "\n") {
		inFence := fenced
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			inFence = true
		}
		if !fn(line, inFence) {
			return
		}
	}
}

// Slug lowercases title into a branch-safe word list of at most max runes,
// cut at a word boundary.
func Slug(title string, max int) string {
	var b strings.Builder
	dash := true // drops leading separators
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII, unicode.IsDigit(r) && r < unicode.MaxASCII:
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) <= max {
		return s
	}
	s = s[:max]
	if i := strings.LastIndexByte(s, '-'); i > max/2 {
		s = s[:i]
	}
	return strings.TrimRight(s, "-")
}

// expand replaces {name} placeholders. Unknown placeholders are kept as they
// are, so a typo in a manifest shows up in the preview instead of vanishing.
func expand(tmpl string, vars map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		if v, ok := vars[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}

var placeholderRe = regexp.MustCompile(`\{[a-z_]+\}`)

// facetLabel renders a facet value as its label ("story" → "type::story").
func facetLabel(pattern, value string) string {
	if value == "" {
		return ""
	}
	for _, ph := range []string{"{value}", "{n}"} {
		if before, after, ok := strings.Cut(pattern, ph); ok {
			return before + value + after
		}
	}
	return ""
}

// refIn writes an issue reference the way GitLab resolves it from project:
// short inside the project, full outside.
func refIn(project string, ref forge.Ref) string {
	if ref.Project == project {
		return fmt.Sprintf("#%d", ref.IID)
	}
	return ref.String()
}
