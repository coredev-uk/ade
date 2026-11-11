package commitmsg

import "testing"

func TestExpand(t *testing.T) {
	v := Vars{Parent: 412, Child: 17, Project: "acme"}
	for _, tc := range []struct {
		tmpl, want string
	}{
		{"", ""},
		{"GL-{parent}", "GL-412"},
		{"{project}-{parent}/{child}", "acme-412/17"},
		{"GL-{parent}:", "GL-412:"},
		{"[{parent}]", "[412]"},
	} {
		got, err := Expand(tc.tmpl, v)
		if err != nil || got != tc.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", tc.tmpl, got, err, tc.want)
		}
	}
	if got, _ := Expand("{child}", Vars{Parent: 9}); got != "9" {
		t.Errorf("{child} without a child issue = %q, want the parent's iid", got)
	}
}

func TestValidate(t *testing.T) {
	for _, ok := range []string{"", "GL-{parent}", "{project}/{child}", "no placeholders"} {
		if err := Validate(ok); err != nil {
			t.Errorf("Validate(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"GL-{parnt}", "GL-{parent", "GL-}", " GL-{parent}", "GL-{parent}\n", "{}"} {
		if err := Validate(bad); err == nil {
			t.Errorf("Validate(%q) accepted", bad)
		}
	}
	if _, err := Expand("GL-{nope}", Vars{}); err == nil {
		t.Error("Expand accepted an unknown placeholder")
	}
}

func TestApplyNeverDoublePrefixes(t *testing.T) {
	for _, tc := range []struct{ prefix, msg, want string }{
		{"GL-1", "fix the thing", "GL-1 fix the thing"},
		{"GL-1", "GL-1 fix the thing", "GL-1 fix the thing"},
		{"GL-1:", "GL-1: fix", "GL-1: fix"},
		{"GL-1", "  GL-1 fix", "  GL-1 fix"},
		{"GL-1", "GL-1", "GL-1"},
		{"", "fix", "fix"},
		{"GL-1", "", ""},
		{"GL-1", "title\n\nbody mentions GL-1", "GL-1 title\n\nbody mentions GL-1"},
		{"GL-1", "GL-12 other ticket", "GL-1 GL-12 other ticket"}, // a longer ticket is not this prefix
		{"GL-1", "GL-1-fix", "GL-1-fix"},
	} {
		if got := Apply(tc.prefix, tc.msg); got != tc.want {
			t.Errorf("Apply(%q, %q) = %q, want %q", tc.prefix, tc.msg, got, tc.want)
		}
	}
}

func TestMergeMessages(t *testing.T) {
	got := MergeMessage("GL-7", "12-limit", "main", "Rate limit exports", "acme/api!3")
	want := "Merge branch '12-limit' into 'main'\n\nGL-7 Rate limit exports\n\nSee merge request acme/api!3"
	if got != want {
		t.Errorf("MergeMessage:\n%q\nwant\n%q", got, want)
	}
	if got := MergeMessage("GL-7", "b", "main", "GL-7 Already prefixed", "a/b!1"); got != "Merge branch 'b' into 'main'\n\nGL-7 Already prefixed\n\nSee merge request a/b!1" {
		t.Errorf("a prefixed title was prefixed again: %q", got)
	}
	if got := SquashMessage("GL-7", "Rate limit"); got != "GL-7 Rate limit" {
		t.Errorf("SquashMessage = %q", got)
	}
}
