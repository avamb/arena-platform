package observability_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/eventmaterials"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// The page beacon counter is fed by an UNAUTHENTICATED endpoint, so its labels
// must stay a closed set whatever the caller sends.

func TestPageTrackKindLabel_MatchesTheTableAllowlist(t *testing.T) {
	for _, k := range eventmaterials.StatKinds {
		if got := observability.PageTrackKindLabel(k); got != k {
			t.Errorf("PageTrackKindLabel(%q) = %q; every counted kind keeps its own label", k, got)
		}
	}
}

func TestPageTrackKindLabel_CollapsesEverythingElse(t *testing.T) {
	for _, unknown := range []string{"", "PAGE_VIEW", "click", "page_view\x00", strings.Repeat("x", 4096), "<script>"} {
		if got := observability.PageTrackKindLabel(unknown); got != observability.PageTrackKindOther {
			t.Errorf("PageTrackKindLabel(%q) = %q; want %q", unknown, got, observability.PageTrackKindOther)
		}
	}
}

func TestRecordPageTrack_BoundsCardinality(t *testing.T) {
	m, err := observability.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("observability.New: %v", err)
	}
	// 500 different caller-chosen kinds must land in ONE series.
	for i := 0; i < 500; i++ {
		m.RecordPageTrack(strings.Repeat("k", i+1), observability.PageTrackRejected)
	}
	m.RecordPageTrack("page_view", observability.PageTrackCounted)
	m.RecordPageTrack("page_view", observability.PageTrackSuspect)
	m.RecordPageTrack("video_play", observability.PageTrackRateLimited)

	if got := testutil.CollectAndCount(m.PageTrackTotal); got != 4 {
		t.Fatalf("series = %d, want 4 (other/rejected, page_view/counted, page_view/suspect, video_play/rate_limited)", got)
	}
	if got := testutil.ToFloat64(m.PageTrackTotal.WithLabelValues("other", observability.PageTrackRejected)); got != 500 {
		t.Errorf("other/rejected = %v, want 500", got)
	}
}

func TestRecordPageTrack_NilSafe(_ *testing.T) {
	var m *observability.Metrics
	m.RecordPageTrack("page_view", observability.PageTrackCounted)
	(&observability.Metrics{}).RecordPageTrack("page_view", observability.PageTrackCounted)
}
