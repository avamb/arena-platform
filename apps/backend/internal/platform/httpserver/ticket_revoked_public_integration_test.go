//go:build integration

// ticket_revoked_public_integration_test.go — spec 63, R-1, through the real
// router and a live database: a ticket that no longer admits never hands out
// its PDF, the order-status answer keeps its line without a code or a PDF
// link, and the offline scanner's delta lists a code revoked since the cursor
// so a device drops it, while the full snapshot leaves it out.
package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

func TestRevokedTicket_PublicPDFAndStatus(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	_, ticket := f.resendSeedOrder(f.channelA, "public_feed", "r1-public@example.test")
	var token string
	if err := f.pool.QueryRow(ctx, `
		SELECT cs.checkout_token FROM tickets t JOIN checkout_sessions cs ON cs.id = t.checkout_session_id
		WHERE t.id = $1`, ticket).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'pdf', $2)`,
		ticket, base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 r1"))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM ticket_credentials WHERE ticket_id = $1`, ticket)
	})
	get := func(path string) (int, []byte) {
		t.Helper()
		resp, err := http.Get(f.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	pdfPath := "/v1/public/checkout/" + token + "/tickets/" + ticket.String() + "/pdf"
	if code, _ := get(pdfPath); code != http.StatusOK {
		t.Fatalf("active ticket PDF: %d", code)
	}

	// The stored PDF is revoked while the ticket is still active.
	if _, err := f.pool.Exec(ctx, `UPDATE ticket_credentials SET revoked_at = now() WHERE ticket_id = $1 AND type = 'pdf'`, ticket); err != nil {
		t.Fatal(err)
	}
	if code, body := get(pdfPath); code != http.StatusGone {
		t.Fatalf("revoked PDF: %d %s", code, body)
	}

	// The ticket itself stops admitting.
	if _, err := f.pool.Exec(ctx, `UPDATE ticket_credentials SET revoked_at = NULL WHERE ticket_id = $1 AND type = 'pdf'`, ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tickets SET status = 'cancelled', cancelled_at = now() WHERE id = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	code, body := get(pdfPath)
	if code != http.StatusGone {
		t.Fatalf("cancelled ticket PDF: %d %s", code, body)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Error.Code != "checkout.ticket_not_valid" {
		t.Fatalf("error code = %q", env.Error.Code)
	}
	code, body = get("/v1/public/checkout/" + token)
	if code != http.StatusOK {
		t.Fatalf("status: %d %s", code, body)
	}
	var st struct {
		Tickets []struct {
			TicketID  string  `json:"ticket_id"`
			PDFURL    *string `json:"pdf_url"`
			HumanCode *string `json:"human_code"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Tickets) != 1 || st.Tickets[0].TicketID != ticket.String() || st.Tickets[0].PDFURL != nil || st.Tickets[0].HumanCode != nil {
		t.Fatalf("status tickets = %s", body)
	}
}

func TestRevokedBarcode_SnapshotDeltaListsItFullSnapshotDoesNot(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	_, ticket := f.resendSeedOrder(f.channelA, "public_feed", "r1-snapshot@example.test")
	var session uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT session_id FROM tickets WHERE id = $1`, ticket).Scan(&session); err != nil {
		t.Fatal(err)
	}
	code, err := ean13.Random()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO barcodes (authority_id, external_ref, ticket_id)
		VALUES ((SELECT id FROM barcode_authorities WHERE type = 'platform' ORDER BY created_at LIMIT 1), $1, $2)`, code, ticket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM barcodes WHERE external_ref = $1`, code)
	})
	q := gen.New(f.pool)
	has := func(since time.Time) (bool, string) {
		t.Helper()
		rows, err := q.ListSnapshotBarcodesBySession(ctx, session, since, 500, 0)
		if err != nil {
			t.Fatal(err)
		}
		n, err := q.CountSnapshotBarcodesBySession(ctx, session, since)
		if err != nil {
			t.Fatal(err)
		}
		if int(n) != len(rows) {
			t.Fatalf("count %d, rows %d", n, len(rows))
		}
		for _, r := range rows {
			if r.ExternalRef == code {
				return true, r.Status
			}
		}
		return false, ""
	}
	if ok, _ := has(time.Time{}); !ok {
		t.Fatal("full snapshot misses the active code")
	}
	cursor := time.Now().UTC().Add(-time.Minute)
	if _, err := f.pool.Exec(ctx, `UPDATE barcodes SET status = 'revoked', updated_at = now() WHERE external_ref = $1`, code); err != nil {
		t.Fatal(err)
	}
	if ok, _ := has(time.Time{}); ok {
		t.Fatal("full snapshot lists a revoked code")
	}
	if ok, status := has(cursor); !ok || status != "revoked" {
		t.Fatalf("delta: listed=%v status=%q, want the revoked code", ok, status)
	}
}
