package itemdoc

import (
	"strings"
	"testing"
)

func TestParseAcceptanceAndTasks(t *testing.T) {
	desc := "Intro line.\n\n## Acceptance criteria\n\n- [x] returns 429\n- [ ] sets Retry-After\n\n### Detail\nmore\n\n## Notes\nunrelated\n"
	d := Parse(desc)
	if d.Acceptance != "- [x] returns 429\n- [ ] sets Retry-After\n\n### Detail\nmore" {
		t.Errorf("acceptance = %q", d.Acceptance)
	}
	if d.Body != "Intro line.\n\n## Notes\nunrelated" {
		t.Errorf("body = %q", d.Body)
	}
	if len(d.Tasks) != 2 || !d.Tasks[0].Done || d.Tasks[1].Done || d.Tasks[1].Text != "sets Retry-After" {
		t.Errorf("tasks = %+v", d.Tasks)
	}
	if done, total := d.Progress(); done != 1 || total != 2 {
		t.Errorf("progress = %d/%d", done, total)
	}
}

func TestParseBoldHeadingAndFences(t *testing.T) {
	desc := "Do it.\n\n**Acceptance:**\n- the API answers\n\n```sh\n# Acceptance\n- [ ] not a task\n```\n"
	d := Parse(desc)
	if d.Acceptance == "" || len(d.Tasks) != 0 {
		t.Errorf("acceptance %q tasks %+v", d.Acceptance, d.Tasks)
	}
	if !strings.Contains(d.Acceptance, "the API answers") || !strings.Contains(d.Acceptance, "# Acceptance") {
		// the fenced "# Acceptance" stays inside the section (it is code, not a heading)
		t.Errorf("acceptance = %q", d.Acceptance)
	}
}

func TestParseNone(t *testing.T) {
	d := Parse("just text")
	if d.Acceptance != "" || d.Body != "just text" || len(d.Tasks) != 0 {
		t.Errorf("%+v", d)
	}
	if d := Parse(""); d.Body != "" {
		t.Errorf("%+v", d)
	}
}
