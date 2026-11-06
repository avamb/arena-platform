//go:build integration

package httpserver

// shop_fulfillment_integration_test.go — the delivery steps of a shop order
// and the sales bot's chats (spec 08_architecture/61 sections 7a.4 and 7b,
// stage M5): the organization route, the bot's service routes and the
// superadmin's "Telegram chats" screen, through the real router. Run on a
// scratch database:
//
//	go test -tags integration -count=1 -p 1 -run 'TestShopFulfillment|TestShopDelivery|TestSalesChats' ./apps/backend/internal/platform/httpserver/

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

const deliverySvcToken = "svc-token-shop-delivery-test"

// deliveryEnv is a shop module env whose server knows the bot service token.
type deliveryEnv struct {
	*shopModuleEnv
}

func newDeliveryEnv(t *testing.T) *deliveryEnv {
	t.Helper()
	e := &deliveryEnv{newShopModuleEnvWith(t, func(c *config.Config) { c.BotServiceToken = deliverySvcToken })}
	t.Cleanup(func() {
		ctx := context.Background()
		orgs := []uuid.UUID{e.orgA, e.orgB}
		_, _ = e.pool.Exec(ctx, `DELETE FROM sales_notification_deliveries WHERE key LIKE 'shop_failed:%' OR key LIKE 'shop_sale_paid:%'`)
		_, _ = e.pool.Exec(ctx, `DELETE FROM sales_chat_connect_codes WHERE org_id = ANY($1) OR (org_id IS NULL AND name LIKE 'm5-test%')`, orgs)
		_, _ = e.pool.Exec(ctx, `DELETE FROM sales_notification_subscriptions WHERE org_id IS NULL AND name LIKE 'm5-test%'`)
	})
	return e
}

// doHdr sends one request with extra headers through the real router.
func (e *deliveryEnv) doHdr(method, path, token, body string, hdr map[string]string) (int, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.router.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(out) == 0 && len(raw) > 0 {
		out["_raw"] = string(raw)
	}
	return rec.Code, out
}

func (e *deliveryEnv) emailJobs(orderID uuid.UUID, kind string) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM worker_jobs
		WHERE job_type = 'shop.order_email' AND payload->>'order_id' = $1 AND payload->>'kind' = $2 AND payload->>'resend' IS NULL`,
		orderID.String(), kind).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *deliveryEnv) eventRows(orderID uuid.UUID) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM shop_order_events WHERE order_id = $1`, orderID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *deliveryEnv) step(orderID uuid.UUID, body string, hdr map[string]string) (int, map[string]any) {
	e.t.Helper()
	return e.doHdr(http.MethodPost, e.orgPath(e.orgA, "/orders/"+orderID.String()+"/fulfillment"), e.ownerA, body, hdr)
}

