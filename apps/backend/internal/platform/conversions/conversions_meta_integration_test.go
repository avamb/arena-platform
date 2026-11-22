//go:build integration

package conversions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

// TestConversions_MetaConnectionToken_LiveDB (spec 66 section 4.4): an
// organization whose only Meta source is its connection gets a meta delivery
// queued, the sweep sends it with the DECRYPTED connection token to the stub
// Graph at the configured version path, and a 190 answer fails the delivery
// for good and moves the connection to error. Without a box the connection
// is ignored.
func TestConversions_MetaConnectionToken_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	box, err := secretbox.ParseKeys("v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32))))
	if err != nil {
		t.Fatal(err)
	}
	const (
		plainToken = "EAAB-conn-token-plaintext-42"
		pixelID    = "880011223344"
		version    = "v25.0"
	)

	var (
		orgID, venueID, evtID, sessID, chanID = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		nonce                                 = orgID.String()[:8]
	)
	defer func() {
		for _, sql := range []string{
			`DELETE FROM orders WHERE org_id = $1`,
			`DELETE FROM checkout_sessions WHERE org_id = $1`,
			`DELETE FROM reservations WHERE org_id = $1`,
			`DELETE FROM sales_channels WHERE org_id = $1`,
			`DELETE FROM sessions WHERE venue_id IN (SELECT id FROM venues WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(context.Background(), sql, orgID); err != nil {
				t.Logf("cleanup %q: %v", sql, err)
			}
		}
	}()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "Meta Conv Org "+nonce, "mconv-"+nonce)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, venueID, orgID, "Meta Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, evtID, orgID, "Meta Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'EUR', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO sales_channels (id, org_id, name, payment_mode, provider)
	      VALUES ($1, $2, $3, 'direct_merchant', 'stripe')`, chanID, orgID, "Channel "+nonce)
	newOrder := func() uuid.UUID {
		resvID, csID, orderID := uuid.New(), uuid.New(), uuid.New()
		exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
		      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resvID, orgID, chanID, sessID)
		exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
		      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)
		exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
		                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, buyer_phone)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'B', 'b@example.test', '+34600000000')`,
			orderID, orgID, chanID, evtID, sessID, csID, resvID)
		exec(`INSERT INTO checkout_buyer_contexts (checkout_session_id, order_id, context)
		      VALUES ($1, $2, '{"fbp":"fb.1.1.2","client_ip":"203.0.113.9","user_agent":"UA"}')`, csID, orderID)
		exec(`UPDATE checkout_sessions SET tracking_consent_granted = true, tracking_consent_version = 'v1',
		             tracking_consent_at = now() - interval '1 hour', tracking_consent_received_at = now() WHERE id = $1`, csID)
		return orderID
	}

	// The connection: active, token sealed for this org, one selected dataset.
	// No org_conversion_settings row at all.
	sealed, err := metaconnect.SealToken(box, orgID, plainToken)
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO org_meta_connections (org_id, meta_user_id, access_token_enc, status) VALUES ($1, $2, $3, 'active')`,
		orgID, "mu-"+nonce, sealed)
	exec(`INSERT INTO org_meta_assets (org_id, kind, meta_id, name, selected) VALUES ($1, 'dataset', $2, 'Pixel', true)`,
		orgID, pixelID)

	count := func(where string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversion_deliveries WHERE org_id = $1 `+where, orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Without a box the connection is ignored: nothing is queued.
	order1 := newOrder()
	plain := NewPGStore(pool)
	_ = NewDispatcher(plain, slog.Default()).Dispatch(ctx, outbox.Event{EventType: EventOrderPaid, AggregateID: order1.String()})
	if n := count(""); n != 0 {
		t.Fatalf("queued without a box: %d", n)
	}
	if _, ok, err := plain.Settings(ctx, orgID); ok || err != nil {
		t.Fatalf("Settings without a box: ok=%v err=%v", ok, err)
	}

	store := NewPGStore(pool).WithMetaBox(box)
	leg := NewDispatcher(store, slog.Default())
	_ = leg.Dispatch(ctx, outbox.Event{EventType: EventOrderPaid, AggregateID: order1.String()})
	if n := count(`AND provider = 'meta'`); n != 1 {
		t.Fatalf("meta deliveries = %d, want 1 (connection only)", n)
	}
	if n := count(`AND provider = 'ga4'`); n != 0 {
		t.Fatalf("ga4 queued without GA4 settings: %d", n)
	}

	var (
		mu       sync.Mutex
		paths    []string
		bodies   []map[string]any
		answer   = func(w http.ResponseWriter) { w.WriteHeader(200) }
		answerMu sync.Mutex
	)
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, m)
		mu.Unlock()
		answerMu.Lock()
		a := answer
		answerMu.Unlock()
		a(w)
	}))
	defer graph.Close()
	senders := Senders{Meta: MetaSender{BaseURL: graph.URL, Version: version}}

	exec(`UPDATE conversion_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE org_id = $1`, orgID)
	for i := 0; i < 3; i++ {
		if _, err := RunOnce(ctx, store, senders, slog.Default(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(`AND status = 'sent'`); n != 1 {
		t.Fatalf("sent = %d, want 1", n)
	}
	mu.Lock()
	if len(paths) != 1 || paths[0] != "/"+version+"/"+pixelID+"/events" {
		t.Fatalf("paths = %v", paths)
	}
	if bodies[0]["access_token"] != plainToken || bodies[0]["partner_agent"] != PartnerAgent {
		t.Fatalf("body token/partner_agent wrong: partner_agent=%v", bodies[0]["partner_agent"])
	}
	mu.Unlock()

	// A 190 answer: the delivery fails at once, the connection goes to error.
	answerMu.Lock()
	answer = func(w http.ResponseWriter) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token ` + plainToken + `","type":"OAuthException","code":190,"error_subcode":460}}`))
	}
	answerMu.Unlock()
	order2 := newOrder()
	_ = leg.Dispatch(ctx, outbox.Event{EventType: EventOrderPaid, AggregateID: order2.String()})
	exec(`UPDATE conversion_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE org_id = $1 AND status = 'pending'`, orgID)
	if _, err := RunOnce(ctx, store, senders, slog.Default(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var status, lastErr string
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT status, COALESCE(last_error, ''), attempts FROM conversion_deliveries WHERE order_id = $1`, order2).
		Scan(&status, &lastErr, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 1 || !strings.Contains(lastErr, "graph_190") {
		t.Fatalf("delivery after 190: %s attempts=%d %q", status, attempts, lastErr)
	}
	if strings.Contains(lastErr, plainToken) || strings.Contains(lastErr, "Invalid OAuth") {
		t.Fatalf("last_error leaks: %q", lastErr)
	}
	var connStatus, connErr string
	if err := pool.QueryRow(ctx, `SELECT status, COALESCE(last_error, '') FROM org_meta_connections WHERE org_id = $1`, orgID).
		Scan(&connStatus, &connErr); err != nil {
		t.Fatal(err)
	}
	if connStatus != metaconnect.StatusError || connErr != "graph_190" {
		t.Fatalf("connection after 190: %s %q", connStatus, connErr)
	}
	// An error connection is no longer a source: a new order queues nothing.
	order3 := newOrder()
	_ = leg.Dispatch(ctx, outbox.Event{EventType: EventOrderPaid, AggregateID: order3.String()})
	var n3 int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM conversion_deliveries WHERE order_id = $1`, order3).Scan(&n3)
	if n3 != 0 {
		t.Fatalf("queued for an error connection: %d", n3)
	}
}

