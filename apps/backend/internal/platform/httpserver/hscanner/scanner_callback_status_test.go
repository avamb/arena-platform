// scanner_callback_status_test.go — AB-50d/AB-50e per-endpoint regression.
//
// Verifies that POST /v1/scanner/scan-events rejects scan attempts for
// tickets in terminal states (cancelled, revoked, transferred).
//
// The admission gate lives at the ResolveScanCredentialByTicketQR result:
// when TicketStatus is one of {cancelled, revoked, transferred}, the
// processScannerScan helper sets result.Error and returns early — no
// scan_event row is inserted, so the side-effects (used_at, outbox) are
// also suppressed.
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
)

// callbackStatusFakeDB implements gen.DBTX for the scan-events status gate.
// It returns a valid feed token scope, then a ticket in the configured
// terminal status when ResolveScanCredentialByTicketQR is called.
type callbackStatusFakeDB struct {
	orgID        uuid.UUID
	channelID    uuid.UUID
	ticketID     uuid.UUID
	sessionID    uuid.UUID
	eventID      uuid.UUID
	ticketStatus string
}

func (f *callbackStatusFakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	// TouchFeedTokenLastUsed is a best-effort UPDATE — no-op it.
	if strings.Contains(sql, "agent_feed_tokens") {
		return pgconn.CommandTag{}, nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("callbackStatusFakeDB: unexpected Exec: %q", sql)
}

func (f *callbackStatusFakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("callbackStatusFakeDB: unexpected Query")
}

func (f *callbackStatusFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM   agent_feed_tokens"):
		// ResolveFeedTokenScannerScope
		return &hscannerTestRow{vals: []any{
			uuid.New(), f.channelID, f.orgID,
		}}

	case strings.Contains(sql, "FROM   ticket_credentials"):
		// ResolveScanCredentialByTicketQR — return ticket in terminal state.
		// Scan args: TicketID, SessionID, EventID, OrgID, TicketStatus, TicketUsedAt
		return &hscannerTestRow{vals: []any{
			f.ticketID, f.sessionID, f.eventID, f.orgID,
			f.ticketStatus,    // ← the gate key
			(*time.Time)(nil), // TicketUsedAt
		}}
	}
	return &hscannerTestRow{err: fmt.Errorf("callbackStatusFakeDB: unexpected SQL: %q", sql)}
}

// TestHandleScannerScanEvents_TicketStatusGate verifies that the scan-events
// ingest endpoint rejects scans of cancelled, revoked, or transferred tickets
// with a per-scan error (not a batch-level error — partial-success semantics).
func TestHandleScannerScanEvents_TicketStatusGate(t *testing.T) {
	statuses := []string{"cancelled", "revoked", "transferred"}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			db := &callbackStatusFakeDB{
				orgID:        uuid.New(),
				channelID:    uuid.New(),
				ticketID:     uuid.New(),
				sessionID:    uuid.New(),
				eventID:      uuid.New(),
				ticketStatus: status,
			}
			q := gen.New(db)
			h := New(nil, q, nil, nil, fakeRateLimiter{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

			// Build a valid scan-events batch with a single scan.
			scannedAt := time.Now().UTC().Format(time.RFC3339)
			reqBody := fmt.Sprintf(`{"scans":[{"credential_code":"TEST-QR-CODE","scanned_at":%q,"result":"admitted"}]}`, scannedAt)
			req := httptest.NewRequest(http.MethodPost, "/v1/scanner/scan-events", strings.NewReader(reqBody))
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			h.HandleScannerScanEvents(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status %s: HTTP code = %d, want 200; body: %s",
					status, rec.Code, rec.Body.String())
			}

			// Response is a batch with per-scan results.
			var resp struct {
				Results []struct {
					Error string `json:"error"`
				} `json:"results"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("status %s: decode: %v", status, err)
			}
			if len(resp.Results) != 1 {
				t.Fatalf("status %s: got %d results, want 1", status, len(resp.Results))
			}
			wantErr := "scanner.ticket_not_admissible: ticket is " + status
			if resp.Results[0].Error != wantErr {
				t.Errorf("status %s: result[0].error = %q, want %q",
					status, resp.Results[0].Error, wantErr)
			}
		})
	}
}
