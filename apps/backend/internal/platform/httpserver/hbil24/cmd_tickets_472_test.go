// cmd_tickets_472_test.go — feature #472 (W1-A1c, spec §5 item 3 + §7.14):
// SCAN_TICKET org-scope enforcement.
//
// The scan flow now walks tickets → sessions → events.org_id and returns
// resultCode=-3 when the resolved barcode belongs to a different tenant
// than the one addressed by the request's fid credential. These unit tests
// exercise the branch with an in-memory ScanQuerier fake so no live
// PostgreSQL is required.
package hbil24

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// fakeScanQ is an in-memory ScanQuerier used by the #472 tests. Barcodes
// keyed by external_ref; MarkBarcodeScanned flips status='scanned' and
// returns ErrNoRows for non-active rows to mirror the real UPDATE ... WHERE
// status='active' behavior.
type fakeScanQ struct {
	barcodesByRef map[string]gen.BarcodeRow
	ticketsByID   map[uuid.UUID]gen.TicketRow
}

func (f *fakeScanQ) GetBarcodeByExternalRefAny(_ context.Context, ref string) (gen.BarcodeRow, error) {
	b, ok := f.barcodesByRef[ref]
	if !ok {
		return gen.BarcodeRow{}, pgx.ErrNoRows
	}
	return b, nil
}

func (f *fakeScanQ) MarkBarcodeScanned(_ context.Context, id uuid.UUID) (gen.BarcodeRow, error) {
	for k, b := range f.barcodesByRef {
		if b.ID != id {
			continue
		}
		if b.Status != "active" {
			return gen.BarcodeRow{}, pgx.ErrNoRows
		}
		b.Status = "scanned"
		f.barcodesByRef[k] = b
		return b, nil
	}
	return gen.BarcodeRow{}, pgx.ErrNoRows
}

func (f *fakeScanQ) GetTicketByID(_ context.Context, id uuid.UUID) (gen.TicketRow, error) {
	t, ok := f.ticketsByID[id]
	if !ok {
		return gen.TicketRow{}, pgx.ErrNoRows
	}
	return t, nil
}

// fakeSessOrg satisfies ReservationContextQuerier for the cross-org scan
// tests. Only GetSessionOrgContext is meaningful; the other methods return
// zero values because SCAN_TICKET never calls them.
type fakeSessOrg struct {
	byID map[uuid.UUID]uuid.UUID // session_id → org_id
}

func (f *fakeSessOrg) GetSessionOrgContext(_ context.Context, id uuid.UUID) (gen.SessionOrgContextRow, error) {
	org, ok := f.byID[id]
	if !ok {
		return gen.SessionOrgContextRow{}, pgx.ErrNoRows
	}
	return gen.SessionOrgContextRow{SessionID: id, OrgID: org}, nil
}

func (f *fakeSessOrg) GetSalesChannelByID(context.Context, uuid.UUID, uuid.UUID) (gen.SalesChannelRow, error) {
	return gen.SalesChannelRow{}, pgx.ErrNoRows
}

func (f *fakeSessOrg) GetReservationByID(context.Context, uuid.UUID) (gen.ReservationRow, error) {
	return gen.ReservationRow{}, pgx.ErrNoRows
}

