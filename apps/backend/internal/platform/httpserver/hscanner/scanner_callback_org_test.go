// scanner_callback_org_test.go — POST /v1/scanner/scan-events keeps a scan
// inside the organization of the feed token. A credential that resolves to a
// ticket of another organization is handled like an unknown credential: the
// scan row carries no event, session or ticket, the ticket is not marked used
// and the answer says nothing about the ticket.
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

// callbackOrgFakeDB resolves the feed token to tokenOrg and the credential
// to a ticket of ticketOrg, and records what the handler writes.
type callbackOrgFakeDB struct {
	tokenOrg     uuid.UUID
	ticketOrg    uuid.UUID
	ticketID     uuid.UUID
	sessionID    uuid.UUID
	eventID      uuid.UUID
	ticketStatus string

	insertArgs []any
	usedAtSet  bool
	unexpected []string
}

func (f *callbackOrgFakeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	switch {
	case strings.Contains(sql, "agent_feed_tokens"):
		return pgconn.CommandTag{}, nil
	case strings.Contains(sql, "UPDATE tickets"):
		f.usedAtSet = true
		return pgconn.CommandTag{}, nil
	}
	f.unexpected = append(f.unexpected, "Exec: "+sql)
	return pgconn.CommandTag{}, fmt.Errorf("callbackOrgFakeDB: unexpected Exec")
}

func (f *callbackOrgFakeDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	f.unexpected = append(f.unexpected, "Query: "+sql)
	return nil, fmt.Errorf("callbackOrgFakeDB: unexpected Query")
}

func (f *callbackOrgFakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM   agent_feed_tokens"):
		return &hscannerTestRow{vals: []any{uuid.New(), uuid.New(), f.tokenOrg}}
	case strings.Contains(sql, "FROM   ticket_credentials"):
		return &hscannerTestRow{vals: []any{
			f.ticketID, f.sessionID, f.eventID, f.ticketOrg,
			f.ticketStatus, (*time.Time)(nil),
		}}
	case strings.Contains(sql, "INSERT INTO scan_events"):
		f.insertArgs = args
		now := time.Now().UTC()
		return &hscannerTestRow{vals: []any{
			uuid.New(), f.tokenOrg, args[1], args[2], args[3],
			args[4], now, "", "", args[8], now, true,
		}}
	}
	f.unexpected = append(f.unexpected, "QueryRow: "+sql)
	return &hscannerTestRow{err: fmt.Errorf("callbackOrgFakeDB: unexpected SQL")}
}

type scanOrgResult struct {
	Error    string  `json:"error"`
	TicketID *string `json:"ticket_id"`
}

func runScanOrg(t *testing.T, db *callbackOrgFakeDB) scanOrgResult {
	t.Helper()
	q := gen.New(db)
	h := New(q, q, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scannedAt := time.Now().UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"scans":[{"credential_code":"TEST-QR-CODE","scanned_at":%q,"result":"admitted"}]}`, scannedAt)
	req := httptest.NewRequest(http.MethodPost, "/v1/scanner/scan-events", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.HandleScannerScanEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []scanOrgResult `json:"results"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(resp.Results))
	}
	if len(db.unexpected) > 0 {
		t.Fatalf("unexpected database calls: %v", db.unexpected)
	}
	return resp.Results[0]
}

func isNilUUIDArg(v any) bool {
	p, ok := v.(*uuid.UUID)
	return v == nil || (ok && p == nil)
}

func TestScannerScanEvents_OtherOrganizationTicketIsUnresolved(t *testing.T) {
	for _, status := range []string{"valid", "cancelled", "transferred"} {
		t.Run(status, func(t *testing.T) {
			db := &callbackOrgFakeDB{
				tokenOrg: uuid.New(), ticketOrg: uuid.New(),
				ticketID: uuid.New(), sessionID: uuid.New(), eventID: uuid.New(),
				ticketStatus: status,
			}
			res := runScanOrg(t, db)

			if db.insertArgs == nil {
				t.Fatal("the scan was not recorded")
			}
			if got := db.insertArgs[0]; got != db.tokenOrg {
				t.Errorf("scan org = %v, want the token organization %v", got, db.tokenOrg)
			}
			for i, name := range map[int]string{1: "event_id", 2: "session_id", 3: "ticket_id"} {
				if !isNilUUIDArg(db.insertArgs[i]) {
					t.Errorf("$%d %s = %v, want nil", i+1, name, db.insertArgs[i])
				}
			}
			if db.usedAtSet {
				t.Error("the ticket of another organization was marked used")
			}
			if res.Error != "" {
				t.Errorf("result error = %q, want none (the ticket status must not show)", res.Error)
			}
			if res.TicketID != nil {
				t.Errorf("result ticket_id = %q, want none", *res.TicketID)
			}
		})
	}
}

func TestScannerScanEvents_SameOrganizationTicketResolves(t *testing.T) {
	org := uuid.New()
	db := &callbackOrgFakeDB{
		tokenOrg: org, ticketOrg: org,
		ticketID: uuid.New(), sessionID: uuid.New(), eventID: uuid.New(),
		ticketStatus: "valid",
	}
	res := runScanOrg(t, db)

	if db.insertArgs == nil {
		t.Fatal("the scan was not recorded")
	}
	if p, ok := db.insertArgs[3].(*uuid.UUID); !ok || p == nil || *p != db.ticketID {
		t.Errorf("$4 ticket_id = %v, want %v", db.insertArgs[3], db.ticketID)
	}
	if !db.usedAtSet {
		t.Error("an admitted scan of an own ticket must mark it used")
	}
	if res.TicketID == nil || *res.TicketID != db.ticketID.String() {
		t.Errorf("result ticket_id = %v, want %s", res.TicketID, db.ticketID)
	}
}
