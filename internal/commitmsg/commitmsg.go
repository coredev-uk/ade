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
	PlaceholderChild   = "{child}"   // the child issue's iid in the code repo (the parent's when there is none)
	PlaceholderProject = "{project}" // the ADE project (manifest name)
)

// Vars are the values of a template's placeholders.
type Vars struct {
	Parent  int
	Child   int // 0: no child issue; {child} then expands like {parent}
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
		child = v.Parent
	}
	return strings.NewReplacer(
		PlaceholderParent, strconv.Itoa(v.Parent),
		PlaceholderChild, strconv.Itoa(child),
		PlaceholderProject, v.Project,
	).Replace(tmpl), nil
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

// MergeMessage is the merge commit message ADE sends for an MR when a prefix
// is configured, in the shape GitLab builds by default:
//
//	Merge branch 'source' into 'target'
//
//	<prefix> <MR title>
//
//	See merge request group/project!N
//
// ref is the MR as "group/project!N".
func MergeMessage(prefix, source, target, title, ref string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge branch '%s' into '%s'\n\n%s\n", source, target, Apply(prefix, title))
	fmt.Fprintf(&b, "\nSee merge request %s", ref)
	return b.String()
}

// SquashMessage is the single commit a squash merge makes: the MR title,
// prefixed.
func SquashMessage(prefix, title string) string { return Apply(prefix, title) }
