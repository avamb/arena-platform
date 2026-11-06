//go:build integration

// site_p6_p7_integration_test.go - the platform items of the Vino&Co static
// site (spec 62) against a live, migrated PostgreSQL (migration 0204):
//
//	P-6   the feed's explicit "unpublished / deleted" marker,
//	P-7   "my tickets by link" and the event block + absolute pdf_url of the
//	      order status,
//	P-16  the public order statuses needs_help / refunded / cancelled.
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_w2_read?sslmode=disable \
//	    go test -tags integration -count=1 ./apps/backend/internal/platform/httpserver/hfeed/ \
//	    -run 'TestSiteP6|TestSiteP7|TestSiteP16'
package hfeed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
)

const siteTestAPIBase = "https://api.example.test"

func siteHandler(pool *pgxpool.Pool) *Handler {
	q := gen.New(pool)
	return (&Handler{
		feedTokenQueries:   q,
		publicFeedQueries:  q,
		sessionQueries:     q,
		tierQueries:        q,
		checkoutQueries:    q,
		reservationQueries: q,
		ticketQueries:      q,
		credentialQueries:  q,
		logger:             slog.Default(),
		rl:                 allowAllRateLimiter{},
	}).WithAPIPublicBase(func() string { return siteTestAPIBase }).
		WithMyTickets(mytickets.New(pool, nil), "salt")
}

func siteDo(h http.HandlerFunc, method, target, body string, params map[string]string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "203.0.113.7:4000"
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func siteExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// ── P-6 ──────────────────────────────────────────────────────────────────────

type removedList struct {
	Events        []map[string]any `json:"events"`
	RemovedEvents []struct {
		ID        string  `json:"id"`
		Name      *string `json:"name"`
		Status    string  `json:"status"`
		RemovedAt string  `json:"removed_at"`
	} `json:"removed_events"`
}

func feedList(t *testing.T, h *Handler, token string, includeRemoved bool) (removedList, map[string]json.RawMessage) {
	t.Helper()
	target := "/v1/public/feeds/" + token + "/events"
	if includeRemoved {
		target += "?include_removed=true"
	}
	w := siteDo(h.HandlePublicFeedEvents, http.MethodGet, target, "", map[string]string{"feed_token": token}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s)", w.Code, w.Body.String())
	}
	var out removedList
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	return out, raw
}

