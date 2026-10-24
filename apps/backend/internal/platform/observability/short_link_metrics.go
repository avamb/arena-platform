package observability

// Short-link redirect label values (spec 43, task QL-3). GET /go/{code} is
// public and unauthenticated, so the only label is a CLOSED set: no code, link
// id, organization, address or agent is ever a label.

const subsystemShortLink = "short_link"

// shortLinkOutcomes is the allowlist of the outcome label; anything else is
// counted as "error" rather than minting a new series.
var shortLinkOutcomes = map[string]bool{
	"redirect":     true,
	"fallback":     true,
	"suspect":      true,
	"dedup":        true,
	"dropped":      true,
	"rate_limited": true,
	"error":        true,
}

// RecordShortLink counts one request of the short-link redirect. Safe on a nil
// receiver or a Metrics without the counter (bare test servers).
func (m *Metrics) RecordShortLink(outcome string) {
	if m == nil || m.ShortLinkTotal == nil {
		return
	}
	if !shortLinkOutcomes[outcome] {
		outcome = "error"
	}
	m.ShortLinkTotal.WithLabelValues(outcome).Inc()
}
