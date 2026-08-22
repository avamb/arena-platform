// scan_ticket_status_test.go — AB-50d/AB-50e per-endpoint regression test.
//
// Verifies that POST /v1/scan rejects scan attempts for tickets in terminal
// non-active states: cancelled, revoked, transferred.
//
// The MACS scanner gate (hardware door unit) is the REAL admission control;
// this internal POST /v1/scan endpoint is internal/testing-only and MUST not
// admit tickets that MACS would reject — so the ticket status gate must be
// enforced here too.
package hbarcode

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
)

// ticketStatusFakeDB simulates the barcode + ticket DB for the status-gate test.
// It returns an "active"-status barcode with a TicketID set, then when
// GetTicketByID is called, returns a ticket with the configured status.
//
// Reuses scanRow from scan_human_code_test.go (same package).
type ticketStatusFakeDB struct {
	authorityID  uuid.UUID
	barcodeID    uuid.UUID
	ticketID     uuid.UUID
	ticketStatus string // the status to return from GetTicketByID
}

func (f *ticketStatusFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *ticketStatusFakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("ticketStatusFakeDB: unexpected Query")
}

func (f *ticketStatusFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	ticketIDPtr := f.ticketID // local copy for pointer safety
	switch {
	case strings.Contains(sql, "FROM   barcode_authorities"):
		return &scanRow{vals: []any{
			f.authorityID, "platform", "Platform Authority", time.Now(),
		}}

	case strings.Contains(sql, "FROM   barcodes"):
		return &scanRow{vals: []any{
			f.barcodeID, f.authorityID, "test-barcode-ref",
			&ticketIDPtr, // *uuid.UUID — TicketID is set
			"active",     // barcode.Status is active (so we reach the ticket check)
			(*time.Time)(nil), time.Now(), time.Now(),
		}}

	case strings.Contains(sql, "FROM   tickets"):
		// GetTicketByID — return a ticket with the configured non-active status.
		return &scanRow{vals: []any{
			f.ticketID, uuid.New(), uuid.New(), // ID, CheckoutSessionID, SessionID
			(*uuid.UUID)(nil), (*string)(nil), // TierID, HolderEmail
			f.ticketStatus,                     // Status — what we're testing
			time.Now(), time.Now(), time.Now(), // IssuedAt, CreatedAt, UpdatedAt
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), // seat fields
			int32(0),          // Ordinal
			(*time.Time)(nil), // CancelledAt
			(*string)(nil),    // CancellationReason
			(*string)(nil),    // RefundMode
			(*uuid.UUID)(nil), // RefundID
			(*time.Time)(nil), // RefundDate
			(*int64)(nil),     // RefundPrice
			false,             // ReviewHold
			(*string)(nil),    // ReviewHoldReason
			int64(12345),      // SystemTicketID
		}}

	case strings.Contains(sql, "FROM   ticket_credentials"):
		// human-code fallback — not triggered in this test
		return &scanRow{err: pgx.ErrNoRows}

	case strings.Contains(sql, "UPDATE barcodes"):
		// MarkBarcodeScanned — should NOT be reached for non-active tickets.
		return &scanRow{err: fmt.Errorf("MarkBarcodeScanned must not be called for a %s ticket", f.ticketStatus)}
	}
	return &scanRow{err: fmt.Errorf("ticketStatusFakeDB: unexpected SQL: %q", sql)}
}

// TestHandleScan_TicketStatusGate checks that POST /v1/scan returns 409
// barcode.ticket_not_admissible when the underlying ticket is in a terminal
// state (cancelled, revoked, transferred).
func TestHandleScan_TicketStatusGate(t *testing.T) {
	statuses := []string{"cancelled", "revoked", "transferred"}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			db := &ticketStatusFakeDB{
				authorityID:  uuid.New(),
				barcodeID:    uuid.New(),
				ticketID:     uuid.New(),
				ticketStatus: status,
			}
			q := gen.New(db)
			h := New(q, q, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

			body := `{"external_ref":"test-barcode-ref","authority_type":"platform"}`
			req := httptest.NewRequest(http.MethodPost, "/v1/scan", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.HandleScan(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status %s: HTTP status = %d, want 409; body: %s",
					status, rec.Code, rec.Body.String())
			}

			var resp map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("status %s: decode response: %v", status, err)
			}
			errObj, _ := resp["error"].(map[string]any)
			code, _ := errObj["code"].(string)
			if code != "barcode.ticket_not_admissible" {
				t.Errorf("status %s: error.code = %q, want %q", status, code, "barcode.ticket_not_admissible")
			}
		})
	}
}
