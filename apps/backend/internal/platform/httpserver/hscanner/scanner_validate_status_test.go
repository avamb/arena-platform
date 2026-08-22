// scanner_validate_status_test.go — AB-50d/AB-50e per-endpoint regression.
//
// Verifies that POST /v1/scanner/validate returns valid=false with
// invalid_reason="ticket_<status>" when the underlying ticket is in a
// terminal non-active state (cancelled, revoked, transferred).
//
// This is the status gate added in the AB-50d pass-6 review. The physical
// door uses MACS; this internal endpoint must mirror its admission logic so
// that testing tools report the correct answer.
package hscanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// hscannerTestRow implements pgx.Row using reflect — safely handles typed nil
// pointer values (e.g. *uuid.UUID(nil)) that appear in the scan destination
// as **uuid.UUID when scanned via &field.
type hscannerTestRow struct {
	vals []any
	err  error
}

func (r *hscannerTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.vals) {
		return fmt.Errorf("hscannerTestRow: %d destinations for %d values", len(dest), len(r.vals))
	}
	for i, v := range r.vals {
		if v == nil {
			continue
		}
		rv := reflect.ValueOf(v)
		dv := reflect.ValueOf(dest[i]).Elem()
		// For nil pointer values (non-nil interface containing nil pointer),
		// set the destination to the zero value of its type.
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			dv.Set(reflect.Zero(dv.Type()))
			continue
		}
		dv.Set(rv)
	}
	return nil
}

// fakeRateLimiter always allows requests.
type fakeRateLimiter struct{}

func (fakeRateLimiter) CheckIP(string) bool      { return true }
func (fakeRateLimiter) CheckSession(string) bool { return true }

// validateStatusFakeDB simulates the barcode + ticket DB for POST /v1/scanner/validate.
// Returns an active barcode with a TicketID, then a ticket with the configured status.
type validateStatusFakeDB struct {
	authorityID  uuid.UUID
	barcodeID    uuid.UUID
	ticketID     uuid.UUID
	ticketStatus string
}

func (f *validateStatusFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *validateStatusFakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("validateStatusFakeDB: unexpected Query")
}

func (f *validateStatusFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	ticketIDPtr := f.ticketID
	switch {
	case strings.Contains(sql, "FROM   barcode_authorities"):
		return &hscannerTestRow{vals: []any{
			f.authorityID, "platform", "Platform Authority", time.Now(),
		}}

	case strings.Contains(sql, "FROM   barcodes"):
		return &hscannerTestRow{vals: []any{
			f.barcodeID, f.authorityID, "test-ref",
			&ticketIDPtr, // *uuid.UUID set → TicketID present
			"active",     // barcode.Status active
			(*time.Time)(nil), time.Now(), time.Now(),
		}}

	case strings.Contains(sql, "FROM   tickets"):
		// GetTicketByID — returns the configured non-active status.
		return &hscannerTestRow{vals: []any{
			f.ticketID, uuid.New(), uuid.New(),
			(*uuid.UUID)(nil), (*string)(nil),
			f.ticketStatus,
			time.Now(), time.Now(), time.Now(),
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			int32(0),
			(*time.Time)(nil), (*string)(nil), (*string)(nil), (*uuid.UUID)(nil),
			(*time.Time)(nil), (*int64)(nil),
			false, (*string)(nil),
			int64(12345),
		}}

	case strings.Contains(sql, "FROM   ticket_credentials"):
		return &hscannerTestRow{err: pgx.ErrNoRows}
	}
	return &hscannerTestRow{err: fmt.Errorf("validateStatusFakeDB: unexpected SQL: %q", sql)}
}

// TestHandleScannerValidate_TicketStatusGate checks that /v1/scanner/validate
// returns valid=false with the right invalid_reason when the ticket is in a
// terminal state (cancelled, revoked, transferred).
func TestHandleScannerValidate_TicketStatusGate(t *testing.T) {
	statuses := []string{"cancelled", "revoked", "transferred"}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			db := &validateStatusFakeDB{
				authorityID:  uuid.New(),
				barcodeID:    uuid.New(),
				ticketID:     uuid.New(),
				ticketStatus: status,
			}
			q := gen.New(db)
			h := New(q, q, nil, nil, fakeRateLimiter{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

			body := `{"external_ref":"test-ref","authority_type":"platform"}`
			req := httptest.NewRequest(http.MethodPost, "/v1/scanner/validate", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.HandleScannerValidate(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status %s: HTTP code = %d, want 200; body: %s",
					status, rec.Code, rec.Body.String())
			}

			var resp struct {
				Valid         bool   `json:"valid"`
				InvalidReason string `json:"invalid_reason"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("status %s: decode: %v", status, err)
			}
			if resp.Valid {
				t.Errorf("status %s: valid = true, want false (ticket is %s)", status, status)
			}
			wantReason := "ticket_" + status
			if resp.InvalidReason != wantReason {
				t.Errorf("status %s: invalid_reason = %q, want %q", status, resp.InvalidReason, wantReason)
			}
		})
	}
}
