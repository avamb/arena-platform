package richtext

import "testing"

// An item may carry several nested lists (item.subs). The TypeScript editor
// (apps/rich-editor/tests/sublists.test.ts) holds the same two cases and must
// give the same canonical text. They are not in richtext_legacy.json because
// FromLegacy never makes a nested list, so a corpus row could not hold.
func TestItemWithSeveralSublists(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			"a bullet and a numbered sublist",
			`<ul><li>a<ul><li>b</li></ul><ol><li>c</li></ol></li></ul>`,
			`<ul><li>a<ul><li>b</li></ul><ol><li>c</li></ol></li></ul>`,
		},
		{
			"two bullet sublists",
			`<ol><li>a<ul><li>b</li></ul><ul><li>c</li></ul></li><li>d</li></ol>`,
			`<ol><li>a<ul><li>b</li></ul><ul><li>c</li></ul></li><li>d</li></ol>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mustSanitize(t, c.in, ProfileDescription); got != c.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
