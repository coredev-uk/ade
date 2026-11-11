package records

import (
	"bufio"
	"strconv"
	"strings"
)

// Frontmatter is the tiny YAML subset ADE writes: "key: value" scalars and
// "key: [a, b]" lists, one per line. Keeping it this small avoids a YAML
// dependency and keeps files grep-able.
type Frontmatter struct {
	Scalars map[string]string
	Lists   map[string][]string
}

const fence = "---"

// quote returns s bare when it is safe in the subset, quoted otherwise.
func quote(s string) string {
	if s == "" || strings.ContainsAny(s, ",[]\"\n\r") || s != strings.TrimSpace(s) || strings.HasPrefix(s, "'") {
		return strconv.Quote(s)
	}
	return s
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func listValue(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = quote(it)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// splitList splits the inside of [..] on commas outside double quotes.
func splitList(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, escaped := false, false
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, unquote(t))
		}
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// parseFrontmatter splits content into frontmatter and the body after the
// closing fence. ok is false when content has no leading frontmatter.
func parseFrontmatter(content string) (fm Frontmatter, body string, ok bool) {
	fm = Frontmatter{Scalars: map[string]string{}, Lists: map[string][]string{}}
	rest, found := strings.CutPrefix(content, fence+"\n")
	if !found {
		return fm, content, false
	}
	sc := bufio.NewScanner(strings.NewReader(rest))
	sc.Buffer(nil, 1<<20)
	consumed := 0
	for sc.Scan() {
		line := sc.Text()
		consumed += len(line) + 1
		if line == fence {
			if consumed > len(rest) {
				consumed = len(rest)
			}
			return fm, rest[consumed:], true
		}
		key, val, has := strings.Cut(line, ":")
		if !has {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			fm.Lists[key] = splitList(val[1 : len(val)-1])
		} else {
			fm.Scalars[key] = unquote(val)
		}
	}
	return fm, content, false
}

// ParseFrontmatter reads the frontmatter of an item file.
func ParseFrontmatter(content string) (Frontmatter, bool) {
	fm, _, ok := parseFrontmatter(content)
	return fm, ok
}
