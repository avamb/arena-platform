//go:build integration

package httpserver

// shop_parity_integration_test.go — the order-list tabs and their counts, the
// internal note of an order and the "send the letter again" action (event
// center parity with the Shopify admin: findings A-08, A-10, A-11), through
// the real router on a live database.
//
//	go test -tags integration -run TestShopParity ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// placedOrder checks out one unit and returns the order id and its public token.
func (f *shopFixture) placedOrder(variantID string) (uuid.UUID, string) {
	f.t.Helper()
	code, body := f.checkout(variantID, 1)
	if code != http.StatusCreated {
		f.t.Fatalf("checkout = %d %v", code, body)
	}
	token := body["order_token"].(string)
	o, err := f.q.GetShopOrderByToken(context.Background(), token)
	if err != nil {
		f.t.Fatalf("order of token: %v", err)
	}
	return o.ID, token
}

func (f *shopFixture) counts(token string, org uuid.UUID) map[string]any {
	f.t.Helper()
	code, body, _ := f.do(http.MethodGet, f.orgPath(org, "/orders/counts"), token, "")
	if code != http.StatusOK {
		f.t.Fatalf("GET counts = %d %v", code, body)
	}
	return body
}

func wantCounts(t *testing.T, got map[string]any, all, toShip, unpaid, shipped, refunds float64) {
	t.Helper()
	want := map[string]float64{"all": all, "to_ship": toShip, "unpaid": unpaid, "shipped": shipped, "refunds": refunds}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("counts = %v; want %v", got, want)
		}
	}
}

func (f *shopFixture) listIDs(query string) []string {
	f.t.Helper()
	code, body, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/orders"+query), f.ownerA, "")
	if code != http.StatusOK {
		f.t.Fatalf("GET orders%s = %d %v", query, code, body)
	}
	var ids []string
	for _, o := range body["orders"].([]any) {
		ids = append(ids, o.(map[string]any)["id"].(string))
	}
	return ids
}

func TestShopParity_OrderTabsAndCounts(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 10)

	unpaid, _ := f.placedOrder(variant)
	paid, paidToken := f.placedOrder(variant)
	f.pay(paidToken)

	wantCounts(t, f.counts(f.ownerA, f.orgA), 2, 1, 1, 0, 0)
	if ids := f.listIDs("?view=to_ship"); len(ids) != 1 || ids[0] != paid.String() {
		t.Fatalf("view=to_ship = %v; want only the paid order %s", ids, paid)
	}
	if ids := f.listIDs("?view=unpaid"); len(ids) != 1 || ids[0] != unpaid.String() {
		t.Fatalf("view=unpaid = %v; want only the unpaid order %s", ids, unpaid)
	}
	if ids := f.listIDs("?view=shipped"); len(ids) != 0 {
		t.Fatalf("view=shipped = %v; want none yet", ids)
	}
	// The status filter still works and composes with a tab.
	if ids := f.listIDs("?status=paid"); len(ids) != 1 || ids[0] != paid.String() {
		t.Fatalf("status=paid = %v", ids)
	}
	if ids := f.listIDs("?status=pending_payment&view=to_ship"); len(ids) != 0 {
		t.Fatalf("status=pending_payment&view=to_ship = %v; want none (both must hold)", ids)
	}
	if code, body, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/orders?view=bogus"), f.ownerA, ""); code != http.StatusBadRequest || shopErrCode(body) != "shop.invalid_filter" {
		t.Fatalf("unknown view = %d %v; want 400 shop.invalid_filter", code, body)
	}

	// An order with nothing to hand over (digital, service) is never "to ship".
	f.exec(`UPDATE shop_orders SET delivery_kind = 'none' WHERE id = $1`, paid)
	wantCounts(t, f.counts(f.ownerA, f.orgA), 2, 0, 1, 0, 0)
	f.exec(`UPDATE shop_orders SET delivery_kind = 'shipping' WHERE id = $1`, paid)

	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+paid.String()+"/ship"), f.ownerA, `{}`); code != http.StatusOK {
		t.Fatalf("ship = %d %v", code, body)
	}
	wantCounts(t, f.counts(f.ownerA, f.orgA), 2, 0, 1, 1, 0)
	if ids := f.listIDs("?view=shipped"); len(ids) != 1 || ids[0] != paid.String() {
		t.Fatalf("view=shipped = %v; want the shipped order", ids)
	}

	// A partly refunded order is in the refunds tab (and still to ship while it did not ship).
	f.exec(`UPDATE shop_orders SET status = 'partially_refunded', refunded_minor = 100, paid_at = now() WHERE id = $1`, unpaid)
	wantCounts(t, f.counts(f.ownerA, f.orgA), 2, 1, 0, 1, 1)
	if ids := f.listIDs("?view=refunds"); len(ids) != 1 || ids[0] != unpaid.String() {
		t.Fatalf("view=refunds = %v", ids)
	}

	// Another organization counts only its own orders.
	wantCounts(t, f.counts(f.keyB, f.orgB), 0, 0, 0, 0, 0)
	if code, _, _ := f.do(http.MethodGet, f.orgPath(f.orgA, "/orders/counts"), f.keyB, ""); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("A's counts through B's key = %d; want 403 or 404", code)
	}
}

