//go:build integration

// promo_0108_test.go — the promo changes of plan 25 / migration 0108 on the
// live wire: CHECK_KDP quotes a cart that does not exist yet, a session-scoped
// code refuses another session, and a used-up code is refused when typed.
//
// Runs against a real server over the seeded database, like promo_491_test.go
// next door. The seeded code is WAVE1, 10 % off, unrestricted; the assigned
// session's seats cost CZK 500 on a 5 % channel.

package compat_bil24_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

func TestCompatBil24_0108_PromoQuoteScopeAndLimits(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()

	actionEventID := mustActionEventID(t, st, st.AssignedSessID)
	tierWireID := sc6TierWireID(t, st, st.AssignedTierID)
	sess, user := createGatewayUser(t, base, st, "harness-0108@example.test")

	kdp := func(code string, extra map[string]any) map[string]interface{} {
		t.Helper()
		body := map[string]any{
			"command":   "CHECK_KDP",
			"fid":       st.ChannelFID,
			"token":     st.ChannelToken,
			"locale":    "en-US",
			"userId":    user,
			"sessionId": sess,
			"promoCode": code,
		}
		for k, v := range extra {
			body[k] = v
		}
		return postBil24(t, base, body)
	}
	quoteLines := map[string]any{
		"actionEventId": actionEventID,
		"lines": []any{map[string]any{
			"categoryPriceId": strconv.FormatInt(tierWireID, 10),
			"quantity":        2,
		}},
	}

	// ── 1. The quote: two CZK 500 seats, 10 % off, 5 % fee on the net ────────
	q := kdp("wave1", quoteLines)
	if code := numberField(t, q, "resultCode"); code != 0 {
		t.Fatalf("CHECK_KDP quote resultCode = %v, want 0 (description %v)", code, q["description"])
	}
	if applied, _ := q["promoApplied"].(bool); !applied {
		t.Fatalf("promoApplied = %v, want true", q["promoApplied"])
	}
	for _, tc := range []struct {
		key  string
		want float64
	}{
		{"sum", 1000},
		{"discountAmount", 100},
		{"chargeAmount", 45}, // 5 % of the NET 900
		{"totalSum", 945},
	} {
		if got := numberField(t, q, tc.key); got != tc.want {
			t.Errorf("quote %s = %v, want %v", tc.key, got, tc.want)
		}
	}
	lines, ok := q["lines"].([]interface{})
	if !ok || len(lines) != 1 {
		t.Fatalf("quote lines = %#v, want one", q["lines"])
	}
	line, _ := lines[0].(map[string]interface{})
	if got := numberField(t, line, "discount"); got != 100 {
		t.Errorf("lines[0].discount = %v, want the whole 100", got)
	}
	if got := numberField(t, line, "sum"); got != 900 {
		t.Errorf("lines[0].sum = %v, want 900", got)
	}

	// Nothing was held or stored by the quote.
	var holds int
	if err := st.Pool.QueryRow(ctx,
		`SELECT count(*) FROM reservations r JOIN gateway_sessions g ON g.id = r.gateway_session_id
		  WHERE g.session_token = $1`, sess).Scan(&holds); err == nil && holds != 0 {
		t.Errorf("the quote must not hold anything: %d reservations", holds)
	}

	// ── 2. A code for another session does not apply to this one ────────────
	var otherID uuid.UUID
	if err := st.Pool.QueryRow(ctx,
		`INSERT INTO promo_codes (org_id, code, discount_type, discount_value, applies_to_session_ids)
		 VALUES ($1, 'ELSEWHERE', 'percent', 50, ARRAY[$2::uuid]) RETURNING id`,
		st.OrgID, uuid.New()).Scan(&otherID); err != nil {
		t.Fatalf("seed session-scoped code: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(), `DELETE FROM promo_codes WHERE id = $1`, otherID)
	})
	scoped := kdp("elsewhere", quoteLines)
	if code := numberField(t, scoped, "resultCode"); code != 0 {
		t.Fatalf("scoped quote resultCode = %v, want 0 with promoApplied=false", code)
	}
	if applied, _ := scoped["promoApplied"].(bool); applied {
		t.Error("a code for another session must not apply")
	}
	if got := numberField(t, scoped, "totalSum"); got != 1050 {
		t.Errorf("undiscounted totalSum = %v, want 1050", got)
	}
	if desc, _ := scoped["description"].(string); desc == "" {
		t.Error("a refused quote must name its reason in description")
	}

	// ── 3. A used-up code is refused when typed, not after payment ──────────
	if _, err := st.Pool.Exec(ctx, `UPDATE promo_codes SET max_uses = 0 WHERE org_id = $1 AND code = 'WAVE1'`, st.OrgID); err != nil {
		t.Fatalf("cap WAVE1: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool.Exec(context.Background(), `UPDATE promo_codes SET max_uses = NULL WHERE org_id = $1 AND code = 'WAVE1'`, st.OrgID)
	})
	used := kdp("wave1", nil)
	if code := numberField(t, used, "resultCode"); code != 101 {
		t.Errorf("CHECK_KDP on a used-up code resultCode = %v, want 101 (description %v)", code, used["description"])
	}
	usedQuote := kdp("wave1", quoteLines)
	if applied, _ := usedQuote["promoApplied"].(bool); applied {
		t.Error("a used-up code must not apply in a quote either")
	}
}
