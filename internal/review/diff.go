package review

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/coredev-uk/ade/internal/forge"
)

// Errors from locating a comment in a diff. Both wrap with detail.
var (
	// ErrNoFile means the diff has no file with that path.
	ErrNoFile = errors.New("file is not in the diff")
	// ErrNotInDiff means the line is not shown by any hunk. GitLab rejects
	// positions outside the hunks, so callers must catch this first.
	ErrNotInDiff = errors.New("line is outside the diff hunks")
)

// Line is one line of a file's unified diff with its number on each side.
// Old is 0 for an added line and New is 0 for a removed one; an unchanged
// (context) line has both, and they can differ.
type Line struct {
	Old  int
	New  int
	Kind byte // '+' added, '-' removed, ' ' unchanged
	Text string
}

// Lines numbers every line of f's hunks. A file whose diff text was withheld
// (collapsed or too large) has none.
func Lines(f forge.DiffFile) []Line {
	var out []Line
	oldN, newN := 0, 0
	for _, l := range strings.Split(strings.TrimSuffix(f.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			fields := strings.Fields(l) // "@@ -a[,b] +c[,d] @@ ..."
			if len(fields) < 3 {
				continue
			}
			oldN, newN = hunkStart(fields[1]), hunkStart(fields[2])
		case strings.HasPrefix(l, `\`): // "\ No newline at end of file"
		case strings.HasPrefix(l, "+"):
			out = append(out, Line{New: newN, Kind: '+', Text: l[1:]})
			newN++
		case strings.HasPrefix(l, "-"):
			out = append(out, Line{Old: oldN, Kind: '-', Text: l[1:]})
			oldN++
		case newN > 0 || oldN > 0: // inside a hunk; an empty line is a stripped blank context line
			text := ""
			if len(l) > 0 {
				text = l[1:]
			}
			out = append(out, Line{Old: oldN, New: newN, Kind: ' ', Text: text})
			oldN++
			newN++
		}
	}
	return out
}

// hunkStart reads the first line number of "-a,b" or "+c,d". A hunk that is
// empty on one side ("-0,0") starts at 0, which no line carries.
func hunkStart(f string) int {
	f = strings.TrimLeft(f, "+-")
	if i := strings.IndexByte(f, ','); i >= 0 {
		f = f[:i]
	}
	n, _ := strconv.Atoi(f)
	return n
}

// File finds the diff file a path names: by its new path first, then (for a
// removed line or a renamed file) by its old path.
func File(d forge.Diff, path string) (forge.DiffFile, bool) {
	for _, f := range d.Files {
		if f.NewPath == path {
			return f, true
		}
	}
	for _, f := range d.Files {
		if f.OldPath == path {
			return f, true
		}
	}
	return forge.DiffFile{}, false
}

func position(d forge.Diff, f forge.DiffFile, oldLine, newLine int) forge.DiffPosition {
	return forge.DiffPosition{
		BaseSHA: d.BaseSHA, StartSHA: d.StartSHA, HeadSHA: d.HeadSHA,
		OldPath: f.OldPath, NewPath: f.NewPath, OldLine: oldLine, NewLine: newLine,
	}
}

// LineFor positions a comment on line newLine of path in the new version of
// the file, using the diff's latest version SHAs. An added line is anchored by
// its new number alone; an unchanged line by both numbers (GitLab requires
// the pair). The old and new path of a renamed file come from the diff, so
// callers name either.
//
// It returns ErrNotInDiff for a line no hunk shows: GitLab rejects those.
func LineFor(d forge.Diff, path string, newLine int) (forge.DiffPosition, error) {
	return locate(d, path, newLine, "new", func(l Line) bool { return l.New == newLine && l.New > 0 })
}

// OldLineFor positions a comment on line oldLine of path in the old version
// of the file: a removed line (old number alone) or an unchanged one (both).
func OldLineFor(d forge.Diff, path string, oldLine int) (forge.DiffPosition, error) {
	return locate(d, path, oldLine, "old", func(l Line) bool { return l.Old == oldLine && l.Old > 0 })
}

func locate(d forge.Diff, path string, n int, side string, match func(Line) bool) (forge.DiffPosition, error) {
	f, ok := File(d, path)
	if !ok {
		return forge.DiffPosition{}, fmt.Errorf("%w: %s", ErrNoFile, path)
	}
	lines := Lines(f)
	if len(lines) == 0 {
		return forge.DiffPosition{}, fmt.Errorf("%w: %s has no diff text (collapsed or too large)", ErrNotInDiff, path)
	}
	for _, l := range lines {
		if !match(l) {
			continue
		}
		switch l.Kind {
		case '+':
			return position(d, f, 0, l.New), nil
		case '-':
			return position(d, f, l.Old, 0), nil
		default:
			return position(d, f, l.Old, l.New), nil
		}
	}
	return forge.DiffPosition{}, fmt.Errorf("%w: %s %s line %d (commentable: %s)", ErrNotInDiff, path, side, n, ranges(lines, side))
}

// ranges summarises the line numbers a file's hunks show on one side, for
// error messages: "1-11" or "3-9, 40-44".
func ranges(lines []Line, side string) string {
	var out []string
	start, prev := 0, 0
	flush := func() {
		if start == 0 {
			return
		}
		if start == prev {
			out = append(out, strconv.Itoa(start))
		} else {
			out = append(out, fmt.Sprintf("%d-%d", start, prev))
		}
	}
	for _, l := range lines {
		n := l.New
		if side == "old" {
			n = l.Old
		}
		if n == 0 {
			continue
		}
		if start != 0 && n == prev+1 {
			prev = n
			continue
		}
		flush()
		start, prev = n, n
	}
	flush()
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}
