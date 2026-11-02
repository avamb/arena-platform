//go:build integration

// ticket_access_integration_test.go — live-DB proof of the buyer's ticket
// access log (migration 0184, spec 51 decision R-13): the ticket e-mail links
// each ticket's PDF with its real checkout token and ?src=email, and the
// access log stores a download with the IP in the clear while a repeated view
// from the same address within ten minutes is written once.
//
// Same database rules as presentation_integration_test.go: DATABASE_URL, a
// FRESH scratch database, skipped when unset.
package delivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const testAPIBaseURL = "https://api.example"

func runDeliveryWithAPIBase(ctx context.Context, t *testing.T, pool *pgxpool.Pool, p Payload, apiBase string) string {
	t.Helper()
	smtp := newSMTPCaptureServer(t)
	queries := gen.New(pool)
	handler := NewHandler(HandlerOptions{
		TicketQueries:      queries,
		DeliveryJobQueries: queries,
		CredentialQueries:  queries,
		Sender:             buildSMTPSender(smtp.Addr),
		TicketsBaseURL:     testTicketsBaseURL,
		APIBaseURL:         apiBase,
		Logger:             testLogger(),
	})
	if err := handler(ctx, mustJSON(t, p)); err != nil {
		t.Fatalf("delivery handler: %v", err)
	}
	select {
	case raw := <-smtp.Captured:
		return string(raw)
	case <-time.After(10 * time.Second):
		t.Fatal("SMTP capture server received no message within 10s")
		return ""
	}
}

func checkoutOfTicket(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ticketID uuid.UUID) (csID, orgID uuid.UUID, token string) {
	t.Helper()
	err := pool.QueryRow(ctx, `
		SELECT cs.id, cs.org_id, cs.checkout_token
		FROM   tickets t JOIN checkout_sessions cs ON cs.id = t.checkout_session_id
		WHERE  t.id = $1`, ticketID).Scan(&csID, &orgID, &token)
	if err != nil {
		t.Fatalf("checkout of ticket: %v", err)
	}
	return csID, orgID, token
}

func TestTicketAccess_LetterLinksEachTicketWithItsCheckoutToken(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()
	_, _, token := checkoutOfTicket(ctx, t, pool, seed.TicketID)

	raw := runDeliveryWithAPIBase(ctx, t, pool, Payload{TicketID: seed.TicketID.String(), Locale: "en"}, testAPIBaseURL)
	text := emailTextBody(t, raw)

	want := testAPIBaseURL + "/v1/public/checkout/" + token + "/tickets/" + seed.TicketID.String() + "/pdf?src=email"
	if !strings.Contains(text, "Download PDF: "+want) {
		t.Errorf("text part lacks the download link %q:\n%s", want, text)
	}
}

func TestTicketAccess_NoAPIBaseMeansNoLink(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	raw := runDeliveryWithAPIBase(ctx, t, pool, Payload{TicketID: seed.TicketID.String(), Locale: "en"}, "")
	if text := emailTextBody(t, raw); strings.Contains(text, "Download PDF") || strings.Contains(text, "src=email") {
		t.Errorf("a worker without an API address must not link the PDF:\n%s", text)
	}
}

func TestTicketAccess_LogStoresDownloadsAndDedupsViews(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()
	csID, orgID, _ := checkoutOfTicket(ctx, t, pool, seed.TicketID)
	q := gen.New(pool)

	download := gen.InsertTicketPDFDownloadParams{
		CheckoutSessionID: csID, TicketID: seed.TicketID, OrgID: orgID,
		Source: "email", IP: "2001:db8::7", UserAgent: "Mail/1.0",
	}
	if wrote, err := q.InsertTicketPDFDownload(ctx, download); err != nil || !wrote {
		t.Fatalf("first download: wrote=%v err=%v", wrote, err)
	}
	if wrote, err := q.InsertTicketPDFDownload(ctx, download); err != nil || wrote {
		t.Fatalf("repeat download within a minute: wrote=%v err=%v, want no row", wrote, err)
	}
	fromPage := download
	fromPage.Source = "web"
	if wrote, err := q.InsertTicketPDFDownload(ctx, fromPage); err != nil || !wrote {
		t.Fatalf("download from the page: wrote=%v err=%v", wrote, err)
	}
	// A mail scanner behind the same address never hides the buyer's own
	// download, and the buyer's never hides it.
	scanner := download
	scanner.UserAgent = "Mimecast URL Protect"
	scanner.LikelyAutomated = true
	if wrote, err := q.InsertTicketPDFDownload(ctx, scanner); err != nil || !wrote {
		t.Fatalf("scanner download: wrote=%v err=%v", wrote, err)
	}
	view := gen.InsertTicketsViewParams{CheckoutSessionID: csID, OrgID: orgID, Source: "web", IP: "203.0.113.9", UserAgent: "Widget/1.0"}
	if wrote, err := q.InsertTicketsView(ctx, view); err != nil || !wrote {
		t.Fatalf("first view: wrote=%v err=%v", wrote, err)
	}
	if wrote, err := q.InsertTicketsView(ctx, view); err != nil || wrote {
		t.Fatalf("repeat view from the same address: wrote=%v err=%v, want no row", wrote, err)
	}
	other := view
	other.IP = "198.51.100.4"
	if wrote, err := q.InsertTicketsView(ctx, other); err != nil || !wrote {
		t.Fatalf("view from another address: wrote=%v err=%v", wrote, err)
	}
	unknown := view
	unknown.IP = ""
	if wrote, err := q.InsertTicketsView(ctx, unknown); err != nil || !wrote {
		t.Fatalf("view from an unknown address: wrote=%v err=%v", wrote, err)
	}
	if wrote, err := q.InsertTicketsView(ctx, unknown); err != nil || wrote {
		t.Fatalf("repeat view from an unknown address: wrote=%v err=%v, want no row", wrote, err)
	}

	rows, err := q.ListTicketAccessEventsByCheckout(ctx, csID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("rows = %d, want 3 downloads + 3 views", len(rows))
	}
	d := rows[0]
	if d.Action != "pdf_download" || d.Source != "email" || d.TicketID == nil || *d.TicketID != seed.TicketID ||
		d.IP == nil || *d.IP != "2001:db8::7" || d.UserAgent == nil || *d.UserAgent != "Mail/1.0" || d.OrgID != orgID ||
		d.LikelyAutomated {
		t.Errorf("download row = %+v", d)
	}
	var views, automated int
	for _, r := range rows {
		if r.LikelyAutomated {
			automated++
		}
		if r.Action == "tickets_view" {
			views++
			if r.TicketID != nil {
				t.Errorf("a view of the whole order names ticket %s", r.TicketID)
			}
		}
	}
	if views != 3 {
		t.Errorf("views = %d, want 3", views)
	}
	if automated != 1 {
		t.Errorf("automated rows = %d, want only the scanner's", automated)
	}

	// The rows go with their checkout: the fixture's own cleanup deletes the
	// checkout, so nothing is left behind and no FK stops it.
}