func buildScanCrossOrgHandler(t *testing.T, channelOrg, ticketOrg uuid.UUID, tokenHash, externalRef string) (*Handler, uuid.UUID) {
	t.Helper()
	// Channel addressed by fid=1271; belongs to channelOrg.
	channelID := uuid.New()
	settings, err := json.Marshal(map[string]any{
		"gateway": map[string]any{"enabled": true, "token_hash": tokenHash},
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	channel := gen.SalesChannelRow{
		ID: channelID, OrgID: channelOrg, DisplayNumber: 1271,
		Name: "wp-channel", Settings: settings,
	}
	lookup := &fakeChannelLookup{
		byDisplayNumber: map[int64]gen.SalesChannelRow{1271: channel},
	}

	// Barcode belongs to ticket in a different org.
	ticketID := uuid.New()
	sessionID := uuid.New()
	barcode := gen.BarcodeRow{
		ID:          uuid.New(),
		AuthorityID: uuid.New(),
		ExternalRef: externalRef,
		TicketID:    &ticketID,
		Status:      "active",
	}
	ticket := gen.TicketRow{
		ID:             ticketID,
		SessionID:      sessionID,
		Status:         "issued",
		SystemTicketID: 9001,
	}
	scanQ := &fakeScanQ{
		barcodesByRef: map[string]gen.BarcodeRow{externalRef: barcode},
		ticketsByID:   map[uuid.UUID]gen.TicketRow{ticketID: ticket},
	}
	sessQ := &fakeSessOrg{byID: map[uuid.UUID]uuid.UUID{sessionID: ticketOrg}}

	h := New(nil, nil, nil, nil, nil, nil, nil, nil,
		ReservationDeps{CtxQ: sessQ},
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	).WithRequireToken(true).
		WithChannelLookup(lookup).
		WithScanQuerier(scanQ)

	return h, ticketID
}

// TestBil24_472_ScanTicket_CrossOrg_Rejected proves the org-scope guard:
// a barcode owned by org B, scanned via a channel that belongs to org A,
// yields resultCode=-3 (NotFound) with the bil24.not_found description
// rather than actually marking the barcode scanned.
func TestBil24_472_ScanTicket_CrossOrg_Rejected(t *testing.T) {
	orgA, orgB := uuid.New(), uuid.New()
	tokenHash := mustBcryptHash(t, "wp-secret")

	h, _ := buildScanCrossOrgHandler(t, orgA, orgB, tokenHash, "2401502608417")

	body := `{"command":"SCAN_TICKET","fid":"1271","token":"wp-secret","ticketId":"2401502608417"}`
	resp := postJSON(t, h, body)

	rc := mustResultCode(t, resp)
	if rc != ResultCodeNotFound {
		t.Fatalf("cross-org scan must return %d (NotFound), got %d; resp=%v",
			ResultCodeNotFound, rc, resp)
	}
	if cmd, _ := resp["command"].(string); cmd != "SCAN_TICKET" {
		t.Errorf("expected command=SCAN_TICKET, got %q", cmd)
	}
	// Description must be present — the localizer fallback still yields
	// English text so the wire never carries an empty description.
	if desc, _ := resp["description"].(string); desc == "" {
		t.Errorf("expected non-empty description, got empty")
	}
}

// TestBil24_472_ScanTicket_SameOrg_Passes proves the org-scope guard is not
// over-triggering: when the ticket's org matches the fid channel's org, the
// scan proceeds and returns resultCode=0.
func TestBil24_472_ScanTicket_SameOrg_Passes(t *testing.T) {
	org := uuid.New()
	tokenHash := mustBcryptHash(t, "wp-secret")

	h, _ := buildScanCrossOrgHandler(t, org, org, tokenHash, "2401502608417")

	body := `{"command":"SCAN_TICKET","fid":"1271","token":"wp-secret","ticketId":"2401502608417"}`
	resp := postJSON(t, h, body)

	rc := mustResultCode(t, resp)
	if rc != ResultCodeOK {
		t.Fatalf("same-org scan must return %d (OK), got %d; resp=%v",
			ResultCodeOK, rc, resp)
	}
	if scanStatus, _ := resp["scanStatus"].(string); scanStatus != "OK" {
		t.Errorf("expected scanStatus=OK, got %q", scanStatus)
	}
	if platformID, _ := resp["platformTicketId"].(float64); int64(platformID) != 9001 {
		t.Errorf("expected platformTicketId=9001, got %v", resp["platformTicketId"])
	}
}

// TestBil24_472_ScanTicket_AlreadyScanned_Returns_2 proves the -2 semantic
// for double-scan detection survived the #472 rewrite (spec §7.14: "keep
// the existing scanned/revoked -2 semantics").
func TestBil24_472_ScanTicket_AlreadyScanned_Returns_2(t *testing.T) {
	org := uuid.New()
	tokenHash := mustBcryptHash(t, "wp-secret")
	h, _ := buildScanCrossOrgHandler(t, org, org, tokenHash, "SCANNED-1")

	// Flip the fixture barcode to status=scanned by scanning once first.
	body := `{"command":"SCAN_TICKET","fid":"1271","token":"wp-secret","ticketId":"SCANNED-1"}`
	if rc := mustResultCode(t, postJSON(t, h, body)); rc != ResultCodeOK {
		t.Fatalf("prime scan must succeed, got %d", rc)
	}
	// Second scan must fail with -2.
	rc := mustResultCode(t, postJSON(t, h, body))
	if rc != ResultCodeInvalidRequest {
		t.Fatalf("second scan must return %d (InvalidRequest), got %d",
			ResultCodeInvalidRequest, rc)
	}
}

// TestBil24_472_ScanTicket_BarcodeNotFound_ReturnsMinus3 proves the
// bil24.not_found envelope is emitted when the WordPress side sends a
// barcode the platform has never issued.
func TestBil24_472_ScanTicket_BarcodeNotFound_ReturnsMinus3(t *testing.T) {
	org := uuid.New()
	tokenHash := mustBcryptHash(t, "wp-secret")
	h, _ := buildScanCrossOrgHandler(t, org, org, tokenHash, "known")

	body := `{"command":"SCAN_TICKET","fid":"1271","token":"wp-secret","ticketId":"unknown-barcode"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeNotFound {
		t.Fatalf("unknown barcode must return %d (NotFound), got %d", ResultCodeNotFound, rc)
	}
}
