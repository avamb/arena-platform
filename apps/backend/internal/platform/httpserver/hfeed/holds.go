// holds.go — the public seat hold at the click (spec 08_architecture/67,
// section 4): shared types, the visitor hashes, the hold token and the
// answer of every hold route. The routes live in holds_create.go and
// holds_manage.go.
//
// A hold is one reservations row with hold_token_hash set (migration 0225).
// The hold token is HMAC-SHA256(HOLD_VISITOR_KEY, "hold_token:" + hold id),
// base64url: 32 bytes the client cannot guess, derived rather than stored, so
// a device that lost the token (second tab, lost answer) gets the token of its
// open hold back (section 4.1). Only SHA-256 of the token is stored. The
// device id and the client address are stored as HMAC-SHA256 values keyed by
// the same key, each with its own prefix.
package hfeed

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/observability"
)

// HoldsMaxBody is how much of a hold request body is read, by the handler and
// by the idempotency middleware in front of it alike.
const HoldsMaxBody = 16 * 1024

const (
	// holdExtendWindow: an extension is taken only this close to the end
	// (60 s of the button plus 15 s of clock skew, spec 67 section 3.3).
	holdExtendWindow = 75 * time.Second
	// holdReholdWindow: "hold again" is accepted this long after the expiry
	// (section 4.6).
	holdReholdWindow = 10 * time.Minute
	// holdMaxSeatKeysPerRequest bounds one request before any database work.
	holdMaxSeatKeysPerRequest = 50
	// holdMaxDeviceIDLen bounds the device id (a UUID v4 in the widget).
	holdMaxDeviceIDLen = 128
)

// HoldSettings is the deployment configuration of the hold routes.
type HoldSettings struct {
	// Enabled is HOLD_ON_CLICK_ENABLED. Off: every hold route answers 404
	// hold.disabled.
	Enabled bool
	// IPLimitEnabled is HOLD_IP_LIMIT_ENABLED: the open-holds limit per
	// address and the address pause of a request without a device id.
	IPLimitEnabled bool
	// VisitorKey is HOLD_VISITOR_KEY. Empty with Enabled on also answers
	// hold.disabled (config validation refuses that combination).
	VisitorKey string
	// Idempotency is the Idempotency-Key middleware of POST .../holds; nil
	// runs the request without it.
	Idempotency func(http.Handler) http.Handler
}

// HoldsHandler serves the hold routes on top of the feed Handler.
type HoldsHandler struct {
	h   *Handler
	cfg HoldSettings
	now func() time.Time
}

// Holds returns the hold routes' handler with the given settings.
func (h *Handler) Holds(cfg HoldSettings) *HoldsHandler {
	return &HoldsHandler{h: h, cfg: cfg, now: func() time.Time { return time.Now().UTC() }}
}

// HoldsIdempotencyScope is the idempotency_keys.scope of a hold request: one
// key namespace per feed token, no actor.
func HoldsIdempotencyScope(r *http.Request) string {
	return "POST holds:" + chi.URLParam(r, "feed_token")
}

// ready answers 404 hold.disabled when the routes are off and 503 without a
// database; it returns false when it has answered.
func (hh *HoldsHandler) ready(w http.ResponseWriter, r *http.Request) bool {
	if !hh.cfg.Enabled || hh.cfg.VisitorKey == "" {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"hold.disabled", "Seat holds are not available here.", r,
		))
		return false
	}
	if hh.h.pool == nil || hh.h.reservationQueries == nil || hh.h.publicFeedQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "The service is temporarily unavailable. Try again in a minute.", r,
		))
		return false
	}
	return true
}

// mac is HMAC-SHA256(HOLD_VISITOR_KEY, prefix + value).
func (hh *HoldsHandler) mac(prefix, value string) []byte {
	m := hmac.New(sha256.New, []byte(hh.cfg.VisitorKey))
	m.Write([]byte(prefix))
	m.Write([]byte(value))
	return m.Sum(nil)
}