func TestShopParity_StaffNote(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 10)
	id, token := f.placedOrder(variant)
	path := f.orgPath(f.orgA, "/orders/"+id.String())

	code, body, _ := f.do(http.MethodGet, path, f.ownerA, "")
	note := body["staff_note"].(map[string]any)
	if code != http.StatusOK || note["text"] != "" || note["by"] != "" || note["at"] != nil {
		t.Fatalf("a new order's note = %d %v; want empty", code, body["staff_note"])
	}

	code, body, _ = f.do(http.MethodPatch, path, f.ownerA, `{"staff_note":"  Called, deliver after 18:00  "}`)
	note, _ = body["staff_note"].(map[string]any)
	if code != http.StatusOK || note["text"] != "Called, deliver after 18:00" || note["at"] == nil || !strings.HasPrefix(note["by"].(string), "shop-owner-") {
		t.Fatalf("PATCH note = %d %v; want the trimmed text, the author (the account has no name: its e-mail) and a time", code, body["staff_note"])
	}
	// A named account is shown by name.
	f.exec(`UPDATE users SET first_name = 'Dana', last_name = 'Levi' WHERE id = $1`, f.users[0])
	code, body, _ = f.do(http.MethodPatch, path, f.ownerA, `{"staff_note":"Second note"}`)
	if note, _ = body["staff_note"].(map[string]any); code != http.StatusOK || note["by"] != "Dana Levi" || note["text"] != "Second note" {
		t.Fatalf("PATCH note by a named user = %d %v", code, body["staff_note"])
	}
	// It is read back from the list and the card.
	code, body, _ = f.do(http.MethodGet, path, f.ownerA, "")
	if note, _ = body["staff_note"].(map[string]any); code != http.StatusOK || note["text"] != "Second note" {
		t.Fatalf("GET after PATCH = %d %v", code, body["staff_note"])
	}

	// The buyer's own status page never carries it.
	code, body, _ = f.do(http.MethodGet, "/v1/public/shop/checkout/"+token, "", "")
	if code != http.StatusOK || strings.Contains(strings.ToLower(toJSON(body)), "second note") || strings.Contains(toJSON(body), "staff") {
		t.Fatalf("public checkout status = %d %v; the staff note must never reach the buyer", code, body)
	}

	// Refusals.
	if code, body, _ := f.do(http.MethodPatch, path, f.ownerA, `{}`); code != http.StatusBadRequest || shopErrCode(body) != "shop.invalid_body" {
		t.Fatalf("PATCH without staff_note = %d %v; want 400 shop.invalid_body", code, body)
	}
	if code, body, _ := f.do(http.MethodPatch, path, f.ownerA, `{"staff_note":"`+strings.Repeat("x", 2001)+`"}`); code != http.StatusUnprocessableEntity || shopErrCode(body) != "shop.text_too_long" {
		t.Fatalf("PATCH of 2001 characters = %d %v; want 422 shop.text_too_long", code, body)
	}
	if code, body, _ := f.do(http.MethodPatch, f.orgPath(f.orgB, "/orders/"+id.String()), f.keyB, `{"staff_note":"mine now"}`); code != http.StatusNotFound || shopErrCode(body) != "shop.order_not_found" {
		t.Fatalf("PATCH through another organization = %d %v; want 404", code, body)
	}

	// Empty text erases the note with its author and time.
	code, body, _ = f.do(http.MethodPatch, path, f.ownerA, `{"staff_note":""}`)
	note, _ = body["staff_note"].(map[string]any)
	if code != http.StatusOK || note["text"] != "" || note["by"] != "" || note["at"] != nil {
		t.Fatalf("erasing the note = %d %v", code, body["staff_note"])
	}
}