func TestSiteP6_FeedMarksRemovedEvents(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	f := newPublicPageFixture(t, context.Background(), pool, false)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM event_publication_removals WHERE event_id = $1`, f.eventID)
	})
	h := siteHandler(pool)

	// Live event: listed, nothing removed, and without the parameter the
	// answer has no removed_events key at all.
	got, _ := feedList(t, h, f.token, true)
	if len(got.Events) != 1 || len(got.RemovedEvents) != 0 {
		t.Fatalf("live: events=%d removed=%d", len(got.Events), len(got.RemovedEvents))
	}
	if _, raw := feedList(t, h, f.token, false); raw["removed_events"] != nil {
		t.Fatal("removed_events present without include_removed")
	}

	// Unpublish: the publication row is deleted, the trigger keeps the trace.
	siteExec(t, pool, `DELETE FROM event_publications WHERE event_id = $1`, f.eventID)
	got, _ = feedList(t, h, f.token, true)
	if len(got.Events) != 0 || len(got.RemovedEvents) != 1 {
		t.Fatalf("unpublished: events=%d removed=%d", len(got.Events), len(got.RemovedEvents))
	}
	r := got.RemovedEvents[0]
	if r.ID != f.eventID.String() || r.Status != "unpublished" || r.Name == nil || r.RemovedAt == "" {
		t.Fatalf("unpublished item = %+v", r)
	}
	if _, raw := feedList(t, h, f.token, false); raw["removed_events"] != nil {
		t.Fatal("removed_events present without include_removed after unpublish")
	}

	// Published again: the trace goes.
	if _, err := f.q.PublishEvent(context.Background(), f.eventID, f.tokenID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = feedList(t, h, f.token, true)
	if len(got.Events) != 1 || len(got.RemovedEvents) != 0 {
		t.Fatalf("republished: events=%d removed=%d", len(got.Events), len(got.RemovedEvents))
	}

	// Still attached but no longer published: the terminal status says why.
	for _, c := range []struct{ set, want string }{
		{`status = 'cancelled'`, "cancelled"},
		{`status = 'archived'`, "archived"},
		{`status = 'draft'`, "unpublished"},
		{`status = 'archived', deleted_at = now()`, "deleted"},
	} {
		siteExec(t, pool, `UPDATE events SET `+c.set+` WHERE id = $1`, f.eventID)
		got, _ = feedList(t, h, f.token, true)
		if len(got.Events) != 0 || len(got.RemovedEvents) != 1 || got.RemovedEvents[0].Status != c.want {
			t.Fatalf("%s: events=%d removed=%+v, want status %s", c.set, len(got.Events), got.RemovedEvents, c.want)
		}
		siteExec(t, pool, `UPDATE events SET status = 'published', deleted_at = NULL WHERE id = $1`, f.eventID)
	}

	// A removal older than 30 days is not reported.
	siteExec(t, pool, `DELETE FROM event_publications WHERE event_id = $1`, f.eventID)
	siteExec(t, pool, `UPDATE event_publication_removals SET removed_at = now() - interval '31 days' WHERE event_id = $1`, f.eventID)
	if got, _ = feedList(t, h, f.token, true); len(got.RemovedEvents) != 0 {
		t.Fatalf("old removal reported: %+v", got.RemovedEvents)
	}
}

// ── order fixture ────────────────────────────────────────────────────────────

type siteOrder struct {
	orgID, channelID, eventID, sessionID, orderID, ticketID, csID uuid.UUID
	feedToken, checkoutToken, email                               string
}

// seedSiteOrder creates an organization with a channel, a feed token, a
// published event with one session and one paid order of one ticket that has
// a PDF credential.
func seedSiteOrder(t *testing.T, pool *pgxpool.Pool, email string) siteOrder {
	t.Helper()
	ctx := context.Background()
	run := uuid.NewString()
	q := gen.New(pool)
	org, err := q.InsertOrganization(ctx, "Site Org "+run, "site-org-"+run, "DE", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := q.InsertSalesChannel(ctx, org.ID, "Site Channel "+run, "direct_merchant", "stripe", nil, "0.00", nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ft, err := q.InsertFeedToken(ctx, "site-token-"+run, ch.ID, "site test")
	if err != nil {
		t.Fatal(err)
	}
	o := siteOrder{
		orgID: org.ID, channelID: ch.ID, feedToken: ft.Token, email: email,
		sessionID: uuid.New(), orderID: uuid.New(), ticketID: uuid.New(), csID: uuid.New(),
	}
	venueID, resvID, tierID := uuid.New(), uuid.New(), uuid.New()
	desc := "site test event"
	ev, err := q.InsertEvent(ctx, o.orgID, "Wine Night "+run, &desc, "published", "public", nil, "text")
	if err != nil {
		t.Fatal(err)
	}
	o.eventID = ev.ID
	siteExec(t, pool, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1,$2,'Cellar Hall','Asia/Jerusalem')`, venueID, o.orgID)
	siteExec(t, pool, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, sales_end_at, capacity_total, status, currency, currency_source)
	      VALUES ($1,$2,$3, now() + interval '10 days', now() + interval '10 days 2 hours', now() + interval '10 days', 50, 'scheduled', 'ILS', 'override')`,
		o.sessionID, o.eventID, venueID)
	siteExec(t, pool, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1,$2,$3,$4,1,'converted', now() + interval '1 hour', now())`, resvID, o.orgID, o.channelID, o.sessionID)
	siteExec(t, pool, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1,$2,$3,$4,'completed')`,
		o.csID, o.orgID, o.channelID, resvID)
	siteExec(t, pool, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	      source, status, currency, subtotal, discount, charge, total, buyer_email, paid_at)
	      VALUES ($1,$2,$3,$4,$5,$6,$7,'public_feed','paid','ILS',100,0,0,100,$8, now())`,
		o.orderID, o.orgID, o.channelID, o.eventID, o.sessionID, o.csID, resvID, email)
	siteExec(t, pool, `INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency)
	      VALUES ($1,$2,'General','fixed',100,'ILS')`, tierID, o.sessionID)
	siteExec(t, pool, `INSERT INTO tickets (id, checkout_session_id, session_id, tier_id, holder_email, order_id)
	      VALUES ($1,$2,$3,$4,$5,$6)`, o.ticketID, o.csID, o.sessionID, tierID, email, o.orderID)
	siteExec(t, pool, `INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1,'pdf',$2)`,
		o.ticketID, base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 test")))
	if err := pool.QueryRow(ctx, `SELECT checkout_token FROM checkout_sessions WHERE id = $1`, o.csID).Scan(&o.checkoutToken); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		for _, s := range []string{
			`DELETE FROM my_tickets_link_requests WHERE org_id = $1`,
			`DELETE FROM my_tickets_sessions WHERE org_id = $1`,
			`DELETE FROM ticket_credentials WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id IN (SELECT id FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)))`,
			`DELETE FROM tickets WHERE session_id IN (SELECT id FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1))`,
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM ticket_tiers WHERE session_id IN (SELECT id FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1))`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`,
		} {
			arg := o.orgID
			if strings.Contains(s, "sales_channel_id") {
				arg = o.channelID
			}
			_, _ = pool.Exec(bg, s, arg)
		}
		_, _ = pool.Exec(bg, `DELETE FROM sales_channels WHERE org_id = $1`, o.orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, o.orgID)
	})
	return o
}