// tokenFor is the hold token of a hold.
func (hh *HoldsHandler) tokenFor(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(hh.mac("hold_token:", id.String()))
}

// deviceHash is nil without a device id.
func (hh *HoldsHandler) deviceHash(deviceID string) []byte {
	if deviceID == "" {
		return nil
	}
	return hh.mac("device:", deviceID)
}

// ipHash is nil without an address.
func (hh *HoldsHandler) ipHash(ip string) []byte {
	if ip == "" {
		return nil
	}
	return hh.mac("ip:", ip)
}

// holdTokenHash is the stored form of a hold token: SHA-256.
func holdTokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// advisoryKey turns a visitor hash into a pg_advisory_xact_lock key.
func advisoryKey(hash []byte) int64 {
	return int64(binary.BigEndian.Uint64(hash[:8])) //nolint:gosec // any 64 bits are a valid key
}

// holdOpen reports whether a reservation state is still open.
func holdOpen(state string) bool { return state == "draft" || state == "active" }

// holdPolicyOf reads the policy snapshot of a hold, Default when unreadable.
func holdPolicyOf(raw []byte) holdpolicy.Policy {
	if len(raw) == 0 {
		return holdpolicy.Default()
	}
	var p holdpolicy.Policy
	if err := json.Unmarshal(raw, &p); err != nil || p.Validate() != nil {
		return holdpolicy.Default()
	}
	return p
}

