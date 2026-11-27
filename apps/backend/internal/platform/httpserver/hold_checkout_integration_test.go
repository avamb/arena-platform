//go:build integration

// hold_checkout_integration_test.go — spec 67 sections 4.4, 4.5, 4.8: a seat
// hold taken at the click becomes the order, through the real router with
// only Stripe stubbed:
//
//   - checkout/start with hold_token (prepared and classic) takes the seats
//     from the hold: no new hold, never reservation.seats_conflict; the
//     payment step is given once (expires_at = now + payment), a reopened
//     order keeps it;
//   - the Stripe page of a 15-minute payment step lives at least 30 minutes
//     and its expiry is written to payment_intents.page_expires_at;
//   - "Pay" (attach) is taken 30 s before the hold ends (no grace);
//   - /paying writes paying_until once and moves the order's expiry;
//   - cancel with keep_hold expires Stripe first and keeps the seats;
//   - a closing or expired hold is 409 hold.expired, a second open order 409
//     hold.in_payment, hold_token with seats 422 checkout.hold_and_seats.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// seatHoldFixture turns the embedded fixture's session into a seated one
// with three seats of the fixture's tier and holds two of them as a hold of
// the click (hold key, policy snapshot, select step).
type seatHoldFixture struct {
	*hostedOrgFixture
	holdToken     string
	reservationID uuid.UUID
	planID        uuid.UUID
	versionID     uuid.UUID
}

// cleanup unbinds and deletes the plan first, then runs the org fixture's
// cleanup (which deletes the venue the plan references).
func (f *seatHoldFixture) cleanup() {
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET admission_mode = 'general_admission', seating_plan_version_id = NULL
		WHERE id = $1`, f.sessionID); err != nil {
		f.t.Logf("seatHoldFixture cleanup: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM seating_plan_versions WHERE id = $1`, f.versionID); err != nil {
		f.t.Logf("seatHoldFixture cleanup version: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM seating_plans WHERE id = $1`, f.planID); err != nil {
		f.t.Logf("seatHoldFixture cleanup plan: %v", err)
	}
	f.hostedOrgFixture.cleanup()
}

func newSeatHoldFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) *seatHoldFixture {
	t.Helper()
	base, _ := embeddedFixture(t, ctx, pool, label)
	f := &seatHoldFixture{hostedOrgFixture: base, planID: uuid.New(), versionID: uuid.New()}
	fail := func(step string, err error) {
		f.cleanup()
		t.Fatalf("seatHoldFixture %s: %v", step, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM session_seats WHERE session_id = $1`, f.sessionID); err != nil {
		fail("drop GA places", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status)
		VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`, f.planID, f.venueID, f.orgID, "Hold Checkout Plan "+label); err != nil {
		fail("plan", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
		VALUES ($1, $2, 1, '{}'::jsonb, $3, 3)`, f.versionID, f.planID, "hold-checkout-"+f.versionID.String()[:8]); err != nil {
		fail("plan version", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET admission_mode = 'assigned_seats', seating_plan_version_id = $2 WHERE id = $1`,
		f.sessionID, f.versionID); err != nil {
		fail("seated session", err)
	}
	q := gen.New(pool)
	for _, n := range []string{"1", "2", "3"} {
		if _, err := q.InsertSessionSeat(ctx, f.sessionID, "S-A-"+n, "S", "A", n, &f.tierID); err != nil {
			fail("insert seat", err)
		}
	}
	res, err := hcheckout.CreateSeatedHold(ctx, pool, q, hcheckout.SeatedHoldInput{
		OrgID: f.orgID, ChannelID: f.channelID, SessionID: f.sessionID,
		SeatKeys: []string{"S-A-1", "S-A-2"}, ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	})
	if err != nil {
		fail("create hold", err)
	}
	token := "ht_" + uuid.NewString()
	policy, _ := json.Marshal(holdpolicy.Default())
	if _, err := pool.Exec(ctx, `UPDATE reservations
		SET hold_token_hash = $2, select_deadline = expires_at, hold_policy = $3::jsonb
		WHERE id = $1`, res.Reservation.ID, holdpolicy.HashHoldToken(token), string(policy)); err != nil {
		fail("mark hold", err)
	}
	f.holdToken, f.reservationID = token, res.Reservation.ID
	return f
}

type holdRow struct {
	state       string
	expiresAt   time.Time
	started     *time.Time
	payingUntil *time.Time
}

func (f *seatHoldFixture) hold(t *testing.T, ctx context.Context) holdRow {
	t.Helper()
	var h holdRow
	if err := f.pool.QueryRow(ctx, `SELECT state, expires_at, payment_started_at, paying_until FROM reservations WHERE id = $1`,
		f.reservationID).Scan(&h.state, &h.expiresAt, &h.started, &h.payingUntil); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	return h
}

func (f *seatHoldFixture) holdsOfSession(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM reservations WHERE session_id = $1`, f.sessionID).Scan(&n); err != nil {
		t.Fatalf("count holds: %v", err)
	}
	return n
}

func (f *seatHoldFixture) seatsOfHold(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_seats WHERE session_id = $1 AND status = 'held' AND reservation_id = $2`,
		f.sessionID, f.reservationID).Scan(&n); err != nil {
		t.Fatalf("count seats of the hold: %v", err)
	}
	return n
}

func pageExpiresOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, csID string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := pool.QueryRow(ctx, `SELECT page_expires_at FROM payment_intents WHERE checkout_session_id = $1
		ORDER BY created_at DESC, id DESC LIMIT 1`, uuid.MustParse(csID)).Scan(&at); err != nil {
		t.Fatalf("read page_expires_at: %v", err)
	}
	return at
}