// ── P-16 + the event block + the absolute pdf_url ────────────────────────────

func statusBody(t *testing.T, h *Handler, token string) (map[string]any, int) {
	t.Helper()
	w := siteDo(h.HandleGetPublicCheckoutStatus, http.MethodGet, "/v1/public/checkout/"+token, "", map[string]string{"checkout_token": token}, nil)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out, w.Code
}

func TestSiteP16_PublicStatusesAndEventBlock(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	o := seedSiteOrder(t, pool, "p16@example.com")
	h := siteHandler(pool)

	body, code := statusBody(t, h, o.checkoutToken)
	if code != 200 || body["status"] != "paid" {
		t.Fatalf("paid: %d %v", code, body["status"])
	}
	ev, _ := body["event"].(map[string]any)
	if ev == nil || ev["event_id"] != o.eventID.String() || ev["timezone"] != "Asia/Jerusalem" || ev["venue_name"] != "Cellar Hall" ||
		ev["session_id"] != o.sessionID.String() || ev["start_at"] == "" || !strings.HasPrefix(ev["name"].(string), "Wine Night") {
		t.Fatalf("event block = %v", body["event"])
	}
	tickets, _ := body["tickets"].([]any)
	if len(tickets) != 1 {
		t.Fatalf("tickets = %v", body["tickets"])
	}
	wantPDF := siteTestAPIBase + "/v1/public/checkout/" + o.checkoutToken + "/tickets/" + o.ticketID.String() + "/pdf"
	if got := tickets[0].(map[string]any)["pdf_url"]; got != wantPDF {
		t.Fatalf("pdf_url = %v, want %s", got, wantPDF)
	}

	for _, c := range []struct{ order, cs, want string }{
		{"refunded", "completed", "refunded"},
		{"cancelled", "completed", "cancelled"},
		{"partially_refunded", "completed", "paid"},
		{"manual_review", "manual_review", "needs_help"},
		{"pending_payment", "manual_review", "needs_help"},
		{"pending_payment", "payment_started", "pending"},
		{"expired", "expired", "expired"},
		{"abandoned", "abandoned", "failed"},
	} {
		siteExec(t, pool, `UPDATE orders SET status = $2 WHERE id = $1`, o.orderID, c.order)
		siteExec(t, pool, `UPDATE checkout_sessions SET state = $2 WHERE id = $1`, o.csID, c.cs)
		body, _ = statusBody(t, h, o.checkoutToken)
		if body["status"] != c.want {
			t.Errorf("order %s / session %s: status = %v, want %s", c.order, c.cs, body["status"], c.want)
		}
		if body["event"] == nil {
			t.Errorf("order %s: event block missing", c.order)
		}
	}
}

