package refunds

import (
	"strconv"
	"strings"
	"testing"
)

// The SQL of the queue spells the stuck threshold in minutes; it must equal
// StuckAfter.
func TestQueueStuckAfterMatchesSQL(t *testing.T) {
	want := "interval '" + strconv.Itoa(int(QueueStuckAfter.Minutes())) + " minutes'"
	if !strings.Contains(queueWhere, want) {
		t.Fatalf("queueWhere does not spell QueueStuckAfter as %q", want)
	}
}
