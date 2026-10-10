package eventbot

import (
	"strings"
	"testing"
)

func TestPagerRow(t *testing.T) {
	t.Parallel()
	if row := PagerRow(Pager{Prefix: "el:p", Page: 1, Pages: 1}, "Prev", "Next"); row != nil {
		t.Errorf("one page needs no row, got %v", row)
	}
	first := PagerRow(Pager{Prefix: "el:p", Page: 1, Pages: 3}, "Prev", "Next")
	if len(first) != 2 || first[0].Text != "1/3" || first[0].CallbackData != calNoop || first[1].CallbackData != "el:p:2" {
		t.Errorf("first page row = %+v", first)
	}
	mid := PagerRow(Pager{Prefix: "el:p", Page: 2, Pages: 3}, "Prev", "Next")
	if len(mid) != 3 || mid[0].CallbackData != "el:p:1" || mid[1].Text != "2/3" || mid[2].CallbackData != "el:p:3" {
		t.Errorf("middle page row = %+v", mid)
	}
	last := PagerRow(Pager{Prefix: "el:p", Page: 3, Pages: 3}, "Prev", "Next")
	if len(last) != 2 || last[0].CallbackData != "el:p:2" || last[1].Text != "3/3" {
		t.Errorf("last page row = %+v", last)
	}
	for _, b := range mid {
		if !strings.HasPrefix(b.Text, "‹") && !strings.HasSuffix(b.Text, "›") && !strings.Contains(b.Text, "/") {
			t.Errorf("button %q is neither a step nor the position", b.Text)
		}
		if len([]byte(b.CallbackData)) > 64 {
			t.Errorf("callback %q exceeds Telegram's 64 bytes", b.CallbackData)
		}
	}
}

func TestParseIndexAndPage(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{"0": true, "4": true, "5": false, "-1": false, "x": false, "": false, " 2 ": true} {
		if _, ok := ParseIndex(in, 5); ok != want {
			t.Errorf("ParseIndex(%q, 5) ok = %v, want %v", in, ok, want)
		}
	}
	if _, ok := ParseIndex("0", 0); ok {
		t.Error("an empty page has no row 0")
	}
	for in, want := range map[string]int{"3": 3, "0": 1, "-2": 1, "x": 1, "": 1} {
		if got := ParsePage(in); got != want {
			t.Errorf("ParsePage(%q) = %d, want %d", in, got, want)
		}
	}
	if got := ItemCallback("el:o", 3); got != "el:o:3" {
		t.Errorf("ItemCallback = %q", got)
	}
}
