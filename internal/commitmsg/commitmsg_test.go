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

func TestExpandChildFallsBackToBranchNumber(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    Vars
		want string
	}{
		{"child issue wins over the branch", Vars{Parent: 412, Child: 17, Branch: "57-fix-export"}, "17"},
		{"branch number without a child", Vars{Parent: 412, Branch: "57-fix-export"}, "57"},
		{"parent when the branch has no number", Vars{Parent: 412, Branch: "fix-export"}, "412"},
		{"parent without a branch", Vars{Parent: 412}, "412"},
	} {
		if got, err := Expand("{child}", tc.v); err != nil || got != tc.want {
			t.Errorf("%s: Expand = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if got, _ := Expand("GL-{parent}", Vars{Parent: 412, Branch: "57-fix"}); got != "GL-412" {
		t.Errorf("{parent} followed the branch: %q", got)
	}
}

func TestBranchNumber(t *testing.T) {
	for _, tc := range []struct {
		branch string
		want   int
	}{
		{"57-fix-export", 57},
		{"57_fix", 57},
		{"57/fix", 57},
		{"57.fix", 57},
		{"57", 57},
		{"007-pad", 7},
		{"2024-cleanup", 2024}, // any leading number counts
		{"fix-57", 0},
		{"v2-x", 0},
		{"57x-fix", 0},
		{"57+fix", 0},
		{"main", 0},
		{"", 0},
		{"99999999999999999999-x", 0}, // does not fit an int
	} {
		if got := BranchNumber(tc.branch); got != tc.want {
			t.Errorf("BranchNumber(%q) = %d, want %d", tc.branch, got, tc.want)
		}
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

func TestSquashMessage(t *testing.T) {
	if got := SquashMessage("GL-7", "Rate limit"); got != "GL-7 Rate limit" {
		t.Errorf("SquashMessage = %q", got)
	}
	if got := SquashMessage("GL-7", "GL-7 Already prefixed"); got != "GL-7 Already prefixed" {
		t.Errorf("a prefixed title was prefixed again: %q", got)
	}
}
