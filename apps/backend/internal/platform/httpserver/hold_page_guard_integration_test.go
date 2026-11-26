//go:build integration

// hold_page_guard_integration_test.go — review findings of spec 67 stage T5,
// through the real router with only Stripe stubbed:
//
//   - the hosted fallback whose new page is created while hold.close_payment
//     claims the hold answers 409 hold.expired, records nothing and expires
//     the new page at Stripe; no page is created in the last minute;
//   - /paying, extend and hold.close_payment's Release on one hold at the
//     same time, 20 rounds: one lock order, so no deadlock reaches a caller
//     and no 5xx.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdclose"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
)

func TestHoldCheckout_FallbackRacingClaimedCloseIs409(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newSeatHoldFixture(t, ctx, pool, "hfr")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := buildEmbeddedServer(t, pool, stripe.baseURL(), true)

	code, out, body := postHoldStart(t, srv, f, map[string]any{
		"capabilities": []string{"stripe_elements"},
		"buyer":        map[string]any{"email": f.buyerMail, "name": "Fallback Hold Buyer", "phone": f.buyerPhone},
	})
	if code != http.StatusCreated || out["payment"] == nil {
		t.Fatalf("embedded start with hold_token = %d %s", code, body)
	}
	token, _ := out["checkout_token"].(string)
	csID, _ := out["checkout_session"].(map[string]any)["id"].(string)
	intents := intentsOf(t, ctx, pool, csID)
	if len(intents) != 1 || intents[0].ProviderPaymentID == nil {
		t.Fatalf("intents after start = %+v", intents)
	}
	embedded := *intents[0].ProviderPaymentID
	if page := pageExpiresOf(t, ctx, pool, csID); page == nil {
		t.Fatal("the embedded attempt has no page_expires_at")
	}

	// Less than a minute left on the hold: no new page at all.
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '30 seconds' WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}
	created := len(stripe.sessions)
	if code, out := postHostedFallback(t, srv, token); code != http.StatusConflict || prepErrCode(out) != "hold.expired" {
		t.Fatalf("fallback in the last minute = %d %v, want 409 hold.expired", code, out)
	}
	if len(stripe.sessions) != created || len(stripe.expired()) != 0 {
		t.Fatalf("the last-minute fallback reached Stripe: %d sessions, expired %v", len(stripe.sessions)-created, stripe.expired())
	}
	if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '10 minutes' WHERE id = $1`, f.reservationID); err != nil {
		t.Fatal(err)
	}

	// hold.close_payment claims the hold while Stripe creates the hosted page.
	var hosted string
	stripe.onHostedCreate = func(id string) {
		hosted = id
		if _, err := pool.Exec(context.Background(), `UPDATE reservations SET closing_started_at = now() WHERE id = $1`, f.reservationID); err != nil {
			t.Errorf("claim the hold: %v", err)
		}
	}
	code, out = postHostedFallback(t, srv, token)
	stripe.onHostedCreate = nil
	if code != http.StatusConflict || prepErrCode(out) != "hold.expired" {
		t.Fatalf("fallback racing the close = %d %v, want 409 hold.expired", code, out)
	}
	if hosted == "" {
		t.Fatal("no hosted page was created")
	}
	expired := map[string]bool{}
	for _, id := range stripe.expired() {
		expired[id] = true
	}
	if !expired[embedded] || !expired[hosted] {
		t.Fatalf("expired at Stripe = %v, want the embedded %s and the new hosted %s", stripe.expired(), embedded, hosted)
	}
	if _, err := gen.New(pool).GetPaymentIntentByProviderID(ctx, hosted); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("the refused hosted page was recorded: %v", err)
	}
	for _, pi := range intentsOf(t, ctx, pool, csID) {
		if pi.FailureCode != nil && *pi.FailureCode == "superseded" {
			t.Fatalf("the embedded attempt was marked superseded by a page that was refused")
		}
	}
}

// holdLockOrderServer is the embedded server with the hold routes on.
func holdLockOrderServer(t *testing.T, pool *pgxpool.Pool, stripeBase string) *Server {
	t.Helper()
	return buildHostedCheckoutServer(t, pool, stripeBase, func(c *config.Config) {
		c.StripeElementsEnabled = true
		c.APIPublicURL = embeddedAPIBase
		c.HoldOnClickEnabled = true
		c.HoldVisitorKey = phVisitorKey
		c.PublicFeedTokenRateLimit = 100000
		c.PublicCheckoutTokenRateLimit = 100000
		c.PublicAPIIPRateLimit = 100000
	})
}

// newRoundHold holds S-A-1 and S-A-2 again as a hold of the click and makes it
// the fixture's hold.
func (f *seatHoldFixture) newRoundHold(t *testing.T, ctx context.Context) {
	t.Helper()
	q := gen.New(f.pool)
	res, err := hcheckout.CreateSeatedHold(ctx, f.pool, q, hcheckout.SeatedHoldInput{
		OrgID: f.orgID, ChannelID: f.channelID, SessionID: f.sessionID,
		SeatKeys: []string{"S-A-1", "S-A-2"}, ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create hold: %v", err)
	}
	token := "ht_" + uuid.NewString()
	policy, _ := json.Marshal(holdpolicy.Default())
	if _, err := f.pool.Exec(ctx, `UPDATE reservations
		SET hold_token_hash = $2, select_deadline = expires_at, hold_policy = $3::jsonb
		WHERE id = $1`, res.Reservation.ID, holdpolicy.HashHoldToken(token), string(policy)); err != nil {
		t.Fatalf("mark hold: %v", err)
	}
	f.holdToken, f.reservationID = token, res.Reservation.ID
}

// claimForClose writes the closing mark as hold.close_payment's claim does,
// without its "past expires_at" condition: the race is with a live hold.
func claimForClose(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (time.Time, bool, error) {
	var mark time.Time
	err := pool.QueryRow(ctx, `UPDATE reservations SET closing_started_at = now()
		WHERE id = $1 AND state IN ('draft', 'active') AND closing_started_at IS NULL
		  AND (paying_until IS NULL OR paying_until <= now())
		RETURNING closing_started_at`, id).Scan(&mark)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return mark, err == nil, err
}

func TestHoldLockOrder_PayingExtendReleaseConcurrent(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newSeatHoldFixture(t, ctx, pool, "hlo")
	defer f.cleanup()
	stripe := newStubStripe(t)
	srv := holdLockOrderServer(t, pool, stripe.baseURL())
	store := holdclose.NewPGStore(pool)
	providers := []string{"stripe"}

	releasedRounds, payingOK, extendOK := 0, 0, 0
	for round := 0; round < 20; round++ {
		if round > 0 {
			f.newRoundHold(t, ctx)
		}
		code, _, body := postHoldStart(t, srv, f, preparedHold)
		if code != http.StatusCreated {
			t.Fatalf("round %d: prepared start = %d %s", round, code, body)
		}
		var start prepareAnswer
		if err := json.Unmarshal(body, &start); err != nil {
			t.Fatal(err)
		}
		// Inside the extend window, so extend really writes.
		if _, err := pool.Exec(ctx, `UPDATE reservations SET expires_at = now() + interval '50 seconds' WHERE id = $1`, f.reservationID); err != nil {
			t.Fatal(err)
		}
		listed, err := store.OpenIntents(ctx, f.reservationID, providers)
		if err != nil || len(listed) != 1 {
			t.Fatalf("round %d: open intents = %v, %v", round, listed, err)
		}
		ids := []uuid.UUID{listed[0].PaymentIntentID}

		var (
			wg                     sync.WaitGroup
			gate                   = make(chan struct{})
			payingCode, extendCode int
			released               bool
			releaseErr             error
		)
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-gate
			payingCode, _, _ = postJSON(t, srv, "/v1/public/checkout/"+start.CheckoutToken+"/paying", nil)
		}()
		go func() {
			defer wg.Done()
			<-gate
			extendCode, _, _ = phCall(t, srv, http.MethodPost, "/v1/public/holds/"+f.holdToken+"/extend", nil, "")
		}()
		go func() {
			defer wg.Done()
			<-gate
			// A different head start each round, so the close sometimes
			// lands before and sometimes after the routes.
			time.Sleep(time.Duration(round%5) * 2 * time.Millisecond)
			mark, ok, err := claimForClose(ctx, pool, f.reservationID)
			if err != nil {
				releaseErr = err
				return
			}
			if !ok {
				return
			}
			released, releaseErr = store.Release(ctx, f.reservationID, mark, ids)
			if releaseErr == nil && !released {
				// Not released (a /paying won): take the claim back as the job does.
				releaseErr = gen.New(pool).ClearHoldCloseMark(ctx, f.reservationID, mark)
			}
		}()
		close(gate)
		wg.Wait()

		if payingCode != http.StatusOK && payingCode != http.StatusConflict {
			t.Fatalf("round %d: /paying = %d, want 200 or 409", round, payingCode)
		}
		if extendCode != http.StatusOK && extendCode != http.StatusConflict {
			t.Fatalf("round %d: extend = %d, want 200 or 409", round, extendCode)
		}
		if payingCode == http.StatusOK {
			payingOK++
		}
		if extendCode == http.StatusOK {
			extendOK++
		}
		if releaseErr != nil {
			if txretry.IsSerializationFailure(releaseErr) {
				t.Fatalf("round %d: a deadlock reached Release: %v", round, releaseErr)
			}
			t.Fatalf("round %d: release: %v", round, releaseErr)
		}

		var state, orderStatus string
		var held int
		if err := pool.QueryRow(ctx, `SELECT state FROM reservations WHERE id = $1`, f.reservationID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE checkout_session_id = $1`, uuid.MustParse(start.CheckoutSess.ID)).Scan(&orderStatus); err != nil {
			t.Fatal(err)
		}
		held = f.seatsOfHold(t, ctx)
		if released {
			releasedRounds++
			if state != "expired" || orderStatus != "expired" || held != 0 {
				t.Fatalf("round %d: released, but hold %s order %s seats held %d", round, state, orderStatus, held)
			}
		} else if (state != "draft" && state != "active") || orderStatus != "pending_payment" || held != 2 {
			t.Fatalf("round %d: kept, but hold %s order %s seats held %d", round, state, orderStatus, held)
		}

		// End the round: the hold is closed for the next one.
		if !released {
			if _, err := pool.Exec(ctx, `UPDATE reservations SET paying_until = NULL WHERE id = $1`, f.reservationID); err != nil {
				t.Fatal(err)
			}
			mark, ok, err := claimForClose(ctx, pool, f.reservationID)
			if err != nil || !ok {
				t.Fatalf("round %d: closing claim: ok=%v err=%v", round, ok, err)
			}
			done, err := store.Release(ctx, f.reservationID, mark, ids)
			if err != nil || !done {
				t.Fatalf("round %d: closing release: %v %v", round, done, err)
			}
		}
	}
	t.Logf("of 20 rounds: released by the racing close %d, /paying 200 %d, extend 200 %d",
		releasedRounds, payingOK, extendOK)
}
