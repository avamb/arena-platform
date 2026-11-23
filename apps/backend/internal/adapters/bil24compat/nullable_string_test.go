package bil24compat

import (
	"encoding/json"
	"testing"
)

func TestNullableStringTellsAbsentNullAndValue(t *testing.T) {
	var a ImportSessionAction
	if err := json.Unmarshal([]byte(`{"actionName":"x"}`), &a); err != nil || a.ArenaSeriesID.Present {
		t.Fatalf("absent: %+v %v", a.ArenaSeriesID, err)
	}
	if err := json.Unmarshal([]byte(`{"arenaSeriesId":null}`), &a); err != nil || !a.ArenaSeriesID.Present || a.ArenaSeriesID.Value != nil {
		t.Fatalf("null: %+v %v", a.ArenaSeriesID, err)
	}
	a = ImportSessionAction{}
	if err := json.Unmarshal([]byte(`{"arenaSeriesId":"abc"}`), &a); err != nil || !a.ArenaSeriesID.Present || a.ArenaSeriesID.Value == nil || *a.ArenaSeriesID.Value != "abc" {
		t.Fatalf("value: %+v %v", a.ArenaSeriesID, err)
	}
	a = ImportSessionAction{}
	if err := json.Unmarshal([]byte(`{"arenaSeriesId":5}`), &a); err == nil {
		t.Fatal("a number must be refused")
	}
}

func TestNullableStringMarshal(t *testing.T) {
	out, _ := json.Marshal(ImportSessionAction{})
	if string(out) == "" || contains(string(out), "arenaSeriesId") {
		t.Errorf("absent must be left out: %s", out)
	}
	empty := ""
	out, _ = json.Marshal(ImportSessionAction{ArenaSeriesID: NullableString{Present: true, Value: &empty}})
	if !contains(string(out), `"arenaSeriesId":""`) {
		t.Errorf("empty string kept: %s", out)
	}
	out, _ = json.Marshal(ImportSessionAction{ArenaSeriesID: NullableString{Present: true}})
	if !contains(string(out), `"arenaSeriesId":null`) {
		t.Errorf("null kept: %s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
