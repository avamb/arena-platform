// scanner_validate_ean13_503_test.go — feature #503 (W1-B6b, spec §11):
// POST /v1/scanner/validate must resolve a ticket by its platform-minted
// EAN-13 barcode number.
//
// HandleScannerValidate resolves purely by (authority_type, external_ref)
// via GetBarcodeByRef — it has no special-casing per code format, so an
// EAN-13 code under authority_type=platform (minted by feature #502's
// issuance path or feature #503's tickets.backfill_ean13 job) already
// resolves like any other platform barcode. This test proves that with a
// fake DB that only returns the fixture barcode row when the incoming
// external_ref exactly matches the EAN-13 code — so the test actually
// exercises ref plumbing, not just a fixed-response stub.
package hscanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// ean13ValidateFakeDB simulates the barcode_authorities + barcodes +
// tickets tables for POST /v1/scanner/validate, matching the barcodes
// lookup against the exact external_ref it was constructed with (unlike
// validateStatusFakeDB, which returns a fixed row regardless of args) so
// this test genuinely proves the EAN-13 string round-trips through
// GetBarcodeByRef's $2 parameter.
type ean13ValidateFakeDB struct {
	authorityID uuid.UUID
	barcodeID   uuid.UUID
	ticketID    uuid.UUID
	externalRef string
}

func (f *ean13ValidateFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *ean13ValidateFakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("ean13ValidateFakeDB: unexpected Query")
}

func (f *ean13ValidateFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	ticketIDPtr := f.ticketID
	switch {
	case strings.Contains(sql, "FROM   barcode_authorities"):
		return &hscannerTestRow{vals: []any{
			f.authorityID, "platform", "Platform Authority", time.Now(),
		}}

	case strings.Contains(sql, "FROM   barcodes"):
		ref, _ := args[len(args)-1].(string)
		if ref != f.externalRef {
			return &hscannerTestRow{err: pgx.ErrNoRows}
		}
		return &hscannerTestRow{vals: []any{
			f.barcodeID, f.authorityID, f.externalRef,
			&ticketIDPtr, // *uuid.UUID set → TicketID present
			"active",     // barcode.Status active
			(*time.Time)(nil), time.Now(), time.Now(),
		}}

	case strings.Contains(sql, "FROM   tickets"):
		return &hscannerTestRow{vals: []any{
			f.ticketID, uuid.New(), uuid.New(),
			(*uuid.UUID)(nil), (*string)(nil),
			"active", // ticket.Status active
			time.Now(), time.Now(), time.Now(),
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			int32(0),
			(*time.Time)(nil), (*string)(nil), (*string)(nil), (*uuid.UUID)(nil),
			(*time.Time)(nil), (*int64)(nil),
			false, (*string)(nil),
			int64(4242),
		}}
	}
	return &hscannerTestRow{err: fmt.Errorf("ean13ValidateFakeDB: unexpected SQL: %q", sql)}
}

// TestHandleScannerValidate_ResolvesByEAN13 proves /v1/scanner/validate
// returns valid=true for an active platform-authority barcode whose
// external_ref is a checksum-valid EAN-13 code.
func TestHandleScannerValidate_ResolvesByEAN13(t *testing.T) {
	eanCode := ean13.Encode("21", 4242)
	if !ean13.Valid(eanCode) {
		t.Fatalf("test fixture ean13.Encode(21, 4242) = %q is not checksum-valid", eanCode)
	}

	db := &ean13ValidateFakeDB{
		authorityID: uuid.New(),
		barcodeID:   uuid.New(),
		ticketID:    uuid.New(),
		externalRef: eanCode,
	}
	q := gen.New(db)
	h := New(q, q, nil, nil, fakeRateLimiter{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	body := `{"external_ref":"` + eanCode + `","authority_type":"platform"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/scanner/validate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleScannerValidate(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP code = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Valid       bool   `json:"valid"`
		Status      string `json:"status"`
		ExternalRef string `json:"external_ref"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Valid {
		t.Errorf("valid = false, want true for an active platform EAN-13 barcode")
	}
	if resp.Status != "active" {
		t.Errorf("status = %q, want %q", resp.Status, "active")
	}
	if resp.ExternalRef != eanCode {
		t.Errorf("external_ref = %q, want %q", resp.ExternalRef, eanCode)
	}

	// A different EAN-13 (unregistered) must not resolve to this fixture.
	other := ean13.Encode("21", 9999)
	db2 := &ean13ValidateFakeDB{
		authorityID: db.authorityID,
		barcodeID:   db.barcodeID,
		ticketID:    db.ticketID,
		externalRef: eanCode, // fixture still keyed to the original code
	}
	q2 := gen.New(db2)
	h2 := New(q2, q2, nil, nil, fakeRateLimiter{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body2 := `{"external_ref":"` + other + `","authority_type":"platform"}`
	req2 := httptest.NewRequest(http.MethodPost, "/v1/scanner/validate", strings.NewReader(body2))
	rec2 := httptest.NewRecorder()
	h2.HandleScannerValidate(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("unregistered EAN-13: HTTP code = %d, want 404; body: %s", rec2.Code, rec2.Body.String())
	}
}
