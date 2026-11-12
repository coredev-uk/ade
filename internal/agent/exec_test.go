package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRunProcessDropsUnsetVariables(t *testing.T) {
	t.Setenv("ADE_TEST_HIDDEN", "write-secret")
	t.Setenv("ADE_TEST_KEPT", "kept")
	dir := t.TempDir()
	run := func(c Cmd) string {
		t.Helper()
		var out strings.Builder
		c.Path, c.Args, c.Dir = "sh", []string{"-c", `echo "hidden=${ADE_TEST_HIDDEN-unset} kept=${ADE_TEST_KEPT-unset} extra=${ADE_TEST_EXTRA-unset}"`}, dir
		res := runProcess(context.Background(), c, lineParser{&out}, procHooks{}, false)
		if res.Err != nil || res.Exit != 0 {
			t.Fatalf("runProcess = %+v", res)
		}
		return out.String()
	}
	if got := run(Cmd{}); !strings.Contains(got, "hidden=write-secret kept=kept") {
		t.Errorf("without Unset: %q", got)
	}
	got := run(Cmd{Unset: []string{"ADE_TEST_HIDDEN"}, Env: []string{"ADE_TEST_EXTRA=x"}})
	if !strings.Contains(got, "hidden=unset kept=kept extra=x") {
		t.Errorf("with Unset: %q", got)
	}
}

// lineParser collects every stdout line.
type lineParser struct{ out *strings.Builder }

func (p lineParser) Feed(line []byte) { p.out.Write(line) }
func (lineParser) Progress() Progress { return Progress{} }
