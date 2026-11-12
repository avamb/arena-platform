package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.json"), filepath.Join(dir, "out.json")
	src := `[{"key":"a","iso2":"CZ","city":"Karlovy Vary"},
{"key":"b","iso2":"US","city":"New York"},
{"key":"c","iso2":"US","city":"Nowhereville"}]`
	if err := os.WriteFile(in, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(in, out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got []Item
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("items %d", len(got))
	}
	if got[0].Timezone != "Europe/Prague" || got[0].Rule != RuleCountry {
		t.Fatalf("CZ: %+v", got[0])
	}
	if got[1].Timezone != "America/New_York" || got[1].Rule != RuleCity {
		t.Fatalf("New York: %+v", got[1])
	}
	if got[2].Timezone != "" || got[2].Rule != RuleUnresolved || len(got[2].Zones) < 2 {
		t.Fatalf("Nowhereville: %+v", got[2])
	}
}
