package aimark

import (
	"strings"
	"testing"
)

func TestMarkAppendsOnceAndStripRestores(t *testing.T) {
	got := Mark("Nil check missing.")
	if want := "Nil check missing.\n\n" + Marker; got != want {
		t.Fatalf("Mark = %q, want %q", got, want)
	}
	if Mark(got) != got {
		t.Fatalf("Mark is not idempotent: %q", Mark(got))
	}
	if Mark(got+"\n\n") != got {
		t.Fatal("trailing blank lines after the mark must not double it")
	}
	if s := Strip(got); s != "Nil check missing." {
		t.Fatalf("Strip = %q", s)
	}
	if Strip("plain\n\n") != "plain" || Strip("plain") != "plain" {
		t.Fatal("Strip of unmarked text only trims trailing space")
	}
	if !Marked(got) || Marked("plain") || Marked(Strip(got)) {
		t.Fatal("Marked disagrees with Mark and Strip")
	}
	if Mark("") != "" || Mark(" \n") != "" {
		t.Fatal("empty text is not marked")
	}
}

func TestMarkKeepsTheMarkLastWhenTheBodyMentionsIt(t *testing.T) {
	body := "see " + Marker + " above\nmore"
	got := Mark(body)
	if !strings.HasSuffix(got, "\n\n"+Marker) || !strings.HasPrefix(got, body) {
		t.Fatalf("Mark = %q", got)
	}
}

func TestNoQuickActionsEscapesEverySlashCommandLine(t *testing.T) {
	in := strings.Join([]string{
		"Nil check missing.",
		"/approve",
		"  /run_pipeline now",
		"/spend_time 1h",
		"/unlock",
		"/remove_reviewer @bob",
		"a path /usr/bin/env mid-line stays",
		"/ alone is not a command",
	}, "\n")
	got := NoQuickActions(in)
	for _, cmd := range []string{"/approve", "/run_pipeline", "/spend_time", "/unlock", "/remove_reviewer"} {
		if strings.Contains(got, "\n"+cmd) || strings.Contains(got, "\n  "+cmd) {
			t.Errorf("%s still starts a line:\n%s", cmd, got)
		}
		if !strings.Contains(got, `\`+cmd) {
			t.Errorf("%s lost its text:\n%s", cmd, got)
		}
	}
	if !strings.Contains(got, "a path /usr/bin/env mid-line stays") || !strings.Contains(got, "\n/ alone is not a command") {
		t.Errorf("lines that are not commands changed:\n%s", got)
	}
	for l := range strings.SplitSeq(got, "\n") {
		if quickAction.MatchString(l) {
			t.Errorf("line %q would still be read as a command", l)
		}
	}
}

// GitLab 16.9 and later reads commands from the top-level paragraphs of the
// parsed Markdown; older versions matched backtick fences with a regular
// expression. A fence aimark believed in can be no fence to the forge, so no
// line is exempt.
func TestNoQuickActionsEscapesLinesInsideAndAroundFencesToo(t *testing.T) {
	for name, in := range map[string]string{
		"backtick fence":     "Use:\n```sh\n/approve\n```",
		"tilde fence":        "~~~\n/approve\n~~~",
		"nested fence":       "````\n```\n/approve\n````",
		"unclosed fence":     "```\n/approve\nno closing fence",
		"suggestion block":   "```suggestion:-0+0\n/approve\n```",
		"list item fence":    "- item\n  ```\n/approve\n  ```\n",
		"html comment":       "<!--\n```\n-->\n/approve\n<!--\n```\n-->",
		"div and blank line": "<div>\n```\n\n/approve\n</div>",
		"indented":           "    /approve",
		"no break space":     "\u00a0/approve",
		"zero width":         "\u200b/approve",
		"after a fence line": "```\n```\n/approve",
	} {
		got := NoQuickActions(in)
		for l := range strings.SplitSeq(got, "\n") {
			if quickAction.MatchString(l) {
				t.Errorf("%s: line %q would still be read as a command:\n%s", name, l, got)
			}
		}
		if !strings.Contains(got, `\/approve`) {
			t.Errorf("%s: the command text is gone:\n%s", name, got)
		}
	}
	// Text that is not a command keeps its bytes.
	for _, keep := range []string{"```go\nx := 1 // not /a command\n```", "a/b", "see /usr/bin mid-line", "// comment", "/* c */", "/ alone"} {
		if NoQuickActions(keep) != keep {
			t.Errorf("%q must not change", keep)
		}
	}
}

func TestNoQuickActionsIsIdempotentAndCRLFSafe(t *testing.T) {
	in := "ok\r\n/approve\r\n"
	once := NoQuickActions(in)
	if NoQuickActions(once) != once || strings.Contains(once, "\n/approve") {
		t.Fatalf("got %q", once)
	}
}

func TestQuickActionNameAgreesWithNoQuickActions(t *testing.T) {
	for _, line := range []string{"/close", "  /assign @rae", "\t/label ~bug", "\u200b/approve", "/usr/bin/env", "/ no", "//x", "text /close", ""} {
		_, ok := QuickActionName(line)
		if esc := NoQuickActions(line) != line; ok != esc {
			t.Errorf("%q: QuickActionName %v, NoQuickActions escapes %v", line, ok, esc)
		}
	}
	if cmd, _ := QuickActionName("  /assign @rae"); cmd != "/assign" {
		t.Errorf("cmd = %q", cmd)
	}
}
