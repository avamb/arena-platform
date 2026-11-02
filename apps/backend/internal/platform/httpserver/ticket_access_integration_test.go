//go:build integration

// ticket_access_integration_test.go — the buyer's ticket access log (migration
// 0184, spec 51 decision R-13) through the real router: downloading a ticket
// PDF writes a 'pdf_download' row with the source the link named, and the
// order-status answer that lists the tickets writes one 'tickets_view' row,
// not one per poll.
package httpserver

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestTicketAccessLog_PublicRoutesRecordDownloadsAndViews(t *testing.T) {
	f := newECOrdersFixture(t)
	ctx := context.Background()
	_, ticket := f.resendSeedOrder(f.channelA, "public_feed", "access-log@example.test")

	var token string
	if err := f.pool.QueryRow(ctx, `
		SELECT cs.checkout_token FROM tickets t JOIN checkout_sessions cs ON cs.id = t.checkout_session_id
		WHERE t.id = $1`, ticket).Scan(&token); err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4 access log test")
	if _, err := f.pool.Exec(ctx, `INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'pdf', $2)`,
		ticket, base64.StdEncoding.EncodeToString(pdf)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM ticket_credentials WHERE ticket_id = $1`, ticket)
	})

	const buyerUA = "Mozilla/5.0 (X11; Linux x86_64) AccessLogTest/1.0"
	getAs := func(path, ua string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, f.ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("User-Agent", ua)
		if ua == buyerUA {
			req.Header.Set("Accept-Language", "es-ES,es;q=0.9")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	get := func(path string) (int, []byte) { return getAs(path, buyerUA) }

	pdfPath := "/v1/public/checkout/" + token + "/tickets/" + ticket.String() + "/pdf"
	// The buyer's mail security opens the link first.
	if code, _ := getAs(pdfPath+"?src=email", "Mozilla/5.0 Mimecast URL Protect"); code != http.StatusOK {
		t.Fatalf("PDF for the scanner: %d", code)
	}
	if code, body := get(pdfPath + "?src=email"); code != http.StatusOK || string(body) != string(pdf) {
		t.Fatalf("PDF from the letter: %d %q", code, body)
	}
	if code, _ := get(pdfPath); code != http.StatusOK {
		t.Fatalf("PDF from the page: %d", code)
	}
	for i := 0; i < 3; i++ { // the widget polls
		if code, body := get("/v1/public/checkout/" + token); code != http.StatusOK {
			t.Fatalf("status %d: %d %s", i, code, body)
		}
	}

	var cs uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT checkout_session_id FROM tickets WHERE id = $1`, ticket).Scan(&cs); err != nil {
		t.Fatal(err)
	}
	rows, err := gen.New(f.pool).ListTicketAccessEventsByCheckout(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	var downloads, views, automated int
	sources := map[string]int{}
	for _, r := range rows {
		if r.LikelyAutomated {
			automated++
			if r.Source != "email" || r.Action != "pdf_download" {
				t.Errorf("automated row = %s/%s, want the scanner's e-mail download", r.Source, r.Action)
			}
			continue
		}
		if r.OrgID != f.orgA {
			t.Errorf("row org = %s, want %s", r.OrgID, f.orgA)
		}
		if r.IP == nil || *r.IP == "" {
			t.Errorf("row %s has no client address", r.Action)
		}
		if r.UserAgent == nil || *r.UserAgent != buyerUA {
			t.Errorf("row %s user agent = %v", r.Action, r.UserAgent)
		}
		switch r.Action {
		case "pdf_download":
			downloads++
			sources[r.Source]++
			if r.TicketID == nil || *r.TicketID != ticket {
				t.Errorf("download row names ticket %v, want %s", r.TicketID, ticket)
			}
		case "tickets_view":
			views++
		}
	}
	if downloads != 2 || sources["email"] != 1 || sources["web"] != 1 {
		t.Errorf("downloads = %d by source %v, want one from the letter and one from the page", downloads, sources)
	}
	if automated != 1 {
		t.Errorf("automated rows = %d, want the scanner's one", automated)
	}
	if views != 1 {
		t.Errorf("views = %d after three polls, want 1", views)
	}
}