func TestShopParity_ResendEmail(t *testing.T) {
	f := newShopFixture(t)
	_, variant := f.openShop(4500, 10)
	id, token := f.placedOrder(variant)
	path := f.orgPath(f.orgA, "/orders/"+id.String()+"/resend-email")

	// Nothing to resend before the order is paid.
	if code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"kind":"received"}`); code != http.StatusConflict || shopErrCode(body) != "shop.order_email_unavailable" {
		t.Fatalf("resend of an unpaid order = %d %v; want 409 shop.order_email_unavailable", code, body)
	}
	f.pay(token)
	first := f.emailJobs(id)
	if len(first) != 1 || first[0].Kind != shop.EmailReceived || first[0].Resend {
		t.Fatalf("jobs after payment = %+v; want one received letter, not a resend", first)
	}

	// A refused request does not spend the order's minute: the shipped letter
	// does not exist yet (409), the received one goes out right after.
	if code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"kind":"shipped"}`); code != http.StatusConflict || shopErrCode(body) != "shop.order_email_unavailable" {
		t.Fatalf("shipped letter of an unshipped order = %d %v; want 409", code, body)
	}
	if code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"kind":"nonsense"}`); code != http.StatusBadRequest || shopErrCode(body) != "shop.invalid_email_kind" {
		t.Fatalf("unknown kind = %d %v; want 400 shop.invalid_email_kind", code, body)
	}
	code, body, _ := f.do(http.MethodPost, path, f.ownerA, `{"kind":"received"}`)
	if code != http.StatusAccepted || body["queued"] != true || body["kind"] != "received" {
		t.Fatalf("resend = %d %v; want 202 queued", code, body)
	}
	jobs := f.emailJobs(id)
	if len(jobs) != 2 || jobs[1].Kind != shop.EmailReceived || !jobs[1].Resend || jobs[1].OrgID != f.orgA.String() {
		t.Fatalf("jobs after the resend = %+v; want the original and a Resend one", jobs)
	}

	// One request per order per minute.
	code, body, hdr := f.do(http.MethodPost, path, f.ownerA, `{"kind":"received"}`)
	if code != http.StatusTooManyRequests || shopErrCode(body) != "shop.rate_limited" || hdr.Get("Retry-After") == "" {
		t.Fatalf("second resend inside the minute = %d %v %v; want 429 shop.rate_limited with Retry-After", code, body, hdr)
	}
	if n := len(f.emailJobs(id)); n != 2 {
		t.Fatalf("jobs after the refused resend = %d; want still 2", n)
	}

	// Another organization cannot trigger it and does not learn the order exists.
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgB, "/orders/"+id.String()+"/resend-email"), f.keyB, `{"kind":"received"}`); code != http.StatusNotFound || shopErrCode(body) != "shop.order_not_found" {
		t.Fatalf("resend through another organization = %d %v; want 404", code, body)
	}

	// No address, no letter.
	bare, bareToken := f.placedOrder(variant)
	f.pay(bareToken)
	f.exec(`UPDATE shop_orders SET buyer_email = '' WHERE id = $1`, bare)
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+bare.String()+"/resend-email"), f.ownerA, `{"kind":"received"}`); code != http.StatusConflict || shopErrCode(body) != "shop.order_no_buyer_email" {
		t.Fatalf("resend without an address = %d %v; want 409 shop.order_no_buyer_email", code, body)
	}

	// Once shipped the shipping letter can be sent again too (a different order
	// of the same shop has its own minute).
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+bare.String()+"/ship"), f.ownerA, `{}`); code != http.StatusOK {
		t.Fatalf("ship = %d %v", code, body)
	}
	f.exec(`UPDATE shop_orders SET buyer_email = 'back@example.test' WHERE id = $1`, bare)
	if code, body, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/orders/"+bare.String()+"/resend-email"), f.ownerA, `{"kind":"shipped"}`); code != http.StatusAccepted {
		t.Fatalf("resend of the shipped letter = %d %v; want 202", code, body)
	}
}

func toJSON(m map[string]any) string {
	var sb strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				sb.WriteString(k + " ")
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		case string:
			sb.WriteString(x + " ")
		}
	}
	walk(m)
	return sb.String()
}
