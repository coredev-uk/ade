package joblog

import (
	"slices"
	"testing"
)

func TestCleanShowsWhatTheTerminalWouldHaveLeftOnScreen(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"tabs go to the next multiple of 8", "ok\tpkg\n12345678\tx\n", []string{"ok      pkg", "12345678        x"}},
		{"other controls are dropped", "a\x07b\x08c\x7fd\x00e\r\n", []string{"abcde"}},
		{"colour is dropped", "\x1b[31mred\x1b[0m\n", []string{"red"}},
		{"progress keeps the last segment", "10%\r20%\r\x1b[0K30%\nnext\n", []string{"30%", "next"}},
		{"a trailing carriage return is not a segment", "plain\r\nline\r\n", []string{"plain", "line"}},
		{
			"sections become a header",
			"\x1b[0Ksection_start:1700000000:step_script[collapsed=true]\r\x1b[0K\x1b[36mRunning\x1b[0m\nwork\n\x1b[0Ksection_end:1700000009:step_script\r\x1b[0K\ndone\n",
			[]string{"── step_script", "Running", "work", "done"},
		},
		{"an empty trace has no lines", "", nil},
		{"a blank trace has no lines", " \n\r\n\n", nil},
	} {
		if got := Clean(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: Clean(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
