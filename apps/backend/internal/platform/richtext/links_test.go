package richtext

import "testing"

func TestAddressBoundary(t *testing.T) {
	cases := []struct {
		before, after string
		want          bool
	}{
		{"", "", true},
		{"Mail ", ". Thanks", true},
		{"(", ") x", true},
		{"Mail ", "? Yes", true},
		{"sales-", "", false},
		{"x", "", false},
		{"a/", "", false},
		{"", "m", false},
		{"", ".com", false},
		{"", "/path", false},
		{"", "?x=1", false},
		{"", "-x", false},
		{"é", "", false},
	}
	for _, c := range cases {
		if got := AddressBoundary(c.before, c.after); got != c.want {
			t.Errorf("AddressBoundary(%q, %q) = %v, want %v", c.before, c.after, got, c.want)
		}
	}
}

func TestLinkCandidates(t *testing.T) {
	got := LinkCandidates("Go to https://x.example.com/a. Mail sales-info@x.com or (Info@X.com).", ProfileDescription)
	want := []LinkCandidate{
		{"https://x.example.com/a", "https://x.example.com/a"},
		{"sales-info@x.com", "mailto:sales-info@x.com"},
		{"Info@X.com", "mailto:Info@x.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if c := LinkCandidates("no links here, a@ b", ProfileDescription); len(c) != 0 {
		t.Fatalf("got %+v", c)
	}
}

func TestLinkCount(t *testing.T) {
	if n := LinkCount(`<p>a <a href="https://x.example.com">https://x.example.com</a> b <a href="mailto:a@b.co">a@b.co</a></p>`); n != 2 {
		t.Fatalf("n = %d", n)
	}
	if n := LinkCount("<p>none</p>"); n != 0 {
		t.Fatalf("n = %d", n)
	}
}
