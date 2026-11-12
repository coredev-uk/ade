// Package commitmsg is the commit prefix convention (manifest
// conventions.commit_prefix, e.g. "GL-{parent}"): expanding the template for a
// work item, putting the prefix on a message exactly once, and shaping the
// merge and squash messages ADE sends. It is pure: no I/O, no manifest types,
// so config can validate templates with it.
package commitmsg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Placeholders a commit_prefix template may use.
const (
	PlaceholderParent  = "{parent}"  // the tracker issue's iid
	PlaceholderChild   = "{child}"   // the child issue's iid in the code repo, else the branch's leading number, else the parent's
	PlaceholderProject = "{project}" // the ADE project (manifest name)
)

// Vars are the values of a template's placeholders.
type Vars struct {
	Parent  int
	Child   int    // 0: no child issue; {child} then expands to the branch's leading number, else like {parent}
	Branch  string // the branch the work is on; "" when unknown
	Project string
}

var placeholderRE = regexp.MustCompile(`\{[^{}]*\}`)

// Validate reports why tmpl is not a usable commit_prefix: empty is fine (no
// prefix), otherwise it must be one line that uses only known placeholders.
func Validate(tmpl string) error {
	if strings.ContainsAny(tmpl, "\r\n") {
		return fmt.Errorf("must be a single line")
	}
	if tmpl != strings.TrimSpace(tmpl) {
		return fmt.Errorf("must not start or end with whitespace (ADE adds the separating space)")
	}
	for _, p := range placeholderRE.FindAllString(tmpl, -1) {
		switch p {
		case PlaceholderParent, PlaceholderChild, PlaceholderProject:
		default:
			return fmt.Errorf("unknown placeholder %s: want %s, %s or %s", p, PlaceholderParent, PlaceholderChild, PlaceholderProject)
		}
	}
	rest := placeholderRE.ReplaceAllString(tmpl, "")
	if strings.ContainsAny(rest, "{}") {
		return fmt.Errorf("unbalanced brace in %q", tmpl)
	}
	return nil
}

// Expand fills tmpl for one work item. An empty template yields "" (no
// prefix). A template that does not validate is an error.
func Expand(tmpl string, v Vars) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	if err := Validate(tmpl); err != nil {
		return "", err
	}
	child := v.Child
	if child == 0 {
		child = BranchNumber(v.Branch)
	}
	if child == 0 {
		child = v.Parent
	}
	return strings.NewReplacer(
		PlaceholderParent, strconv.Itoa(v.Parent),
		PlaceholderChild, strconv.Itoa(child),
		PlaceholderProject, v.Project,
	).Replace(tmpl), nil
}

// BranchNumber is the issue number a branch name starts with: the leading run
// of digits when it ends the name or is followed by '-', '_', '/' or '.'
// ("57-fix-export", "57_fix", "57/fix", "57" are 57; "2024-cleanup" is 2024).
// 0 when the name does not start that way ("fix-57", "v2-x", "57x") or the
// number does not fit an int.
func BranchNumber(branch string) int {
	end := 0
	for end < len(branch) && branch[end] >= '0' && branch[end] <= '9' {
		end++
	}
	if end == 0 || (end < len(branch) && !strings.ContainsRune("-_/.", rune(branch[end]))) {
		return 0
	}
	n, err := strconv.Atoi(branch[:end])
	if err != nil {
		return 0
	}
	return n
}

// Apply puts prefix before msg, separated by one space, unless msg already
// starts with it (never a double prefix). An empty prefix or message is
// returned as is. Only the first line is looked at; the rest of msg follows
// untouched.
func Apply(prefix, msg string) string {
	if prefix == "" || msg == "" || hasPrefix(msg, prefix) {
		return msg
	}
	return prefix + " " + msg
}

// hasPrefix reports whether msg starts with prefix as a whole token: "GL-1"
// is not the prefix of "GL-12 ...".
func hasPrefix(msg, prefix string) bool {
	rest, ok := strings.CutPrefix(strings.TrimLeft(msg, " \t"), prefix)
	if !ok || rest == "" {
		return ok
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// SquashMessage is the single commit a squash merge makes: the MR title,
// prefixed.
func SquashMessage(prefix, title string) string { return Apply(prefix, title) }
