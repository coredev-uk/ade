package forge

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDateJSONIsCalendarDate(t *testing.T) {
	type wrap struct {
		D Date `json:"d"`
		Z Date `json:"z,omitzero"`
	}
	b, err := json.Marshal(wrap{D: NewDate(2026, time.November, 13)})
	if err != nil || string(b) != `{"d":"2026-11-13"}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var w wrap
	if err := json.Unmarshal([]byte(`{"d":"2026-11-02","z":null}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.D.String() != "2026-11-02" || !w.Z.IsZero() {
		t.Errorf("unmarshal = %v %v", w.D, w.Z)
	}
	if err := json.Unmarshal([]byte(`{"d":"13/11/2026"}`), &w); err == nil {
		t.Error("non-ISO date accepted")
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"g/tracker#12": {Project: "g/tracker", IID: 12},
		"a/b/api!3":    {Project: "a/b/api", IID: 3, Kind: KindMR},
	} {
		got, err := ParseRef(in)
		if err != nil || got != want || got.String() != in {
			t.Errorf("ParseRef(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "#1", "g/p#", "g/p#x", "g/p#0"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
}

func TestRefJSONIsText(t *testing.T) {
	type wrap struct {
		R Ref `json:"r"`
		Z Ref `json:"z"`
	}
	b, err := json.Marshal(wrap{R: Ref{Project: "g/api", IID: 3, Kind: KindMR}})
	if err != nil || string(b) != `{"r":"g/api!3","z":""}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var w wrap
	if err := json.Unmarshal(b, &w); err != nil || w.R.String() != "g/api!3" || !w.Z.IsZero() {
		t.Fatalf("round trip = %+v, %v", w, err)
	}
}