// holdSeatKeysOf reads hold_policy->'seat_keys': the seats the hold held
// last, kept after the expiry deleted the reservation_seats links.
func holdSeatKeysOf(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var v struct {
		SeatKeys []string `json:"seat_keys"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v.SeatKeys
}

// holdPolicyAnswer is the policy part of a hold answer.
type holdPolicyAnswer struct {
	SelectSeconds  int64 `json:"select_seconds"`
	PaymentSeconds int64 `json:"payment_seconds"`
	ExtendSeconds  int64 `json:"extend_seconds"`
	ExtendCount    int   `json:"extend_count"`
}

// HoldAnswer is the answer of every hold route (spec 67 section 4.1).
//
// step is "select" (no order), "payment" (an order waits for payment) or
// "closed". state is the reservation state, except that an open hold of the
// select step past its expiry already reads "expired". extend_available is
// true when an extension can be taken right now; extensions_left counts the
// extensions still left on the hold.
type HoldAnswer struct {
	HoldToken       string              `json:"hold_token"`
	SessionID       string              `json:"session_id"`
	State           string              `json:"state"`
	Step            string              `json:"step"`
	SeatKeys        []string            `json:"seat_keys"`
	ExpiresAt       string              `json:"expires_at"`
	ExtendAvailable bool                `json:"extend_available"`
	ExtensionsLeft  int                 `json:"extensions_left"`
	Policy          holdPolicyAnswer    `json:"policy"`
	Conflicts       []map[string]string `json:"conflicts,omitempty"`
	CheckoutToken   *string             `json:"checkout_token,omitempty"`
}

// buildHoldAnswer builds the answer of a hold row. seatKeys are the seats
// the hold holds now (open) or held last (closed).
func buildHoldAnswer(row gen.PublicHoldRow, token string, seatKeys []string, inPayment bool, now time.Time) HoldAnswer {
	pol := holdPolicyOf(row.HoldPolicy)
	open := holdOpen(row.State)
	state, step := row.State, "closed"
	if open {
		step = "select"
		if inPayment {
			step = "payment"
		} else if !row.ExpiresAt.After(now) {
			state = "expired"
		}
	}
	left := pol.ExtendCount - int(row.ExtensionsUsed)
	if left < 0 || pol.Extend <= 0 {
		left = 0
	}
	remaining := row.ExpiresAt.Sub(now)
	if seatKeys == nil {
		seatKeys = []string{}
	}
	return HoldAnswer{
		HoldToken: token,
		SessionID: row.SessionID.String(),
		State:     state,
		Step:      step,
		SeatKeys:  seatKeys,
		ExpiresAt: row.ExpiresAt.UTC().Format(time.RFC3339),
		ExtendAvailable: open && state != "expired" && row.ClosingStartedAt == nil && left > 0 &&
			remaining > 0 && remaining <= holdExtendWindow,
		ExtensionsLeft: left,
		Policy: holdPolicyAnswer{
			SelectSeconds:  int64(pol.Select / time.Second),
			PaymentSeconds: int64(pol.Payment / time.Second),
			ExtendSeconds:  int64(pol.Extend / time.Second),
			ExtendCount:    pol.ExtendCount,
		},
	}
}

// holdRefusal is a planned answer other than success, returned from inside a
// hold transaction so it rolls back before the answer is written.
type holdRefusal struct {
	status  int
	code    string
	msg     string
	details map[string]any
	result  string
	// write, when set, writes the answer instead (category gate refusals).
	write func(w http.ResponseWriter, r *http.Request)
	// retryAfter, when > 0, is sent as Retry-After.
	retryAfter int
}

func (e *holdRefusal) Error() string { return "hold refused: " + e.code }

func refuse(status int, code, msg, result string, details map[string]any) *holdRefusal {
	return &holdRefusal{status: status, code: code, msg: msg, result: result, details: details}
}

var (
	errHoldNotFound = refuse(http.StatusNotFound, "hold.not_found",
		"We cannot find this hold.", observability.HoldResultNotFound, nil)
	errHoldExpired = refuse(http.StatusConflict, "hold.expired",
		"Your hold has ended. Hold the seats again.", observability.HoldResultExpired, nil)
	errHoldInPayment = refuse(http.StatusConflict, "hold.in_payment",
		"To change seats, go back to the seating plan.", observability.HoldResultInPayment, nil)
)

// writeHoldRefusal writes a refusal and reports whether err was one.
func (hh *HoldsHandler) writeHoldRefusal(w http.ResponseWriter, r *http.Request, err error) (string, bool) {
	var ref *holdRefusal
	if !errors.As(err, &ref) {
		return "", false
	}
	if ref.write != nil {
		ref.write(w, r)
		return ref.result, true
	}
	if ref.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ref.retryAfter))
	}
	if ref.details != nil {
		httputil.WriteJSON(w, ref.status, httputil.ErrorEnvelopeWithDetails(ref.code, ref.msg, r, ref.details))
	} else {
		httputil.WriteJSON(w, ref.status, httputil.ErrorEnvelope(ref.code, ref.msg, r))
	}
	return ref.result, true
}

// writeHoldFailure writes a refusal, or a 503 for anything else (never 502:
// Cloudflare replaces it, spec 67 section 4).
func (hh *HoldsHandler) writeHoldFailure(w http.ResponseWriter, r *http.Request, op string, err error) string {
	if result, ok := hh.writeHoldRefusal(w, r, err); ok {
		return result
	}
	if errors.Is(err, context.Canceled) {
		return observability.HoldResultError
	}
	hh.h.logger.Error("public_holds: "+op+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
		"hold.failed", "We could not select the seat. Try again.", r,
	))
	return observability.HoldResultError
}

// seatKeysOf lists the seat keys of seat rows.
func seatKeysOf(rows []gen.SessionSeatRow) []string {
	out := make([]string, 0, len(rows))
	for _, s := range rows {
		out = append(out, s.SeatKey)
	}
	return out
}

// readHoldSeats lists the seats of an open hold (its links), or the seats a
// closed hold held last.
func readHoldSeats(ctx context.Context, q *gen.Queries, row gen.PublicHoldRow) ([]string, error) {
	if !holdOpen(row.State) {
		return holdSeatKeysOf(row.HoldPolicy), nil
	}
	seats, err := q.ListReservationSeats(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return seatKeysOf(seats), nil
}
