//go:build integration

// revoked_pdf_integration_test.go — spec 63, R-1: a revoked PDF credential is
// never attached again. Its barcode no longer admits, so the letter draws the
// ticket afresh and, for an active ticket, stores the fresh PDF in its place.
// Same fixture and DATABASE_URL pattern as order_resend_integration_test.go.
package delivery

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestRevokedPDFCredentialIsNeverAttached(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedOrderOfThree(ctx, t, pool)
	defer cleanup()

	if _, err := pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent', sent_at = now() WHERE id = ANY($1)`, seed.JobIDs); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	stale := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 stale r1"))
	target := seed.TicketIDs[0]
	if _, err := pool.Exec(ctx, `INSERT INTO ticket_credentials (ticket_id, type, payload, revoked_at)
		VALUES ($1, 'pdf', $2, now())
		ON CONFLICT (ticket_id, type) DO UPDATE SET payload = EXCLUDED.payload, revoked_at = now()`, target, stale); err != nil {
		t.Fatalf("stale credential: %v", err)
	}
	addr := "r1-" + seed.OrderID.String()[:8] + "@example.org"
	requeueOrder(ctx, t, gen.New(pool), seed.TicketIDs[:1], addr)

	raw := runDeliveryWithPayload(ctx, t, pool, Payload{TicketID: target.String(), Locale: "en"})
	if names := attachmentNames(t, raw); len(names) != 1 {
		t.Fatalf("attachments = %v, want the one ticket", names)
	}
	if strings.Contains(raw, stale) {
		t.Fatal("the revoked PDF was attached")
	}
	var payload string
	var revoked bool
	if err := pool.QueryRow(ctx, `SELECT payload, revoked_at IS NOT NULL FROM ticket_credentials WHERE ticket_id = $1 AND type = 'pdf'`,
		target).Scan(&payload, &revoked); err != nil {
		t.Fatal(err)
	}
	if payload == stale || revoked {
		t.Fatalf("the active ticket keeps the revoked PDF (revoked=%v)", revoked)
	}
}
