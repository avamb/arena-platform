//go:build integration

// promo_attempts_test.go — the journal of promo-code checks (migration 0158)
// against the live gateway: a code that belongs to ANOTHER session is refused
// and the refusal leaves a row naming the cause, an accepted code leaves an
// "applied" row, a repeat inside a minute is stored once, and the read side
// (List, Summarize) tells the operator what happened.

package compat_bil24_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/promoattempts"
)

func TestPromoAttempts_GatewayJournalsWrongSessionAndApplied(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	orgID := uuid.MustParse(st.OrgID)

	// RED30 is valid only for a session this cart does not hold.
	if _, err := st.Pool.Exec(ctx,
		`INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_session_ids)
		 VALUES ($1, 'RED30', 'percent', 30, ARRAY[$2::uuid])`, orgID, uuid.New()); err != nil {
		t.Fatalf("seed RED30: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(ctx, `DELETE FROM promo_code_attempts WHERE org_id=$1`, orgID)
		_, _ = st.Pool.Exec(ctx, `DELETE FROM promo_codes WHERE org_id=$1 AND code='RED30'`, orgID)
	})

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	labels := sortedSeatLabels(st)
	sess, user := createGatewayUser(t, base, st, "harness-promo-attempts@example.test")
	reserve := postBil24(t, base, map[string]any{
		"command": "RESERVATION", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "ru-RU",
		"type": "RESERVE", "userId": user, "sessionId": sess, "actionEventId": actionEventID,
		"seatList": []any{map[string]any{"seatId": st.SeatIDs[labels[0]]}},
	})
	if code := numberField(t, reserve, "resultCode"); code != 0 {
		t.Fatalf("RESERVE resultCode = %v", code)
	}

	add := func(code string) {
		t.Helper()
		resp := postBil24(t, base, map[string]any{
			"command": "ADD_PROMO_CODES", "fid": st.ChannelFID, "token": st.ChannelToken, "locale": "ru-RU",
			"userId": user, "sessionId": sess, "promoCodeList": []any{code},
		})
		if rc := numberField(t, resp, "resultCode"); rc != 0 {
			t.Fatalf("ADD_PROMO_CODES %s resultCode = %v", code, rc)
		}
	}
	add("red30")  // wrong session
	add("RED30")  // same check again, same cart: collapses
	add("NOPE99") // no such code
	add("WAVE1")  // fits

	waitFor := func(f promoattempts.Filter, want int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, total, err := promoattempts.List(ctx, st.Pool, f, 50, 0)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if total == want || time.Now().After(deadline) {
				if total != want {
					t.Fatalf("attempts for %+v = %d, want %d", f, total, want)
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	waitFor(promoattempts.Filter{OrgID: orgID, Code: "RED30"}, 1)
	items, _, err := promoattempts.List(ctx, st.Pool, promoattempts.Filter{OrgID: orgID, Code: "red30"}, 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("List RED30 = %v, %v", items, err)
	}
	it := items[0]
	if it.Result != promoattempts.ResultRejected || it.Reason == nil || *it.Reason != promoattempts.ReasonWrongSession {
		t.Errorf("RED30 attempt = %s/%v, want rejected/wrong_session", it.Result, it.Reason)
	}
	if it.Surface != promoattempts.SurfaceGatewayAddPromoCodes || it.PromoCodeID == nil || it.GatewaySessionID == nil {
		t.Errorf("RED30 attempt lacks surface, code id or gateway session: %+v", it)
	}
	if len(it.SessionIDs) != 1 || it.SessionIDs[0].String() != st.AssignedSessID {
		t.Errorf("session ids = %v, want the cart's session %s", it.SessionIDs, st.AssignedSessID)
	}

	waitFor(promoattempts.Filter{OrgID: orgID, Code: "NOPE99", Reason: promoattempts.ReasonUnknownCode}, 1)
	waitFor(promoattempts.Filter{OrgID: orgID, Code: "WAVE1", Result: promoattempts.ResultApplied}, 1)

	sum, err := promoattempts.Summarize(ctx, st.Pool, promoattempts.Filter{OrgID: orgID})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if sum.Total != 3 || sum.Applied != 1 || sum.Rejected != 2 || len(sum.TopReasons) != 2 {
		t.Errorf("summary = %+v, want 3 total, 1 applied, 2 rejected, 2 reasons", sum)
	}

	// Another organization sees none of it.
	other, _, err := promoattempts.List(ctx, st.Pool, promoattempts.Filter{OrgID: uuid.New()}, 10, 0)
	if err != nil || len(other) != 0 {
		t.Errorf("foreign org List = %v, %v; want empty", other, err)
	}
}

// Rows older than the retention are removed by the cleanup job; newer stay.
func TestPromoAttempts_PurgeKeepsRecentRows(t *testing.T) {
	st := setupHarness(t)
	ctx := context.Background()
	orgID := uuid.MustParse(st.OrgID)
	t.Cleanup(func() { _, _ = st.Pool.Exec(ctx, `DELETE FROM promo_code_attempts WHERE org_id=$1`, orgID) })
	for _, age := range []string{"100 days", "30 days"} {
		if _, err := st.Pool.Exec(ctx,
			`INSERT INTO promo_code_attempts (org_id, surface, code_entered, result, reason, created_at)
			 VALUES ($1, 'org_validate', 'OLD', 'rejected', 'unknown_code', now() - $2::interval)`, orgID, age); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if _, err := promoattempts.Purge(ctx, st.Pool, time.Now()); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	var left int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM promo_code_attempts WHERE org_id=$1`, orgID).Scan(&left); err != nil || left != 1 {
		t.Fatalf("rows left = %d (%v), want only the 30-day-old one", left, err)
	}
}