// TestConversions_MetaConnectionNoAccess_LiveDB: a 10 / 200-299 answer fails
// only that delivery; the connection stays active with the reason recorded.
func TestConversions_MetaConnectionNoAccess_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	box, _ := secretbox.ParseKeys("v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("n", 32))))
	orgID := uuid.New()
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID) }()
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgID, "NoAccess "+orgID.String()[:8], "noacc-"+orgID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	sealed, _ := metaconnect.SealToken(box, orgID, "tok-noaccess")
	if _, err := pool.Exec(ctx, `INSERT INTO org_meta_connections (org_id, meta_user_id, access_token_enc) VALUES ($1, 'mu', $2)`, orgID, sealed); err != nil {
		t.Fatal(err)
	}
	store := NewPGStore(pool).WithMetaBox(box)
	if err := store.RecordMetaError(ctx, orgID, metaconnect.ErrorCode(&metaconnect.GraphError{Code: 200})); err != nil {
		t.Fatal(err)
	}
	var status, lastErr string
	_ = pool.QueryRow(ctx, `SELECT status, COALESCE(last_error, '') FROM org_meta_connections WHERE org_id = $1`, orgID).Scan(&status, &lastErr)
	if status != metaconnect.StatusActive || lastErr != "graph_200" {
		t.Fatalf("after RecordMetaError: %s %q", status, lastErr)
	}
}
