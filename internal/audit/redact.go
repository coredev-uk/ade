package audit

import "regexp"

// Secrets never reach disk. Callers keep credentials out of params, but
// error text (git stderr, argv echoes) can still carry one, so Append runs
// every string through Redact as a backstop.
var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	// https://user:secret@host and https://token@host (ssh://git@host is just a login).
	{regexp.MustCompile(`(?i)\b(https?://)[^/\s@]+@`), "${1}***@"},
	{regexp.MustCompile(`(?i)\b((?:authorization|private-token|job-token)["']?\s*[:=]\s*["']?(?:(?:basic|bearer)\s+)?)[A-Za-z0-9+/=_.-]{8,}`), "${1}***"},
	{regexp.MustCompile(`\bgl(?:pat|dt|ptt|rt|cbt|imt|agent|oas|soat|ffct|ft)-[A-Za-z0-9_-]{12,}`), "***"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), "***"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "***"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "***"},
}

// Redact masks credentials in s: URL userinfo, auth headers, and well-known
// token shapes (GitLab, GitHub, OpenAI-style keys).
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

// redactValue redacts strings inside the JSON-shaped values params carry.
func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return Redact(x)
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = Redact(s)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = redactValue(e)
		}
		return out
	}
	return v
}

func redactEntry(e *Entry) {
	e.Target, e.Error, e.URL = Redact(e.Target), Redact(e.Error), Redact(e.URL)
	if e.Params != nil {
		e.Params = redactValue(e.Params).(map[string]any)
	}
}
