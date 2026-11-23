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

func TestNoQuickActionsLeavesClosedCodeFencesAlone(t *testing.T) {
	in := "Use:\n```sh\n/usr/bin/env go\n/approve\n```\nthen\n~~~\n/merge\n~~~\n/merge\n"
	got := NoQuickActions(in)
	want := "Use:\n```sh\n/usr/bin/env go\n/approve\n```\nthen\n~~~\n/merge\n~~~\n\\/merge\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	suggestion := "Fix:\n```suggestion:-0+0\n/x := 1\n```"
	if NoQuickActions(suggestion) != suggestion {
		t.Fatal("a suggestion block is code and is untouched")
	}
}

func TestNoQuickActionsDoesNotTrustAnUnclosedFence(t *testing.T) {
	in := "```\n/approve\nno closing fence"
	if got := NoQuickActions(in); !strings.Contains(got, "\n\\/approve") {
		t.Fatalf("an unclosed fence protects nothing: %q", got)
	}
	// A longer fence is closed only by an equally long one.
	in = "````\n```\n/approve\n````\n/merge"
	if got := NoQuickActions(in); !strings.Contains(got, "\n/approve\n") || !strings.Contains(got, `\/merge`) {
		t.Fatalf("nested fence: %q", got)
	}
}

func TestNoQuickActionsIsIdempotentAndCRLFSafe(t *testing.T) {
	in := "ok\r\n/approve\r\n"
	once := NoQuickActions(in)
	if NoQuickActions(once) != once || strings.Contains(once, "\n/approve") {
		t.Fatalf("got %q", once)
	}
}
