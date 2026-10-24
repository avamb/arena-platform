//go:build integration

// CREATE_ORDER_EXT and the arena `attribution` extension (spec 43 section
// 4.4). A selling site sends the same object the widget sends to
// checkout/start; the gateway cleans it, resolves its ref to a sales link of
// the channel's organization before the order transaction, and writes it on
// the order. A re-send of the same cart with another attribution updates the
// order (last touch wins, the earlier touch is kept as "first"), never -2; a
// re-send without the field leaves it alone.

package compat_bil24_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

func attrHarnessLink(t *testing.T, q *gen.Queries, orgID uuid.UUID, label string) gen.SalesLinkRow {
	t.Helper()
	for i := 0; i < 8; i++ {
		b := make([]byte, attribution.CodeLen)
		if _, err := rand.Read(b); err != nil {
			t.Fatalf("rand: %v", err)
		}
		for j := range b {
			b[j] = attribution.CodeAlphabet[int(b[j])%len(attribution.CodeAlphabet)]
		}
		l, ok, err := q.InsertSalesLinkIfCodeFree(context.Background(), gen.InsertSalesLinkParams{
			OrgID: orgID, Code: string(b), TargetKind: "org_page", Label: label,
		})
		if err != nil {
			t.Fatalf("InsertSalesLinkIfCodeFree: %v", err)
		}
		if ok {
			return l
		}
	}
	t.Fatal("no free sales-link code after 8 draws")
	return gen.SalesLinkRow{}
}

func TestCompatBil24_CreateOrder_Attribution(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)
	ctx := context.Background()
	q := gen.New(st.Pool)
	orgID := uuid.MustParse(st.OrgID)

	linkA := attrHarnessLink(t, q, orgID, "Poster A")
	linkB := attrHarnessLink(t, q, orgID, "Poster B")
	// Registered after setupHarness, so it runs BEFORE the harness sweeps
	// its orders: the orders are unlinked first, then the links go.
	t.Cleanup(func() {
		ids := []uuid.UUID{linkA.ID, linkB.ID}
		_, _ = st.Pool.Exec(context.Background(), `UPDATE orders SET sales_link_id = NULL WHERE sales_link_id = ANY($1::uuid[])`, ids)
		_, _ = st.Pool.Exec(context.Background(), `DELETE FROM sales_links WHERE id = ANY($1::uuid[])`, ids)
	})

	gaActionEventID := mustActionEventID(t, st, st.GAsessID)
	gaTierWireID := sc6TierWireID(t, st, sc2EarlyBirdTierID(t, st))
	sess, user := createGatewayUser(t, base, st, "harness-attr-"+uuid.NewString()[:8]+"@example.test")
	runtime := map[string]string{
		"actionEventId":   strconv.FormatInt(gaActionEventID, 10),
		"categoryPriceId": strconv.FormatInt(gaTierWireID, 10),
		"sessionId":       sess,
	}
	stamp := func(req map[string]interface{}) map[string]interface{} {
		req = resolveGolden(req, st, runtime)
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["userId"] = user
		return req
	}

	reqReserve, _ := loadWPFixture(t, "RESERVATION", "reserve_by_category")
	reqReserve = stamp(reqReserve)
	reqReserve["categoryList"] = []any{map[string]any{
		"categoryPriceId": gaTierWireID,
		"quantity":        sc2Quantity,
		"tariffPlanId":    nil,
	}}
	sc2RequireOK(t, "RESERVATION", postBil24(t, base, reqReserve))

	type stored struct {
		link *uuid.UUID
		attr map[string]any
	}
	read := func(label string, orderID uuid.UUID) stored {
		t.Helper()
		var s stored
		var raw []byte
		if err := st.Pool.QueryRow(ctx, `SELECT sales_link_id, attribution FROM orders WHERE id = $1`, orderID).
			Scan(&s.link, &raw); err != nil {
			t.Fatalf("%s: read order: %v", label, err)
		}
		if err := json.Unmarshal(raw, &s.attr); err != nil {
			t.Fatalf("%s: decode attribution %s: %v", label, raw, err)
		}
		return s
	}
	sendOrder := func(label string, attr any) uuid.UUID {
		t.Helper()
		req, _ := loadWPFixture(t, "CREATE_ORDER_EXT", "ga")
		req = stamp(req)
		req["orderId"] = "wc-attr-" + sess[:8]
		if attr != nil {
			req["attribution"] = attr
		}
		resp := postBil24(t, base, req)
		sc2RequireOK(t, label, resp)
		return sc6OrderID(t, st, label, resp)
	}

	// ── first CREATE_ORDER_EXT came through link A ──────────────────────────
	first := sendOrder("CREATE_ORDER_EXT ref=A", map[string]any{"ref": linkA.Code, "utm_source": "qr"})
	s := read("ref=A", first)
	if s.link == nil || *s.link != linkA.ID {
		t.Fatalf("ref=A: sales_link_id = %v, want %s", s.link, linkA.ID)
	}
	if s.attr["ref"] != linkA.Code || s.attr["utm_source"] != "qr" {
		t.Fatalf("ref=A: attribution = %v", s.attr)
	}
	var csAttr []byte
	if err := st.Pool.QueryRow(ctx, `SELECT cs.attribution FROM orders o JOIN checkout_sessions cs ON cs.id = o.checkout_session_id WHERE o.id = $1`, first).
		Scan(&csAttr); err != nil {
		t.Fatalf("checkout session attribution: %v", err)
	}
	var csMap map[string]any
	if err := json.Unmarshal(csAttr, &csMap); err != nil || csMap["ref"] != linkA.Code {
		t.Fatalf("checkout_sessions.attribution = %s, want ref %s", csAttr, linkA.Code)
	}

	// ── the same cart re-sent through link B: last touch wins, A is first ──
	second := sendOrder("CREATE_ORDER_EXT ref=B", map[string]any{"ref": linkB.Code})
	if second != first {
		t.Fatalf("re-send answered order %s, want the same open order %s", second, first)
	}
	s = read("ref=B", second)
	if s.link == nil || *s.link != linkB.ID {
		t.Fatalf("ref=B: sales_link_id = %v, want %s", s.link, linkB.ID)
	}
	f, _ := s.attr["first"].(map[string]any)
	if s.attr["ref"] != linkB.Code || f == nil || f["ref"] != linkA.Code {
		t.Fatalf("ref=B: attribution = %v, want ref B with first ref A", s.attr)
	}

	// ── re-sent without the field: the order keeps what it has ──────────────
	third := sendOrder("CREATE_ORDER_EXT no attribution", nil)
	if third != first {
		t.Fatalf("re-send answered order %s, want %s", third, first)
	}
	s = read("no attribution", third)
	if s.link == nil || *s.link != linkB.ID || s.attr["ref"] != linkB.Code {
		t.Fatalf("no attribution: link %v attribution %v, want B kept", s.link, s.attr)
	}

	// ── an unusable value is not a refusal either ───────────────────────────
	fourth := sendOrder("CREATE_ORDER_EXT attribution=42", 42)
	if fourth != first {
		t.Fatalf("re-send answered order %s, want %s", fourth, first)
	}
	s = read("attribution=42", fourth)
	if s.link == nil || *s.link != linkB.ID {
		t.Fatalf("attribution=42: link %v, want B kept", s.link)
	}
}
