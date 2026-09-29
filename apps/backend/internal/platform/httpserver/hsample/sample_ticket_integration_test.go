//go:build integration

package hsample

// The sample e-ticket of a session (migration 0119): a PDF stamped SAMPLE
// with a real EAN-13 in the 'sample' authority, minted once per session and
// reused, invisible from another organization. Run against a migrated
// database:
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci?sslmode=disable \
//	  go test -tags integration ./apps/backend/internal/platform/httpserver/hsample/

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

func samplePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type sampleFixture struct {
	orgID, otherOrgID, sessionID uuid.UUID
}

func newSampleFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) sampleFixture {
	t.Helper()
	q := gen.New(pool)
	suffix := uuid.NewString()
	org, err := q.InsertOrganization(ctx, "Sample Org "+suffix, "sample-"+suffix, "CZ", "ru", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	other, err := q.InsertOrganization(ctx, "Sample Other "+suffix, "sample-other-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization other: %v", err)
	}
	tz := "Europe/Prague"
	addr := "Kubelíkova 27"
	venue, err := q.InsertVenue(ctx, org.ID, nil, "Palác Akropolis "+suffix[:6], nil, nil, &addr, nil, nil, nil, nil, nil, &tz, nil, nil, nil, "active")
	if err != nil {
		t.Fatalf("InsertVenue: %v", err)
	}
	event, err := q.InsertEvent(ctx, org.ID, "Ночь открытой сцены "+suffix[:6], nil, "published", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	start := time.Date(2027, 3, 14, 18, 0, 0, 0, time.UTC)
	session, err := q.InsertSession(ctx, event.ID, venue.ID, start, start.Add(2*time.Hour), 100, nil, "scheduled", nil, "CZK", "derived")
	if err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	closedCap, openCap := int32(10), int32(90)
	closed := false
	if _, err := q.InsertTicketTierWithOpen(ctx, session.ID, "Closed early", "fixed", 19900, "CZK", nil, nil, &closedCap, nil, nil, 0, &closed); err != nil {
		t.Fatalf("InsertTicketTier closed: %v", err)
	}
	if _, err := q.InsertTicketTier(ctx, session.ID, "Parter", "fixed", 34900, "CZK", nil, nil, &openCap, nil, nil, 1); err != nil {
		t.Fatalf("InsertTicketTier open: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM barcodes WHERE id IN (SELECT barcode_id FROM session_sample_barcodes WHERE session_id = $1)`, session.ID)
		_, _ = pool.Exec(c, `DELETE FROM session_sample_barcodes WHERE session_id = $1`, session.ID)
		_, _ = pool.Exec(c, `DELETE FROM ticket_tiers WHERE session_id = $1`, session.ID)
		_, _ = pool.Exec(c, `DELETE FROM sessions WHERE id = $1`, session.ID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1`, event.ID)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE id = $1`, venue.ID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{org.ID, other.ID})
	})
	return sampleFixture{orgID: org.ID, otherOrgID: other.ID, sessionID: session.ID}
}

func sampleRequest(orgID, sessionID uuid.UUID, query string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/sessions/"+sessionID.String()+"/sample-ticket"+query, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	rctx.URLParams.Add("session_id", sessionID.String())
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestSampleTicket_RendersOncePerSessionWithARealSampleCode(t *testing.T) {
	pool := samplePool(t)
	ctx := context.Background()
	f := newSampleFixture(t, ctx, pool)
	q := gen.New(pool)
	h := New(q, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	rec := httptest.NewRecorder()
	h.HandleSessionSampleTicket(rec, sampleRequest(f.orgID, f.sessionID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content type = %q", ct)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("%PDF-")) {
		t.Fatalf("body is not a PDF")
	}

	// The code is a valid EAN-13 in the 'sample' authority, linked to the
	// session, carrying no ticket.
	row, err := q.GetSessionSampleBarcode(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("GetSessionSampleBarcode: %v", err)
	}
	if !ean13.Valid(row.EAN13) {
		t.Fatalf("sample code %q is not a valid EAN-13", row.EAN13)
	}
	barcode, err := q.GetBarcodeByExternalRefAny(ctx, row.EAN13)
	if err != nil || barcode.ID != row.BarcodeID || barcode.TicketID != nil {
		t.Fatalf("barcode row = %+v (%v)", barcode, err)
	}
	authority, err := q.GetBarcodeAuthorityByID(ctx, barcode.AuthorityID)
	if err != nil || authority.Type != SampleAuthorityType {
		t.Fatalf("authority = %+v (%v), want type sample", authority, err)
	}
	// The organization's default locale (ru) picks the stamp; the open
	// category's price is printed, the closed one is skipped. The
	// renderer writes text as UTF-16BE, so look for the digits of the
	// code, which are ASCII either way.
	body := rec.Body.Bytes()
	if !bytes.Contains(body, utf16be(row.EAN13[1:7])) {
		t.Fatalf("the PDF does not print the sample code")
	}
	if !bytes.Contains(body, utf16be("ОБРАЗЕЦ")) {
		t.Fatalf("the PDF is not stamped in the organization's language")
	}
	if !bytes.Contains(body, utf16be("Parter")) || bytes.Contains(body, utf16be("Closed early")) {
		t.Fatalf("the PDF should print the open category only")
	}

	// A second request reuses the same code — and an explicit locale wins.
	rec2 := httptest.NewRecorder()
	h.HandleSessionSampleTicket(rec2, sampleRequest(f.orgID, f.sessionID, "?locale=en-GB"))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d", rec2.Code)
	}
	again, err := q.GetSessionSampleBarcode(ctx, f.sessionID)
	if err != nil || again.EAN13 != row.EAN13 {
		t.Fatalf("second request minted another code: %q vs %q (%v)", again.EAN13, row.EAN13, err)
	}
	if !bytes.Contains(rec2.Body.Bytes(), utf16be("SAMPLE")) {
		t.Fatalf("locale=en-GB should stamp SAMPLE")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM barcodes WHERE external_ref = $1`, row.EAN13).Scan(&n); err != nil || n != 1 {
		t.Fatalf("barcode rows for the code = %d (%v)", n, err)
	}

	// Another organization does not see the session.
	rec3 := httptest.NewRecorder()
	h.HandleSessionSampleTicket(rec3, sampleRequest(f.otherOrgID, f.sessionID, ""))
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("foreign org status = %d, want 404", rec3.Code)
	}
}

// utf16be is how gofpdf writes text under the embedded TrueType font: two
// bytes per rune, big-endian, no BOM (see pdf.pdf_testutil_test.go).
func utf16be(s string) []byte {
	out := make([]byte, 0, 2*len(s))
	for _, r := range s {
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}
