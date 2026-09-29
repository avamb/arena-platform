package hbil24

// cmd_tickets_sample_test.go — SCAN_TICKET meets the code of a session's
// sample e-ticket (migration 0119): a real, unique EAN-13 in the 'sample'
// barcode authority that carries no ticket. The gate must answer "sample"
// (-2) and admit nobody, and the code must NOT be marked scanned, so the
// same PDF demonstrates the scanner as many times as the organizer likes.

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

// fakeScanWithAuthorities is fakeScanQ plus the authority lookup the sample
// check needs — what the real *gen.Queries offers.
type fakeScanWithAuthorities struct {
	*fakeScanQ
	authorities map[uuid.UUID]gen.BarcodeAuthorityRow
}

func (f *fakeScanWithAuthorities) GetBarcodeAuthorityByID(_ context.Context, id uuid.UUID) (gen.BarcodeAuthorityRow, error) {
	a, ok := f.authorities[id]
	if !ok {
		return gen.BarcodeAuthorityRow{}, pgx.ErrNoRows
	}
	return a, nil
}

func buildSampleScanHandler(t *testing.T, authorityType, code string) (*Handler, *fakeScanWithAuthorities) {
	t.Helper()
	settings, err := json.Marshal(map[string]any{
		"gateway": map[string]any{"enabled": true, "token_hash": mustBcryptHash(t, "wp-secret")},
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := gen.SalesChannelRow{ID: uuid.New(), OrgID: uuid.New(), DisplayNumber: 1271, Name: "wp", Settings: settings}
	authorityID := uuid.New()
	scanQ := &fakeScanWithAuthorities{
		fakeScanQ: &fakeScanQ{
			barcodesByRef: map[string]gen.BarcodeRow{code: {ID: uuid.New(), AuthorityID: authorityID, ExternalRef: code, Status: "active"}},
			ticketsByID:   map[uuid.UUID]gen.TicketRow{},
		},
		authorities: map[uuid.UUID]gen.BarcodeAuthorityRow{authorityID: {ID: authorityID, Type: authorityType, Label: authorityType}},
	}
	h := New(nil, nil, nil, nil, nil, nil, nil, nil,
		ReservationDeps{CtxQ: &fakeSessOrg{byID: map[uuid.UUID]uuid.UUID{}}},
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	).WithRequireToken(true).
		WithChannelLookup(&fakeChannelLookup{byDisplayNumber: map[int64]gen.SalesChannelRow{1271: channel}}).
		WithScanQuerier(scanQ)
	return h, scanQ
}

func TestBil24_ScanTicket_SampleCode_AnswersSampleAndStaysActive(t *testing.T) {
	const code = "2100000000302"
	h, scanQ := buildSampleScanHandler(t, "sample", code)
	body := `{"command":"SCAN_TICKET","fid":"1271","token":"wp-secret","ticketId":"` + code + `"}`

	for i := 0; i < 2; i++ {
		resp := postJSON(t, h, body)
		if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
			t.Fatalf("scan %d of a sample code: resultCode = %d, want %d; resp=%v", i+1, rc, ResultCodeInvalidRequest, resp)
		}
		if desc, _ := resp["description"].(string); desc != "sample ticket, not valid for entry" {
			t.Fatalf("scan %d description = %q", i+1, desc)
		}
		if st := scanQ.barcodesByRef[code].Status; st != "active" {
			t.Fatalf("scan %d marked the sample code %q", i+1, st)
		}
	}
}

// A ticket-less code of any OTHER authority (a guest list) keeps the
// pre-0119 behaviour: it admits and is marked scanned.
func TestBil24_ScanTicket_GuestListCode_StillAdmits(t *testing.T) {
	const code = "GUEST-42"
	h, scanQ := buildSampleScanHandler(t, "guest_list", code)
	resp := postJSON(t, h, `{"command":"SCAN_TICKET","fid":"1271","token":"wp-secret","ticketId":"`+code+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("guest-list scan: resultCode = %d, want 0; resp=%v", rc, resp)
	}
	if st := scanQ.barcodesByRef[code].Status; st != "scanned" {
		t.Fatalf("guest-list code status = %q, want scanned", st)
	}
}