// postHoldStart sends checkout/start for the fixture's hold; extra is merged
// in (a nil value deletes the key).
func postHoldStart(t *testing.T, srv *Server, f *seatHoldFixture, extra map[string]any) (int, map[string]any, []byte) {
	t.Helper()
	payload := map[string]any{
		"session_id": f.sessionID.String(),
		"hold_token": f.holdToken,
		"return_url": hostedTicketsBaseURL + "/shows/hold",
		// Every paid sale needs the tick on the sale terms (spec 68 5.1).
		"consent": map[string]any{"accepted": true},
	}
	for k, v := range extra {
		if v == nil {
			delete(payload, k)
			continue
		}
		payload[k] = v
	}
	// A prepared order has no buyer yet: the tick comes at attach.
	if payload["prepare"] == true {
		delete(payload, "consent")
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.Bytes()
}

var preparedHold = map[string]any{"prepare": true, "capabilities": []string{"stripe_elements"}}

func holdNear(a, b time.Time, slack time.Duration) bool {
	d := a.Sub(b)
	return d < slack && d > -slack
}

func TestHoldCheckout_PreparedPathKeepsTheHold(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newSeatHoldFixture(t, ctx, pool, "hcp")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	// Seats together with the hold key are refused before anything is read.
	if code, out, _ := postHoldStart(t, srv, f, map[string]any{"prepare": true, "capabilities": []string{"stripe_elements"}, "seats": []string{"S-A-1"}}); code != http.StatusUnprocessableEntity || prepErrCode(out) != "checkout.hold_and_seats" {
		t.Fatalf("hold_token with seats = %d %v", code, out)
	}
	// The old path on the same seats is a seat conflict; the hold path below
	// cannot be one.
	if code, out, _ := postHoldStart(t, srv, f, map[string]any{"prepare": true, "capabilities": []string{"stripe_elements"}, "hold_token": nil, "seats": []string{"S-A-1"}}); code != http.StatusConflict || prepErrCode(out) != "reservation.seats_conflict" {
		t.Fatalf("old path on held seats = %d %v", code, out)
	}

	before := time.Now().UTC()
	code, out, body := postHoldStart(t, srv, f, preparedHold)
	if code != http.StatusCreated || out["prepared"] != true {
		t.Fatalf("prepare with hold_token = %d %s", code, body)
	}
	var start prepareAnswer
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatal(err)
	}
	if n := f.holdsOfSession(t, ctx); n != 1 {
		t.Fatalf("holds of the session = %d, want 1 (no new hold)", n)
	}
	if n := f.seatsOfHold(t, ctx); n != 2 {
		t.Fatalf("seats of the hold = %d, want 2", n)
	}
	h := f.hold(t, ctx)
	if h.started == nil || !holdNear(h.expiresAt, before.Add(15*time.Minute), time.Minute) {
		t.Fatalf("payment step: started %v, expires %v, want about now + 15 min", h.started, h.expiresAt)
	}
	if start.ExpiresAt != h.expiresAt.UTC().Format(time.RFC3339) || start.PaymentExpiresAt != start.ExpiresAt {
		t.Errorf("answer expires_at %s payment_expires_at %s, want both %s (no grace)", start.ExpiresAt, start.PaymentExpiresAt, h.expiresAt.UTC().Format(time.RFC3339))
	}
	ord := orderOf(t, ctx, pool, start.CheckoutSess.ID)
	if ord.ExpiresAt == nil || !ord.ExpiresAt.Equal(h.expiresAt) {
		t.Errorf("order expires %v, hold %v", ord.ExpiresAt, h.expiresAt)
	}
	// The Stripe page of a 15-minute step lives at least 30 minutes.
	stripeExp, _ := strconv.ParseInt(stripe.lastForm(t).Get("expires_at"), 10, 64)
	if life := time.Unix(stripeExp, 0).Sub(before); life < 30*time.Minute {
		t.Errorf("Stripe page lives %v, want >= 30 min", life)
	}
	page := pageExpiresOf(t, ctx, pool, start.CheckoutSess.ID)
	if page == nil || page.Unix() != stripeExp {
		t.Errorf("page_expires_at = %v, want the Stripe expiry %d", page, stripeExp)
	}

	// A second order of the same hold while this one is open.
	if code, out, _ := postHoldStart(t, srv, f, preparedHold); code != http.StatusConflict || prepErrCode(out) != "hold.in_payment" {
		t.Fatalf("second order of the hold = %d %v", code, out)
	}

	// "Pay" 30 s before the hold ends: taken (no grace on this path).
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '30 seconds' WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}
	buyer := map[string]any{"email": f.buyerMail, "name": "Hold Buyer", "phone": f.buyerPhone}
	if code, out := attachBuyer(t, srv, start.CheckoutToken, buyer, nil); code != http.StatusOK || out["status"] != "pending" {
		t.Fatalf("attach 30 s before the hold ends = %d %v", code, out)
	}

	// The payment is in progress: paying_until once, the order moves. The
	// hold gets a few minutes back so a slow host cannot let it end first.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '5 minutes' WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}
	payPath := "/v1/public/checkout/" + start.CheckoutToken + "/paying"
	code, out, _ = postJSON(t, srv, payPath, nil)
	if code != http.StatusOK || out["paying_until"] == nil {
		t.Fatalf("/paying = %d %v", code, out)
	}
	h = f.hold(t, ctx)
	if h.payingUntil == nil || !holdNear(*h.payingUntil, time.Now().Add(10*time.Minute), time.Minute) {
		t.Fatalf("paying_until = %v, want about now + 10 min", h.payingUntil)
	}
	ord = orderOf(t, ctx, pool, start.CheckoutSess.ID)
	if ord.ExpiresAt == nil || ord.ExpiresAt.Before(*h.payingUntil) {
		t.Errorf("order expires %v, before paying_until %v", ord.ExpiresAt, h.payingUntil)
	}
	first := *h.payingUntil
	if code, out, _ := postJSON(t, srv, payPath, nil); code != http.StatusOK || out["paying_until"] != first.UTC().Format(time.RFC3339) {
		t.Fatalf("repeat /paying = %d %v, want the same paying_until %s", code, out, first.UTC().Format(time.RFC3339))
	}

	// Cancel keeping the hold: Stripe first, the seats stay, expires_at too.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '8 minutes' WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}
	kept := f.hold(t, ctx).expiresAt
	cs := *intentsOf(t, ctx, pool, start.CheckoutSess.ID)[0].ProviderPaymentID
	code, out, _ = postJSON(t, srv, "/v1/public/checkout/"+start.CheckoutToken+"/cancel", map[string]any{"keep_hold": true})
	if code != http.StatusOK || out["cancelled"] != true || out["hold_kept"] != true {
		t.Fatalf("cancel keep_hold = %d %v", code, out)
	}
	if ex := stripe.expired(); len(ex) != 1 || ex[0] != cs {
		t.Fatalf("expired at Stripe = %v, want [%s]", ex, cs)
	}
	if n := f.seatsOfHold(t, ctx); n != 2 {
		t.Fatalf("seats of the hold after keep_hold = %d, want 2", n)
	}
	h = f.hold(t, ctx)
	if h.state != "draft" || !h.expiresAt.Equal(kept) {
		t.Fatalf("hold after keep_hold = %s expires %v, want draft expires %v", h.state, h.expiresAt, kept)
	}
	if o := orderOf(t, ctx, pool, start.CheckoutSess.ID); o.Status != "cancelled" {
		t.Errorf("order after keep_hold = %q, want cancelled", o.Status)
	}

	// Reopen: a new order on the same hold keeps expires_at.
	code, _, body = postHoldStart(t, srv, f, preparedHold)
	if code != http.StatusCreated {
		t.Fatalf("reopen = %d %s", code, body)
	}
	var again prepareAnswer
	_ = json.Unmarshal(body, &again)
	if h := f.hold(t, ctx); !h.expiresAt.Equal(kept) {
		t.Fatalf("reopen moved expires_at %v -> %v", kept, h.expiresAt)
	}
	if n := f.holdsOfSession(t, ctx); n != 1 {
		t.Fatalf("holds after reopen = %d, want 1", n)
	}

	// Closing: /paying and attach refused.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET closing_started_at = now() WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := postJSON(t, srv, "/v1/public/checkout/"+again.CheckoutToken+"/paying", nil); code != http.StatusConflict || prepErrCode(out) != "hold.expired" {
		t.Fatalf("/paying while closing = %d %v", code, out)
	}
	if code, out := attachBuyer(t, srv, again.CheckoutToken, buyer, nil); code != http.StatusConflict || prepErrCode(out) != "checkout.not_payable" {
		t.Fatalf("attach while closing = %d %v", code, out)
	}
	if _, err := pool.Exec(ctx, `UPDATE reservations SET closing_started_at = NULL WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}

	// Expired: no new order.
	if code, out, _ := postJSON(t, srv, "/v1/public/checkout/"+again.CheckoutToken+"/cancel", map[string]any{"keep_hold": true}); code != http.StatusOK || out["hold_kept"] != true {
		t.Fatalf("second keep_hold cancel = %d %v", code, out)
	}
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() - interval '1 second' WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := postHoldStart(t, srv, f, preparedHold); code != http.StatusConflict || prepErrCode(out) != "hold.expired" {
		t.Fatalf("start on an expired hold = %d %v", code, out)
	}
	if code, out, _ := postHoldStart(t, srv, f, map[string]any{"prepare": true, "capabilities": []string{"stripe_elements"}, "hold_token": "ht_unknown_" + uuid.NewString()}); code != http.StatusNotFound || prepErrCode(out) != "hold.not_found" {
		t.Fatalf("unknown hold = %d %v", code, out)
	}
}

func TestHoldCheckout_ClassicPathHostedPage(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newSeatHoldFixture(t, ctx, pool, "hcc")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	before := time.Now().UTC()
	code, out, body := postHoldStart(t, srv, f, map[string]any{
		"buyer": map[string]any{"email": f.buyerMail, "name": "Classic Hold Buyer", "phone": f.buyerPhone},
	})
	if code != http.StatusCreated || out["redirect_url"] == "" || out["redirect_url"] == nil {
		t.Fatalf("classic start with hold_token = %d %s", code, body)
	}
	if n := f.holdsOfSession(t, ctx); n != 1 {
		t.Fatalf("holds of the session = %d, want 1 (no new hold)", n)
	}
	if n := f.seatsOfHold(t, ctx); n != 2 {
		t.Fatalf("seats of the hold = %d, want 2", n)
	}
	h := f.hold(t, ctx)
	if h.started == nil || !holdNear(h.expiresAt, before.Add(15*time.Minute), time.Minute) {
		t.Fatalf("payment step: started %v, expires %v", h.started, h.expiresAt)
	}
	if out["payment_expires_at"] != h.expiresAt.UTC().Format(time.RFC3339) {
		t.Errorf("payment_expires_at = %v, want the hold's %s", out["payment_expires_at"], h.expiresAt.UTC().Format(time.RFC3339))
	}
	form := stripe.lastForm(t)
	if form.Get("ui_mode") == "elements" {
		t.Fatalf("an older widget got the embedded form: %v", form)
	}
	stripeExp, _ := strconv.ParseInt(form.Get("expires_at"), 10, 64)
	if life := time.Unix(stripeExp, 0).Sub(before); life < 30*time.Minute {
		t.Errorf("hosted Stripe page lives %v, want >= 30 min", life)
	}
	csID, _ := out["checkout_session"].(map[string]any)["id"].(string)
	if page := pageExpiresOf(t, ctx, pool, csID); page == nil || page.Unix() != stripeExp {
		t.Errorf("page_expires_at = %v, want %d", page, stripeExp)
	}
	ord := orderOf(t, ctx, pool, csID)
	if ord.ExpiresAt == nil || !ord.ExpiresAt.Equal(h.expiresAt) || ord.Status != "pending_payment" {
		t.Errorf("order = %s expires %v, hold %v", ord.Status, ord.ExpiresAt, h.expiresAt)
	}
	// The status counts down to the hold, without the grace.
	token, _ := out["checkout_token"].(string)
	req := httptest.NewRequest(http.MethodGet, "/v1/public/checkout/"+token, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var st map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != http.StatusOK || st["payment_expires_at"] != h.expiresAt.UTC().Format(time.RFC3339) {
		t.Errorf("status = %d payment_expires_at %v, want %s", rec.Code, st["payment_expires_at"], h.expiresAt.UTC().Format(time.RFC3339))
	}
	// /paying moves the order's expiry.
	code, out, _ = postJSON(t, srv, "/v1/public/checkout/"+token+"/paying", nil)
	if code != http.StatusOK || out["paying_until"] == nil {
		t.Fatalf("/paying = %d %v", code, out)
	}
	h = f.hold(t, ctx)
	ord = orderOf(t, ctx, pool, csID)
	if h.payingUntil == nil || ord.ExpiresAt == nil || ord.ExpiresAt.Before(*h.payingUntil) {
		t.Errorf("order expires %v, paying_until %v", ord.ExpiresAt, h.payingUntil)
	}
}
