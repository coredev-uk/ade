package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coredev-uk/ade/internal/config"
)

func (e *cliEnv) notify(args ...string) (string, error) { return e.run(newNotifyCmd, args...) }

func TestNotifyTestRunsTheExternalCommand(t *testing.T) {
	e := newCLIEnv(t)
	out := filepath.Join(t.TempDir(), "got")
	x := e.app.Config.Notify.External
	x.Command = `cat > "` + out + `"`
	// A kind filter that excludes notify.test must not matter: the test sends on purpose.
	x.Kinds = []string{"budget.*"}
	e.app.Config.Notify.External = x

	asJSON(t)
	res, err := e.notify("test", "--severity", "error")
	if err != nil {
		t.Fatalf("%v\n%s", err, res)
	}
	var js struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal([]byte(res), &js) != nil || !js.OK {
		t.Errorf("json = %s", res)
	}
	b, _ := os.ReadFile(out)
	var p struct{ Kind, Severity string }
	if err := json.Unmarshal(b, &p); err != nil || p.Kind != "notify.test" || p.Severity != "error" {
		t.Errorf("command stdin = %q (%v)", b, err)
	}
}

func TestNotifyTestReportsFailureAndMissingCommand(t *testing.T) {
	e := newCLIEnv(t)
	if _, err := e.notify("test"); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Errorf("no command: err = %v", err)
	}
	e.app.Config.Notify.External = config.NotifyExternal{Command: "echo no route >&2; exit 2", Timeout: config.Duration{Duration: time.Second}}
	if _, err := e.notify("test"); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Errorf("failing command: err = %v", err)
	}
}
