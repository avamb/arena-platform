package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordShortLink_ClosedOutcomeSet(t *testing.T) {
	m, err := New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	m.RecordShortLink("redirect")
	m.RecordShortLink("redirect")
	m.RecordShortLink("x7k2qpm") // never a new series
	if got := testutil.ToFloat64(m.ShortLinkTotal.WithLabelValues("redirect")); got != 2 {
		t.Errorf("redirect = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.ShortLinkTotal.WithLabelValues("error")); got != 1 {
		t.Errorf("error = %v, want 1 (an unknown outcome folds into error)", got)
	}
	if n := testutil.CollectAndCount(m.ShortLinkTotal); n != 2 {
		t.Errorf("series = %d, want 2", n)
	}
	var nilMetrics *Metrics
	nilMetrics.RecordShortLink("redirect") // must not panic
}
