package observability

// Seat hold at the click (spec 08_architecture/67). The routes are public and
// unauthenticated, so the only label is a CLOSED set: no hold, session,
// device or address is ever a label.

const subsystemHold = "hold"

// LabelResult is the result label of arena_hold_created_total.
const LabelResult = "result"

// Results of POST /v1/public/feeds/{feed_token}/holds.
const (
	HoldResultCreated       = "created"
	HoldResultAdded         = "added"
	HoldResultSeatsTaken    = "seats_taken"
	HoldResultTooManySeats  = "too_many_seats"
	HoldResultTooManyActive = "too_many_active"
	HoldResultReholdPaused  = "rehold_paused"
	HoldResultReholdUsed    = "rehold_used"
	HoldResultExpired       = "expired"
	HoldResultInPayment     = "in_payment"
	HoldResultNotFound      = "not_found"
	HoldResultRejected      = "rejected"
	HoldResultDisabled      = "disabled"
	HoldResultError         = "error"
)

var holdResults = map[string]bool{
	HoldResultCreated:       true,
	HoldResultAdded:         true,
	HoldResultSeatsTaken:    true,
	HoldResultTooManySeats:  true,
	HoldResultTooManyActive: true,
	HoldResultReholdPaused:  true,
	HoldResultReholdUsed:    true,
	HoldResultExpired:       true,
	HoldResultInPayment:     true,
	HoldResultNotFound:      true,
	HoldResultRejected:      true,
	HoldResultDisabled:      true,
	HoldResultError:         true,
}

// RecordHoldCreated counts one hold request by result; a result outside the
// closed set is counted as "error". Safe on a nil receiver or a Metrics
// without the counter (bare test servers).
func (m *Metrics) RecordHoldCreated(result string) {
	if m == nil || m.HoldCreatedTotal == nil {
		return
	}
	if !holdResults[result] {
		result = HoldResultError
	}
	m.HoldCreatedTotal.WithLabelValues(result).Inc()
}
