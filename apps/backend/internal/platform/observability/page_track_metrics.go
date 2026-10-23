package observability

// Page statistics beacon label values (HP-2a). The beacon is public and
// unauthenticated, so both labels are CLOSED sets: the kind is mapped through
// PageTrackKindLabel and the outcome is one of the four constants below. No
// slug, event id, organization, utm value or address is ever a label.
const (
	// LabelKind is the counted kind of a beacon hit.
	LabelKind = "kind"

	// PageTrackKindOther is the bucket for a kind outside the allowlist (a
	// rejected request).
	PageTrackKindOther = "other"

	// PageTrackCounted: the hit was added to hits.
	PageTrackCounted = "counted"
	// PageTrackSuspect: the hit was added to suspect_hits (an obvious bot).
	PageTrackSuspect = "suspect"
	// PageTrackRateLimited: a limit was over, nothing was written.
	PageTrackRateLimited = "rate_limited"
	// PageTrackRejected: not counted for any other reason (bad body, bad kind,
	// unknown page, database trouble).
	PageTrackRejected = "rejected"

	subsystemPage = "page"
)

var knownPageTrackKinds = map[string]bool{
	"page_view": true, "video_play": true, "partner_click": true,
}

// PageTrackKindLabel maps a beacon kind onto the bounded kind label value.
func PageTrackKindLabel(kind string) string {
	if knownPageTrackKinds[kind] {
		return kind
	}
	return PageTrackKindOther
}

// RecordPageTrack counts one beacon request. Safe on a nil receiver or a
// Metrics without the counter (bare test servers).
func (m *Metrics) RecordPageTrack(kind, outcome string) {
	if m == nil || m.PageTrackTotal == nil {
		return
	}
	m.PageTrackTotal.WithLabelValues(PageTrackKindLabel(kind), outcome).Inc()
}
