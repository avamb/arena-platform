package richtext

import (
	"encoding/json"
	"os"
	"testing"
)

// linkCase is a row of testdata/links_table.json, the table of cleanHref that
// the TypeScript editor (apps/rich-editor/tests/links-table.test.ts) reads as
// well. A null want means the link must be unwrapped. Change a row only with
// both sides in mind: the two ports must agree on every one.
type linkCase struct {
	Name    string  `json:"name"`
	Profile string  `json:"profile"`
	Href    string  `json:"href"`
	Want    *string `json:"want"`
}

func TestCleanHrefSharedTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/links_table.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []linkCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 50 {
		t.Fatalf("the table has only %d rows", len(cases))
	}
	for _, c := range cases {
		p, ok := ProfileByName(c.Profile)
		if !ok {
			t.Fatalf("%s: unknown profile %q", c.Name, c.Profile)
		}
		got, accepted := cleanHref(c.Href, p)
		switch {
		case c.Want == nil && accepted:
			t.Errorf("%s: cleanHref(%q) = %q, want a refusal", c.Name, c.Href, got)
		case c.Want != nil && !accepted:
			t.Errorf("%s: cleanHref(%q) refused, want %q", c.Name, c.Href, *c.Want)
		case c.Want != nil && got != *c.Want:
			t.Errorf("%s: cleanHref(%q) = %q, want %q", c.Name, c.Href, got, *c.Want)
		}
		// What Go accepts is canonical: it comes back unchanged.
		if accepted {
			if again, ok := cleanHref(got, p); !ok || again != got {
				t.Errorf("%s: %q is not stable: %q, %v", c.Name, got, again, ok)
			}
		}
	}
}