// ── P-7 ──────────────────────────────────────────────────────────────────────

func linkRequests(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, email string) (requests, jobs int) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM my_tickets_link_requests WHERE org_id = $1 AND email_normalized = $2`, orgID, email).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'request_id' IN
	      (SELECT id::text FROM my_tickets_link_requests WHERE org_id = $2 AND email_normalized = $3)`,
		mytickets.JobTypeLinkEmail, orgID, email).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	return
}

// mintLink plays the worker: a token for the newest request of the address.
func mintLink(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, email string, ttl time.Duration) string {
	t.Helper()
	tok, err := mytickets.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	siteExec(t, pool, `UPDATE my_tickets_link_requests SET token_hash = $3, token_expires_at = now() + $4::interval, sent_at = now()
	      WHERE id = (SELECT id FROM my_tickets_link_requests WHERE org_id = $1 AND email_normalized = $2 AND token_hash IS NULL
	                  ORDER BY created_at DESC LIMIT 1)`,
		orgID, email, mytickets.HashToken(tok), fmt.Sprintf("%d seconds", int(ttl.Seconds())))
	return tok
}

func TestSiteP7_MyTicketsByLink(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	o := seedSiteOrder(t, pool, "Buyer.One@Example.com") // stored with its own case
	other := seedSiteOrder(t, pool, "buyer.one@example.com")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1`, mytickets.JobTypeLinkEmail)
	})
	h := siteHandler(pool)
	params := map[string]string{"feed_token": o.feedToken}
	request := func(email string) int {
		return siteDo(h.HandleMyTicketsRequestLink, http.MethodPost, "/x", `{"email":"`+email+`","locale":"ru"}`, params, nil).Code
	}

	// Known and unknown addresses look the same, and only the known one
	// queues a letter.
	if c := request("buyer.one@example.com"); c != http.StatusAccepted {
		t.Fatalf("known: %d", c)
	}
	if c := request("nobody@example.com"); c != http.StatusAccepted {
		t.Fatalf("unknown: %d", c)
	}
	if r, j := linkRequests(t, pool, o.orgID, "buyer.one@example.com"); r != 1 || j != 1 {
		t.Fatalf("known: requests=%d jobs=%d", r, j)
	}
	if r, j := linkRequests(t, pool, o.orgID, "nobody@example.com"); r != 1 || j != 0 {
		t.Fatalf("unknown: requests=%d jobs=%d", r, j)
	}
	// Bad input and a bad feed token.
	if c := request("not-an-email"); c != http.StatusBadRequest {
		t.Fatalf("invalid e-mail: %d", c)
	}
	if c := siteDo(h.HandleMyTicketsRequestLink, http.MethodPost, "/x", `{"email":"a@b.co"}`, map[string]string{"feed_token": "nope"}, nil).Code; c != http.StatusNotFound {
		t.Fatalf("unknown feed token: %d", c)
	}
	// 3 letters an hour per address: the 4th is swallowed silently.
	for i := 0; i < 4; i++ {
		if c := request("buyer.one@example.com"); c != http.StatusAccepted {
			t.Fatalf("repeat %d: %d", i, c)
		}
	}
	if r, _ := linkRequests(t, pool, o.orgID, "buyer.one@example.com"); r != 3 {
		t.Fatalf("requests after the limit = %d, want 3", r)
	}

	// Exchange: an unknown and an expired token are 410; a good one works
	// once, also under concurrency.
	exch := func(tok string) (*httptest.ResponseRecorder, string) {
		w := siteDo(h.HandleMyTicketsExchange, http.MethodPost, "/x", `{"token":"`+tok+`"}`, nil, nil)
		var out struct {
			Session string `json:"session_token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out.Session
	}
	if w, _ := exch(strings.Repeat("a", 64)); w.Code != http.StatusGone {
		t.Fatalf("unknown token: %d", w.Code)
	}
	expired := mintLink(t, pool, o.orgID, "buyer.one@example.com", -time.Minute)
	if w, _ := exch(expired); w.Code != http.StatusGone {
		t.Fatalf("expired token: %d", w.Code)
	}
	good := mintLink(t, pool, o.orgID, "buyer.one@example.com", mytickets.LinkTTL)
	var wg sync.WaitGroup
	codes := make([]int, 2)
	sessions := make([]string, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, s := exch(good)
			codes[i], sessions[i] = w.Code, s
		}(i)
	}
	wg.Wait()
	ok, gone := 0, 0
	session := ""
	for i, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
			session = sessions[i]
		case http.StatusGone:
			gone++
		}
	}
	if ok != 1 || gone != 1 || len(session) != 64 {
		t.Fatalf("two concurrent exchanges: codes=%v", codes)
	}
	auth := map[string]string{"Authorization": "Bearer " + session}

	// The list shows this organization's order of this address only.
	w := siteDo(h.HandleMyTicketsOrders, http.MethodGet, "/x", "", nil, auth)
	if w.Code != 200 {
		t.Fatalf("orders: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Orders []struct {
			OrderID string `json:"order_id"`
			Status  string `json:"status"`
			Event   struct {
				Name     string `json:"name"`
				Timezone string `json:"timezone"`
			} `json:"event"`
			Tickets []struct {
				TicketID string `json:"ticket_id"`
				PDFURL   string `json:"pdf_url"`
			} `json:"tickets"`
		} `json:"orders"`
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Orders) != 1 || list.Pagination.Total != 1 || list.Orders[0].OrderID != o.orderID.String() ||
		list.Orders[0].Status != "paid" || list.Orders[0].Event.Timezone != "Asia/Jerusalem" || len(list.Orders[0].Tickets) != 1 {
		t.Fatalf("orders = %s", w.Body.String())
	}
	wantURL := siteTestAPIBase + "/v1/public/my-tickets/orders/" + o.orderID.String() + "/tickets/" + o.ticketID.String() + "/pdf"
	if list.Orders[0].Tickets[0].PDFURL != wantURL {
		t.Fatalf("pdf_url = %s, want %s", list.Orders[0].Tickets[0].PDFURL, wantURL)
	}

	// PDF: own ticket yes; another organization's order with the SAME address
	// and an order/ticket mismatch are the same 404.
	pdf := func(orderID, ticketID uuid.UUID) *httptest.ResponseRecorder {
		return siteDo(h.HandleMyTicketsPDF, http.MethodGet, "/x", "",
			map[string]string{"order_id": orderID.String(), "ticket_id": ticketID.String()}, auth)
	}
	if w := pdf(o.orderID, o.ticketID); w.Code != 200 || !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF")) {
		t.Fatalf("own pdf: %d", w.Code)
	}
	if w := pdf(other.orderID, other.ticketID); w.Code != 404 {
		t.Fatalf("foreign organization pdf: %d", w.Code)
	}
	if w := pdf(o.orderID, other.ticketID); w.Code != 404 {
		t.Fatalf("mismatched ticket pdf: %d", w.Code)
	}

	// No or wrong bearer: 401. Logout revokes on the server.
	if w := siteDo(h.HandleMyTicketsOrders, http.MethodGet, "/x", "", nil, nil); w.Code != 401 {
		t.Fatalf("no bearer: %d", w.Code)
	}
	if w := siteDo(h.HandleMyTicketsLogout, http.MethodDelete, "/x", "", nil, auth); w.Code != 204 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := siteDo(h.HandleMyTicketsOrders, http.MethodGet, "/x", "", nil, auth); w.Code != 401 {
		t.Fatalf("revoked session: %d", w.Code)
	}
	// An expired session is refused too.
	tok2 := mintLink(t, pool, o.orgID, "nobody@example.com", mytickets.LinkTTL)
	_, s2 := exch(tok2)
	siteExec(t, pool, `UPDATE my_tickets_sessions SET expires_at = now() - interval '1 minute' WHERE org_id = $1`, o.orgID)
	if w := siteDo(h.HandleMyTicketsOrders, http.MethodGet, "/x", "", nil, map[string]string{"Authorization": "Bearer " + s2}); w.Code != 401 {
		t.Fatalf("expired session: %d", w.Code)
	}
}
