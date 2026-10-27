package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordTelegramMiniappLogin_ClosedOutcomeSet(t *testing.T) {
	m, err := New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	m.RecordTelegramMiniappLogin("ok")
	m.RecordTelegramMiniappLogin("invalid")
	for i := 0; i < 50; i++ {
		m.RecordTelegramMiniappLogin("tg-user-" + string(rune('a'+i%26))) // never a new series
	}
	if got := testutil.ToFloat64(m.TelegramMiniappLoginTotal.WithLabelValues("error")); got != 50 {
		t.Errorf("error = %v, want 50 (an unknown outcome folds into error)", got)
	}
	if n := testutil.CollectAndCount(m.TelegramMiniappLoginTotal); n != 3 {
		t.Errorf("series = %d, want 3", n)
	}
	var nilMetrics *Metrics
	nilMetrics.RecordTelegramMiniappLogin("ok") // must not panic
}
