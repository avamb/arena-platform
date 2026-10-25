package richtext

import (
	"encoding/json"
	"os"
	"testing"
)

// schemeCase is a row of testdata/scheme_words_table.json, which the
// TypeScript editor (apps/rich-editor/tests/scheme-words.test.ts) reads as
// well: it must flag exactly the texts this Sanitize refuses with
// ReasonSchemeWord. An empty reason means no scheme word.
type schemeCase struct {
	Name    string `json:"name"`
	Profile string `json:"profile"`
	HTML    string `json:"html"`
	Reason  string `json:"reason"`
}

func TestSchemeWordSharedTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/scheme_words_table.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []schemeCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("the table has only %d rows", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			p, ok := ProfileByName(c.Profile)
			if !ok {
				t.Fatalf("unknown profile %q", c.Profile)
			}
			_, err := Sanitize(c.HTML, p)
			got := ""
			if err != nil {
				got = string(ReasonOf(err))
			}
			if got != c.Reason {
				t.Fatalf("Sanitize(%q): reason %q (%v), want %q", c.HTML, got, err, c.Reason)
			}
		})
	}
}
