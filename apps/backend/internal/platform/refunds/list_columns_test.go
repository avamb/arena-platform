package refunds

import (
	"strings"
	"testing"
)

// prefixedColumns qualifies exactly the columns of refundColumns, in order.
func TestPrefixedColumnsFollowRefundColumns(t *testing.T) {
	want := strings.Split(refundColumns, ",")
	got := strings.Split(prefixedColumns("r"), ",")
	if len(got) != len(want) {
		t.Fatalf("%d prefixed columns; refundColumns has %d", len(got), len(want))
	}
	for i := range want {
		if strings.TrimSpace(got[i]) != "r."+strings.TrimSpace(want[i]) {
			t.Errorf("column %d = %q; want r.%s", i, strings.TrimSpace(got[i]), strings.TrimSpace(want[i]))
		}
	}
}
