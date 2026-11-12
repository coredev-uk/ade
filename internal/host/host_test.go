package host

import (
	"slices"
	"testing"

	"github.com/coredev-uk/ade/internal/host/zellij"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name   string
		env    Env
		action Action
		ok     bool
	}{
		{"auto inside the ade session", Env{Mode: ModeAuto, InsideZellij: true, CurrentSession: "ade"}, FocusHome, true},
		{"auto inside another session", Env{Mode: ModeAuto, InsideZellij: true, CurrentSession: "work"}, OpenHome, true},
		{"inside zellij without a session name", Env{Mode: ModeAuto, InsideZellij: true}, RunHere, true},
		{"a session that only starts with ade is another session", Env{InsideZellij: true, CurrentSession: "ade-old"}, OpenHome, true},
		{"plain inside zellij runs here", Env{Mode: ModePlain, InsideZellij: true, CurrentSession: "ade"}, RunHere, true},
		{"vscode inside zellij runs here", Env{Mode: ModeVSCode, InsideZellij: true, CurrentSession: "work"}, RunHere, true},
		{"auto absent session", Env{Mode: ModeAuto, ZellijAvailable: true}, Create, true},
		{"auto live session", Env{Mode: ModeAuto, ZellijAvailable: true, Session: zellij.Live}, Attach, true},
		{"auto exited session", Env{Mode: ModeAuto, ZellijAvailable: true, Session: zellij.Exited}, Attach, true},
		{"auto no zellij", Env{Mode: ModeAuto}, RunHere, true},
		{"empty mode behaves as auto", Env{ZellijAvailable: true}, Create, true},
		{"plain ignores zellij", Env{Mode: ModePlain, ZellijAvailable: true, Session: zellij.Live}, RunHere, true},
		{"vscode runs here", Env{Mode: ModeVSCode, ZellijAvailable: true}, RunHere, true},
		{"zellij mode missing binary", Env{Mode: ModeZellij}, RunHere, false},
		{"zellij mode creates", Env{Mode: ModeZellij, ZellijAvailable: true}, Create, true},
		{"zellij mode inside the ade session", Env{Mode: ModeZellij, InsideZellij: true, CurrentSession: "ade"}, FocusHome, true},
	}
	for _, c := range cases {
		a, ok := Decide(c.env)
		if a != c.action || ok != c.ok {
			t.Errorf("%s: got (%v,%v), want (%v,%v)", c.name, a, ok, c.action, c.ok)
		}
	}
}

func TestArgv(t *testing.T) {
	cases := []struct {
		a      Action
		state  zellij.State
		config string
		want   []string
	}{
		{Create, zellij.Absent, "", []string{"zellij", "--session", "ade", "--new-session-with-layout", "/l.kdl"}},
		{Attach, zellij.Live, "", []string{"zellij", "attach", "ade"}},
		{Attach, zellij.Exited, "", []string{"zellij", "attach", "--force-run-commands", "ade"}},
		{RunHere, zellij.Absent, "", nil},
		{FocusHome, zellij.Live, "", nil},
		{OpenHome, zellij.Live, "", nil},
		{Create, zellij.Absent, "/c/z.kdl", []string{"zellij", "--config", "/c/z.kdl", "--session", "ade", "--new-session-with-layout", "/l.kdl"}},
		{Attach, zellij.Live, "/c/z.kdl", []string{"zellij", "--config", "/c/z.kdl", "attach", "ade"}},
		{Attach, zellij.Exited, "/c/z.kdl", []string{"zellij", "--config", "/c/z.kdl", "attach", "--force-run-commands", "ade"}},
	}
	for _, c := range cases {
		if got := Argv(c.a, c.state, "/l.kdl", c.config); !slices.Equal(got, c.want) {
			t.Errorf("Argv(%v,%v,%q) = %v, want %v", c.a, c.state, c.config, got, c.want)
		}
	}
}

func TestUsesUnlockFirstOnlyInsideVSCodeWithoutAUserConfig(t *testing.T) {
	none := func(string) string { return "" }
	userCfg := func(k string) string {
		if k == "ZELLIJ_CONFIG_FILE" {
			return "/home/me/z.kdl"
		}
		return ""
	}
	if !(Env{InsideVSCode: true}).UsesUnlockFirst(none) {
		t.Error("zellij started in a VS Code terminal must get the unlock-first preset")
	}
	if (Env{}).UsesUnlockFirst(none) {
		t.Error("outside VS Code the user's own zellij config applies")
	}
	if (Env{InsideVSCode: true}).UsesUnlockFirst(userCfg) {
		t.Error("an explicit $ZELLIJ_CONFIG_FILE wins over ADE's preset")
	}
}

func TestFocusesHomeFirstOnlyBeforeAttachingALiveSession(t *testing.T) {
	cases := []struct {
		a     Action
		state zellij.State
		want  bool
	}{
		{Attach, zellij.Live, true},
		{Attach, zellij.Exited, false},
		{Create, zellij.Absent, false},
		{RunHere, zellij.Live, false},
		{FocusHome, zellij.Live, false},
	}
	for _, c := range cases {
		if got := FocusesHomeFirst(c.a, c.state); got != c.want {
			t.Errorf("FocusesHomeFirst(%v,%v) = %v, want %v", c.a, c.state, got, c.want)
		}
	}
}
