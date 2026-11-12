package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestMRPipelineListsJobsInPipelineOrder(t *testing.T) {
	e := newCLIEnv(t)
	out := e.mustMR("pipeline", webPath+"!1")
	for _, want := range []string{"pipeline #9002 failed", "STATUS", "passed", "build", "41s", "skipped", "1 job failed: `ade mr pipeline " + webPath + "!1 --log lint`"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if b, l, s := strings.Index(out, "build"), strings.Index(out, "lint"), strings.Index(out, "test"); l > b || b > s {
		t.Errorf("jobs not failed-first then pipeline order:\n%s", out)
	}
	if es := e.audited("forge.pipeline.retry"); len(es) != 0 {
		t.Errorf("pipeline view wrote %d retry entries", len(es))
	}
}

func TestMRPipelineLogByNameAndID(t *testing.T) {
	e := newCLIEnv(t)
	out := e.mustMR("pipeline", webPath+"!1", "--log", "lint")
	if !strings.Contains(out, "log of lint") || !strings.Contains(out, "│ src/audit/pager.ts:3 no-unused-vars") {
		t.Fatalf("log tail missing:\n%s", out)
	}
	if strings.Contains(out, "retries") {
		t.Errorf("the log view repeats the failed-jobs hint:\n%s", out)
	}
	out = e.mustMR("pipeline", webPath+"!1", "--log", "lint", "--tail", "1")
	if strings.Contains(out, "eslint: 2 errors") {
		t.Errorf("--tail 1 kept more than one line:\n%s", out)
	}

	asJSON(t)
	var v pipelineView
	if err := json.Unmarshal([]byte(e.mustMR("pipeline", webPath+"!1")), &v); err != nil {
		t.Fatal(err)
	}
	if v.Pipeline == nil || len(v.Jobs) != 3 || v.Log != nil {
		t.Fatalf("json view = %+v", v)
	}
	if v.Jobs[0].Name != "lint" {
		t.Errorf("failed job not first: %+v", v.Jobs)
	}
	id := v.Jobs[0].ID
	if err := json.Unmarshal([]byte(e.mustMR("pipeline", webPath+"!1", "--log", "#"+strconv.FormatInt(id, 10))), &v); err != nil {
		t.Fatal(err)
	}
	if v.Log == nil || v.Log.Job.Name != "lint" || !strings.Contains(v.Log.Text, "no-unused-vars") {
		t.Fatalf("log by id = %+v", v.Log)
	}

	if _, err := e.mr("pipeline", webPath+"!1", "--log", "deploy"); err == nil || !strings.Contains(err.Error(), `no job "deploy" (jobs: build, lint, test)`) {
		t.Errorf("unknown job: err = %v", err)
	}
}