func TestShopFulfillment_StepsThroughTheOrgAPI(t *testing.T) {
	e := newDeliveryEnv(t)
	_, variant := e.openShop(4500, 10)
	o := e.paidOrder(variant, 1)
	base := "/orders/" + o.ID.String()

	code, body, _ := e.do(http.MethodGet, e.orgPath(e.orgA, base), e.ownerA, "")
	if code != http.StatusOK || body["fulfillment_step"] != shop.StepWaiting {
		t.Fatalf("a paid order starts at waiting: %d %v", code, body)
	}
	if ev, ok := body["events"].([]any); !ok || len(ev) != 0 {
		t.Fatalf("events of a new order = %v", body["events"])
	}

	// packed, from the bot: the history names the channel and the person.
	code, body = e.step(o.ID, `{"step":"packed","note":"box 3"}`, map[string]string{"X-Client-Channel": "telegram_bot"})
	if code != http.StatusOK || body["fulfillment_step"] != shop.StepPacked {
		t.Fatalf("packed = %d %v", code, body)
	}
	ev := body["events"].([]any)
	if len(ev) != 1 {
		t.Fatalf("events = %v", ev)
	}
	first := ev[0].(map[string]any)
	if first["step"] != "packed" || first["from_step"] != "waiting" || first["via"] != "telegram_bot" ||
		first["note"] != "box 3" || first["actor_user_id"] == nil || first["actor_name"] == "" {
		t.Fatalf("history row %v", first)
	}
	// The same step again changes nothing.
	if code, body = e.step(o.ID, `{"step":"packed"}`, nil); code != http.StatusOK || e.eventRows(o.ID) != 1 {
		t.Fatalf("repeated packed = %d %v rows %d", code, body, e.eventRows(o.ID))
	}
	// delivered does not follow packed.
	code, body = e.step(o.ID, `{"step":"delivered"}`, nil)
	if code != http.StatusConflict || shopErrCode(body) != "shop.fulfillment_invalid_step" {
		t.Fatalf("packed -> delivered = %d %v", code, body)
	}
	// Bad bodies.
	if code, body = e.step(o.ID, `{"step":"waiting"}`, nil); code != http.StatusBadRequest || shopErrCode(body) != "shop.invalid_fulfillment_step" {
		t.Fatalf("waiting = %d %v", code, body)
	}
	if code, body = e.step(o.ID, `{"step":"failed"}`, nil); code != http.StatusBadRequest || shopErrCode(body) != "shop.fulfillment_reason_required" {
		t.Fatalf("failed without a reason = %d %v", code, body)
	}

	// handed_over is "ship": status, shipped_at, the number, one letter.
	code, body = e.step(o.ID, `{"step":"handed_over","tracking":"RR1"}`, nil)
	if code != http.StatusOK || body["status"] != shop.StatusShipped || body["tracking_note"] != "RR1" || body["shipped_at"] == nil {
		t.Fatalf("handed_over = %d %v", code, body)
	}
	shippedAt := body["shipped_at"]
	if n := e.outboxCount(shop.EventOrderShipped, o.ID); n != 1 || e.emailJobs(o.ID, shop.EmailShipped) != 1 {
		t.Fatalf("shipped events %d letters %d", n, e.emailJobs(o.ID, shop.EmailShipped))
	}
	// A new number on the same step is stored, nothing else happens.
	code, body = e.step(o.ID, `{"step":"handed_over","tracking":"RR2"}`, nil)
	if code != http.StatusOK || body["tracking_note"] != "RR2" || body["shipped_at"] != shippedAt || e.emailJobs(o.ID, shop.EmailShipped) != 1 {
		t.Fatalf("new number = %d %v", code, body)
	}
	// failed, then another hand-over: no second shipped letter.
	if code, body = e.step(o.ID, `{"step":"failed","reason":"not_home"}`, nil); code != http.StatusOK || body["fulfillment_step"] != shop.StepFailed {
		t.Fatalf("failed = %d %v", code, body)
	}
	if code, body = e.step(o.ID, `{"step":"handed_over"}`, nil); code != http.StatusOK || body["shipped_at"] != shippedAt || body["tracking_note"] != "RR2" {
		t.Fatalf("handed over again = %d %v", code, body)
	}
	if e.emailJobs(o.ID, shop.EmailShipped) != 1 || e.outboxCount(shop.EventOrderShipped, o.ID) != 1 {
		t.Fatal("a second hand-over sent a second shipped letter or event")
	}
	// delivered: one letter, always; a repeat queues none.
	if code, body = e.step(o.ID, `{"step":"delivered"}`, nil); code != http.StatusOK || body["fulfillment_step"] != shop.StepDelivered {
		t.Fatalf("delivered = %d %v", code, body)
	}
	_, _ = e.step(o.ID, `{"step":"delivered"}`, nil)
	if n := e.emailJobs(o.ID, shop.EmailDelivered); n != 1 {
		t.Fatalf("delivered letters = %d", n)
	}
	// Every change is in the outbox, the history and the audit trail.
	if n := e.outboxCount(shop.EventOrderFulfillmentChanged, o.ID); n != 6 || e.eventRows(o.ID) != 6 {
		t.Fatalf("fulfillment events %d rows %d; want 6", n, e.eventRows(o.ID))
	}
	var audits int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'v1.shop.order.fulfillment' AND resource_id = $1`, o.ID.String()).Scan(&audits)
	if audits != 6 {
		t.Fatalf("audit rows = %d", audits)
	}

	// The list filters by step and the counts carry the steps.
	_, list, _ := e.doKeyed(http.MethodPost, e.orgPath(e.orgA, "/orders/search"), e.ownerA, `{"fulfillment_step":"delivered"}`, "")
	if got := list["orders"].([]any); len(got) != 1 || got[0].(map[string]any)["fulfillment_step"] != "delivered" || got[0].(map[string]any)["events"] != nil {
		t.Fatalf("delivered list = %v", list)
	}
	if code, _, _ = e.do(http.MethodGet, e.orgPath(e.orgA, "/orders?fulfillment_step=lost"), e.ownerA, ""); code != http.StatusBadRequest {
		t.Fatalf("unknown step filter = %d", code)
	}
	_, counts, _ := e.do(http.MethodGet, e.orgPath(e.orgA, "/orders/counts"), e.ownerA, "")
	if steps := counts["steps"].(map[string]any); steps["delivered"].(float64) != 1 || steps["waiting"].(float64) != 0 {
		t.Fatalf("counts = %v", counts)
	}

	// The delivered letter can be sent again; a waiting order has none.
	if code, body, _ = e.do(http.MethodPost, e.orgPath(e.orgA, base+"/resend-email"), e.ownerA, `{"kind":"delivered"}`); code != http.StatusAccepted {
		t.Fatalf("resend delivered = %d %v", code, body)
	}
	other := e.paidOrder(variant, 1)
	code, body, _ = e.do(http.MethodPost, e.orgPath(e.orgA, "/orders/"+other.ID.String()+"/resend-email"), e.ownerA, `{"kind":"delivered"}`)
	if code != http.StatusConflict || shopErrCode(body) != "shop.order_email_unavailable" {
		t.Fatalf("resend delivered of a waiting order = %d %v", code, body)
	}

	// A refunded order is never shipped.
	if code, body, _ = e.do(http.MethodPost, e.orgPath(e.orgA, "/orders/"+other.ID.String()+"/refund"), e.ownerA, `{}`); code != http.StatusOK {
		t.Fatalf("refund = %d %v", code, body)
	}
	code, body = e.step(other.ID, `{"step":"packed"}`, nil)
	if code != http.StatusConflict || shopErrCode(body) != "shop.fulfillment_order_closed" {
		t.Fatalf("packed of a refunded order = %d %v", code, body)
	}
	// Another organization's order is not found.
	code, body, _ = e.do(http.MethodPost, e.orgPath(e.orgB, base+"/fulfillment"), e.ownerB, `{"step":"packed"}`)
	if code != http.StatusNotFound {
		t.Fatalf("foreign order = %d %v", code, body)
	}
}

// Changing the tracking note through /ship moves no step: a delivered order
// stays delivered, a failed one stays failed (no history row, no event, no
// letter), while an explicit handed_over step still means a new try.
func TestShopFulfillment_TrackingNoteEditMovesNoStep(t *testing.T) {
	e := newDeliveryEnv(t)
	_, variant := e.openShop(4500, 10)
	ship := func(id uuid.UUID, note string) (int, map[string]any) {
		code, body, _ := e.do(http.MethodPost, e.orgPath(e.orgA, "/orders/"+id.String()+"/ship"), e.ownerA, `{"tracking_note":"`+note+`"}`)
		return code, body
	}

	// A first ship still goes through the step machine.
	o := e.paidOrder(variant, 1)
	code, body := ship(o.ID, "N1")
	if code != http.StatusOK || body["fulfillment_step"] != shop.StepHandedOver || body["tracking_note"] != "N1" ||
		e.eventRows(o.ID) != 1 || e.outboxCount(shop.EventOrderShipped, o.ID) != 1 || e.emailJobs(o.ID, shop.EmailShipped) != 1 {
		t.Fatalf("first ship = %d %v rows %d", code, body, e.eventRows(o.ID))
	}
	// A note edit at handed_over: stored, nothing else.
	if code, body = ship(o.ID, "N2"); code != http.StatusOK || body["tracking_note"] != "N2" || body["fulfillment_step"] != shop.StepHandedOver ||
		e.eventRows(o.ID) != 1 || e.outboxCount(shop.EventOrderFulfillmentChanged, o.ID) != 1 || e.emailJobs(o.ID, shop.EmailShipped) != 1 {
		t.Fatalf("note edit at handed_over = %d %v", code, body)
	}

	// Delivered: the note changes, the step stays.
	if code, body = e.step(o.ID, `{"step":"delivered"}`, nil); code != http.StatusOK {
		t.Fatalf("delivered = %d %v", code, body)
	}
	rows, changed := e.eventRows(o.ID), e.outboxCount(shop.EventOrderFulfillmentChanged, o.ID)
	if code, body = ship(o.ID, "N3"); code != http.StatusOK || body["tracking_note"] != "N3" || body["fulfillment_step"] != shop.StepDelivered {
		t.Fatalf("note edit of a delivered order = %d %v", code, body)
	}
	if e.eventRows(o.ID) != rows || e.outboxCount(shop.EventOrderFulfillmentChanged, o.ID) != changed {
		t.Fatal("a note edit of a delivered order wrote a history row or an event")
	}

	// Failed: the note changes, the order stays failed and nothing is written.
	f := e.paidOrder(variant, 1)
	if code, body = ship(f.ID, "F1"); code != http.StatusOK {
		t.Fatalf("ship = %d %v", code, body)
	}
	if code, body = e.step(f.ID, `{"step":"failed","reason":"not_home"}`, nil); code != http.StatusOK {
		t.Fatalf("failed = %d %v", code, body)
	}
	rows, changed = e.eventRows(f.ID), e.outboxCount(shop.EventOrderFulfillmentChanged, f.ID)
	if code, body = ship(f.ID, "F2"); code != http.StatusOK || body["tracking_note"] != "F2" || body["fulfillment_step"] != shop.StepFailed {
		t.Fatalf("note edit of a failed order = %d %v", code, body)
	}
	if e.eventRows(f.ID) != rows || e.outboxCount(shop.EventOrderFulfillmentChanged, f.ID) != changed || e.emailJobs(f.ID, shop.EmailShipped) != 1 {
		t.Fatal("a note edit of a failed order wrote a history row, an event or a letter")
	}
	// An explicit handed_over is still a new try.
	if code, body = e.step(f.ID, `{"step":"handed_over"}`, nil); code != http.StatusOK || body["fulfillment_step"] != shop.StepHandedOver {
		t.Fatalf("new try = %d %v", code, body)
	}
}

// insertChat makes a sales chat of the organization (nil: the operator's).
func (e *deliveryEnv) insertChat(org *uuid.UUID, chat string, kinds ...string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(), `INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
		VALUES ($1, 'm5-test chat', $2, $3) RETURNING id`, org, chat, kinds).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *deliveryEnv) botStep(token, chat string, orderID uuid.UUID, step, extra string) (int, map[string]any) {
	e.t.Helper()
	body := fmt.Sprintf(`{"chat_id":%q,"telegram_user_id":4242,"telegram_name":"Ivan Courier","order_id":%q,"step":%q%s}`, chat, orderID, step, extra)
	return e.doHdr(http.MethodPost, "/v1/bot/shop-delivery/steps", token, body, nil)
}

func TestShopDelivery_ServiceRoute(t *testing.T) {
	e := newDeliveryEnv(t)
	_, variant := e.openShop(4500, 10)
	o := e.paidOrder(variant, 1)
	chatA := fmt.Sprintf("-100%d", 1000000+uuid.New().ID()%8999999)
	chatSales := chatA + "1"
	chatB := chatA + "2"
	e.insertChat(&e.orgA, chatA, "shop_delivery")
	e.insertChat(&e.orgA, chatSales, "shop_sale")
	e.insertChat(&e.orgB, chatB, "shop_delivery")

	if code, _ := e.botStep("wrong", chatA, o.ID, "packed", ""); code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", code)
	}
	for _, chat := range []string{chatSales, chatB, "-999"} {
		if code, body := e.botStep(deliverySvcToken, chat, o.ID, "packed", ""); code != http.StatusNotFound || shopErrCode(body) != "shop_delivery.not_found" {
			t.Fatalf("chat %s = %d %v", chat, code, body)
		}
	}
	if code, _ := e.botStep(deliverySvcToken, chatA, uuid.New(), "packed", ""); code != http.StatusNotFound {
		t.Fatalf("unknown order = %d", code)
	}

	code, body := e.botStep(deliverySvcToken, chatA, o.ID, "packed", "")
	if code != http.StatusOK || body["outcome"] != "applied" || body["fulfillment_step"] != "packed" {
		t.Fatalf("packed = %d %v", code, body)
	}
	card := body["card"].(map[string]any)
	if !strings.Contains(card["text"].(string), "Ivan Courier") || len(card["rows"].([]any)) == 0 {
		t.Fatalf("card %v", card)
	}
	var via, name string
	var tg int64
	if err := e.pool.QueryRow(context.Background(), `SELECT via, actor_name, actor_telegram_id FROM shop_order_events WHERE order_id = $1`, o.ID).
		Scan(&via, &name, &tg); err != nil || via != "delivery_group" || name != "Ivan Courier" || tg != 4242 {
		t.Fatalf("history %s %s %d %v", via, name, tg, err)
	}
	// The same press again, and a step that no longer follows.
	if _, body = e.botStep(deliverySvcToken, chatA, o.ID, "packed", ""); body["outcome"] != "unchanged" {
		t.Fatalf("repeat = %v", body)
	}
	if _, body = e.botStep(deliverySvcToken, chatA, o.ID, "delivered", ""); body["outcome"] != "stale" || body["fulfillment_step"] != "packed" {
		t.Fatalf("stale = %v", body)
	}
	if code, body = e.botStep(deliverySvcToken, chatA, o.ID, "handed_over", `,"tracking":"TG-1"`); body["outcome"] != "applied" {
		t.Fatalf("handed over = %d %v", code, body)
	}
	got, _ := e.q.GetShopOrder(context.Background(), o.ID, e.orgA)
	if got.Status != shop.StatusShipped || got.TrackingNote != "TG-1" || e.emailJobs(o.ID, shop.EmailShipped) != 1 {
		t.Fatalf("order after the group's hand-over: %s %q", got.Status, got.TrackingNote)
	}
	// The card read route.
	code, body = e.doHdr(http.MethodPost, "/v1/bot/shop-delivery/card", deliverySvcToken, fmt.Sprintf(`{"chat_id":%q,"order_id":%q}`, chatA, o.ID), nil)
	if code != http.StatusOK || body["fulfillment_step"] != "handed_over" {
		t.Fatalf("card = %d %v", code, body)
	}

	// A refunded order: "do not ship", no buttons.
	r := e.paidOrder(variant, 1)
	if code, body, _ := e.do(http.MethodPost, e.orgPath(e.orgA, "/orders/"+r.ID.String()+"/refund"), e.ownerA, `{}`); code != http.StatusOK {
		t.Fatalf("refund = %d %v", code, body)
	}
	code, body = e.botStep(deliverySvcToken, chatA, r.ID, "packed", "")
	if code != http.StatusOK || body["outcome"] != "closed" || len(body["card"].(map[string]any)["rows"].([]any)) != 0 {
		t.Fatalf("refunded press = %d %v", code, body)
	}
	// A switched-off or deleted chat may not press.
	e.exec(`UPDATE sales_notification_subscriptions SET allowed = false WHERE chat_id = $1`, chatA)
	if code, _ = e.botStep(deliverySvcToken, chatA, o.ID, "delivered", ""); code != http.StatusNotFound {
		t.Fatalf("switched-off chat = %d", code)
	}
}

func TestSalesChats_AdminScreenAndConnectCode(t *testing.T) {
	e := newDeliveryEnv(t)
	base := "/v1/admin/organizations/" + e.orgA.String() + "/sales-chats"
	reason := map[string]string{"X-Admin-Reason": "m5 test"}

	// Only a platform superadmin, and a write needs a reason.
	if code, _ := e.doHdr(http.MethodGet, base, e.ownerA, "", nil); code != http.StatusForbidden {
		t.Fatalf("owner list = %d", code)
	}
	if code, _ := e.doHdr(http.MethodPost, base+"/connect-codes", e.ownerA, `{"name":"x","kinds":["shop_delivery"]}`, reason); code != http.StatusForbidden {
		t.Fatalf("owner code = %d", code)
	}
	if code, body := e.doHdr(http.MethodPost, base+"/connect-codes", e.super, `{"name":"x","kinds":["shop_delivery"]}`, nil); code != http.StatusBadRequest {
		t.Fatalf("no reason = %d %v", code, body)
	}
	if code, body := e.doHdr(http.MethodPost, base+"/connect-codes", e.super, `{"name":"x","kinds":["shop"]}`, reason); code != http.StatusUnprocessableEntity || shopErrCode(body) != "sales_chat.invalid_kinds" {
		t.Fatalf("bad kinds = %d %v", code, body)
	}

	code, created := e.doHdr(http.MethodPost, base+"/connect-codes", e.super, `{"name":"m5-test delivery","kinds":["shop_delivery","shop_sale"]}`, reason)
	if code != http.StatusCreated {
		t.Fatalf("code = %d %v", code, created)
	}
	codeText := created["code"].(string)
	if len(codeText) != 11 || created["command"] != "/connect "+codeText {
		t.Fatalf("code %v", created)
	}
	var stored, codeID string
	if err := e.pool.QueryRow(context.Background(), `SELECT code_hash, id::text FROM sales_chat_connect_codes WHERE org_id = $1`, e.orgA).Scan(&stored, &codeID); err != nil ||
		strings.Contains(stored, strings.ReplaceAll(codeText, "-", "")) {
		t.Fatalf("stored %q %v", stored, err)
	}
	var auditHasCode bool
	_ = e.pool.QueryRow(context.Background(), `SELECT bool_or(metadata::text LIKE '%' || $2 || '%') FROM audit_events WHERE action = 'v1.admin.sales_chat.connect_code' AND resource_id = $1`,
		codeID, strings.ReplaceAll(codeText, "-", "")).Scan(&auditHasCode)
	if auditHasCode {
		t.Fatal("the code reached the audit trail")
	}

	// The bot redeems it once, in lower case and without the dash too.
	chat := fmt.Sprintf("-100%d", 2000000+uuid.New().ID()%7999999)
	connect := func(code string) (int, map[string]any) {
		return e.doHdr(http.MethodPost, "/v1/bot/sales-chats/connect", deliverySvcToken,
			fmt.Sprintf(`{"code":%q,"chat_id":%q,"chat_title":"Couriers","telegram_user_id":7,"telegram_name":"Op"}`, code, chat), nil)
	}
	code, body := connect(strings.ToLower(strings.ReplaceAll(codeText, "-", "")))
	if code != http.StatusOK || body["operator"] != false || !strings.HasPrefix(body["org_name"].(string), "Shop A") {
		t.Fatalf("connect = %d %v", code, body)
	}
	if code, _ = connect(codeText); code != http.StatusNotFound {
		t.Fatalf("second use = %d", code)
	}
	// An expired code.
	_, again := e.doHdr(http.MethodPost, base+"/connect-codes", e.super, `{"name":"m5-test late","kinds":["tickets"]}`, reason)
	e.exec(`UPDATE sales_chat_connect_codes SET expires_at = now() - interval '1 minute' WHERE name = 'm5-test late'`)
	if code, body = connect(again["code"].(string)); code != http.StatusNotFound || shopErrCode(body) != "sales_chat.code_invalid" {
		t.Fatalf("expired = %d %v", code, body)
	}

	code, list := e.doHdr(http.MethodGet, base, e.super, "", nil)
	items := list["items"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("list = %d %v", code, list)
	}
	item := items[0].(map[string]any)
	id := item["id"].(string)
	if item["chat_id"] != chat || item["chat_title"] != "Couriers" || item["connected_at"] == nil || len(item["kinds"].([]any)) != 2 {
		t.Fatalf("chat %v", item)
	}

	// Edit, then the bot is removed, then a delete.
	code, body = e.doHdr(http.MethodPatch, "/v1/admin/sales-chats/"+id, e.super, `{"name":"m5-test couriers","kinds":["shop_delivery"]}`, reason)
	if code != http.StatusOK || body["name"] != "m5-test couriers" || len(body["kinds"].([]any)) != 1 {
		t.Fatalf("patch = %d %v", code, body)
	}
	if code, body = e.doHdr(http.MethodPatch, "/v1/admin/sales-chats/"+id, e.super, `{"kinds":[]}`, reason); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty kinds = %d %v", code, body)
	}
	code, body = e.doHdr(http.MethodPost, "/v1/bot/sales-chats/removed", deliverySvcToken, fmt.Sprintf(`{"chat_id":%q}`, chat), nil)
	if code != http.StatusOK || body["marked"].(float64) != 1 {
		t.Fatalf("removed = %d %v", code, body)
	}
	_, list = e.doHdr(http.MethodGet, base, e.super, "", nil)
	if list["items"].([]any)[0].(map[string]any)["bot_removed_at"] == nil {
		t.Fatalf("removed not shown: %v", list)
	}
	if code, _ = e.doHdr(http.MethodDelete, "/v1/admin/sales-chats/"+id, e.super, "", nil); code != http.StatusBadRequest {
		t.Fatalf("delete without a reason = %d", code)
	}
	if code, _ = e.doHdr(http.MethodDelete, "/v1/admin/sales-chats/"+id, e.super, "", reason); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if code, _ = e.doHdr(http.MethodDelete, "/v1/admin/sales-chats/"+id, e.super, "", reason); code != http.StatusNotFound {
		t.Fatalf("delete again = %d", code)
	}
	if _, list = e.doHdr(http.MethodGet, base, e.super, "", nil); len(list["items"].([]any)) != 0 {
		t.Fatalf("a deleted chat is listed: %v", list)
	}
	for _, action := range []string{"v1.admin.sales_chat.connect_code", "v1.admin.sales_chat.update", "v1.admin.sales_chat.delete", "v1.sales_chat.connected"} {
		var n int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1 AND (metadata->>'org_id' = $2 OR resource_id = $3)`,
			action, e.orgA.String(), id).Scan(&n)
		if n == 0 {
			t.Errorf("no audit row %s", action)
		}
	}
	var reasonOK bool
	_ = e.pool.QueryRow(context.Background(), `SELECT bool_and(metadata->>'reason' = 'm5 test') FROM audit_events WHERE action LIKE 'v1.admin.sales_chat.%' AND (resource_id = $1 OR metadata->>'org_id' = $2)`,
		id, e.orgA.String()).Scan(&reasonOK)
	if !reasonOK {
		t.Error("an admin audit row lacks the reason")
	}

	// The operator's tab.
	code, op := e.doHdr(http.MethodPost, "/v1/admin/sales-chats/operator/connect-codes", e.super, `{"name":"m5-test operator","kinds":["shop_sale"]}`, reason)
	if code != http.StatusCreated {
		t.Fatalf("operator code = %d %v", code, op)
	}
	opChat := chat + "9"
	code, body = e.doHdr(http.MethodPost, "/v1/bot/sales-chats/connect", deliverySvcToken, fmt.Sprintf(`{"code":%q,"chat_id":%q}`, op["code"], opChat), nil)
	if code != http.StatusOK || body["operator"] != true {
		t.Fatalf("operator connect = %d %v", code, body)
	}
	_, list = e.doHdr(http.MethodGet, "/v1/admin/sales-chats/operator", e.super, "", nil)
	found := false
	for _, it := range list["items"].([]any) {
		if it.(map[string]any)["chat_id"] == opChat {
			found = true
		}
	}
	if !found {
		t.Fatalf("operator chat not listed: %v", list)
	}
}
