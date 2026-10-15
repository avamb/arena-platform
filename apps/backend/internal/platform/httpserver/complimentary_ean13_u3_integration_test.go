//go:build integration

package httpserver

// U-3: an invitation is a ticket like any other, so it carries a platform
// EAN-13 and the gate finds it by that code. The scenario issues invitations
// through the real router (the same call the bot makes), then checks the
// barcodes row (authority platform), the ean13 credential, the lookups the
// scanner (/v1/scanner/validate) and the Bil24 SCAN_TICKET use, the
// idempotent replay (no second code) and the revoke (barcode and credential
// both revoked, the scanner refuses the code).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/backfill"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

func TestComplimentaryU3_InvitationGetsAScannableEAN13(t *testing.T) {
	e := newInviteE2E(t)
	org := e.seed.orgID.String()
	ctx := context.Background()
	q := gen.New(e.pool)

	body := map[string]any{
		"session_id": e.seed.session, "tier_id": e.seed.vip, "qty": 2,
		"batch_id":   "u3-" + e.seed.suffix,
		"recipients": []string{e.email("u3a"), e.email("u3b")},
	}
	code, raw := e.call(ecOwnerTG, http.MethodPost, "/v1/organizations/"+org+"/complimentary", body)
	if code != http.StatusCreated {
		t.Fatalf("issue: %d %s", code, raw)
	}
	var created struct {
		Issuance struct{ ID uuid.UUID } `json:"issuance"`
		Tickets  []struct {
			ID uuid.UUID `json:"id"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || len(created.Tickets) != 2 {
		t.Fatalf("issue answer: %v %s", err, raw)
	}

	platform, err := q.GetBarcodeAuthorityByType(ctx, "platform")
	if err != nil {
		t.Fatal(err)
	}
	codes := map[uuid.UUID]string{}
	for _, tk := range created.Tickets {
		cred, err := q.GetCredentialByTicketID(ctx, tk.ID, "ean13")
		if err != nil {
			t.Fatalf("ticket %s has no ean13 credential: %v", tk.ID, err)
		}
		if !ean13.Valid(cred.Payload) || cred.RevokedAt != nil {
			t.Fatalf("credential %q valid=%v revoked=%v", cred.Payload, ean13.Valid(cred.Payload), cred.RevokedAt != nil)
		}
		codes[tk.ID] = cred.Payload

		bcs, err := q.ListBarcodesByTicketID(ctx, tk.ID)
		if err != nil || len(bcs) != 1 {
			t.Fatalf("barcodes of ticket %s: %d rows, err %v, want exactly 1", tk.ID, len(bcs), err)
		}
		if bcs[0].AuthorityID != platform.ID || bcs[0].ExternalRef != cred.Payload || bcs[0].Status != "active" {
			t.Fatalf("barcode %+v does not match the credential %q on the platform authority", bcs[0], cred.Payload)
		}

		// The lookup SCAN_TICKET runs: any authority, by the code alone.
		found, err := q.GetBarcodeByExternalRefAny(ctx, cred.Payload)
		if err != nil || found.TicketID == nil || *found.TicketID != tk.ID {
			t.Fatalf("GetBarcodeByExternalRefAny(%q) = %+v, %v; want the invitation ticket", cred.Payload, found, err)
		}
	}
	if codes[created.Tickets[0].ID] == codes[created.Tickets[1].ID] {
		t.Fatal("two invitations got the same code")
	}

	validate := func(ref string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/v1/scanner/validate",
			strings.NewReader(`{"external_ref":"`+ref+`","authority_type":"platform"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.srv.scannerHandler().HandleScannerValidate(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	first := created.Tickets[0].ID
	if status, out := validate(codes[first]); status != http.StatusOK || out["valid"] != true || out["ticket_id"] != first.String() {
		t.Fatalf("scanner validate of the invitation: %d %v", status, out)
	}

	count := func(sql string) int {
		return e.count(sql, e.seed.orgID)
	}
	const barcodesOfOrg = `SELECT count(*) FROM barcodes b JOIN tickets t ON t.id = b.ticket_id
		JOIN complimentary_issuances ci ON ci.id = t.complimentary_issuance_id WHERE ci.org_id = $1`
	const credsOfOrg = `SELECT count(*) FROM ticket_credentials c JOIN tickets t ON t.id = c.ticket_id
		JOIN complimentary_issuances ci ON ci.id = t.complimentary_issuance_id WHERE ci.org_id = $1 AND c.type = 'ean13'`
	if n := count(barcodesOfOrg); n != 2 {
		t.Fatalf("barcodes after issue = %d, want 2", n)
	}

	// The same batch again is a replay: nothing new, no second code.
	code, raw = e.call(ecOwnerTG, http.MethodPost, "/v1/organizations/"+org+"/complimentary", body)
	if code != http.StatusOK || !strings.Contains(string(raw), `"idempotent_replay":true`) {
		t.Fatalf("replay: %d %s", code, raw)
	}
	if b, c := count(barcodesOfOrg), count(credsOfOrg); b != 2 || c != 2 {
		t.Fatalf("after the replay barcodes=%d credentials=%d, want 2 and 2", b, c)
	}
	if cred, _ := q.GetCredentialByTicketID(ctx, first, "ean13"); cred.Payload != codes[first] {
		t.Fatalf("the replay changed the code: %q -> %q", codes[first], cred.Payload)
	}

	// Annulling the invitation revokes the barcode AND the credential, and the
	// gate refuses the code.
	if code, raw := e.call(ecOwnerTG, http.MethodPost, "/v1/complimentary/"+created.Issuance.ID.String()+"/revoke", map[string]string{}); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, raw)
	}
	for _, tk := range created.Tickets {
		bcs, _ := q.ListBarcodesByTicketID(ctx, tk.ID)
		if len(bcs) != 1 || bcs[0].Status != "revoked" {
			t.Errorf("barcode of ticket %s after revoke: %+v", tk.ID, bcs)
		}
		if cred, err := q.GetCredentialByTicketID(ctx, tk.ID, "ean13"); err != nil || cred.RevokedAt == nil {
			t.Errorf("ean13 credential of ticket %s after revoke: %+v, %v", tk.ID, cred, err)
		}
	}
	if status, out := validate(codes[first]); status != http.StatusOK || out["valid"] != false {
		t.Errorf("a revoked invitation must not validate: %d %v", status, out)
	}
}

// onlyTickets narrows the backfill's candidate list to the given tickets, so
// the test repairs its own rows and never mints for another test's tickets in
// a shared database.
type onlyTickets struct {
	backfill.PGStore
	keep map[uuid.UUID]bool
}

func (s onlyTickets) ListTicketsMissingEAN13(ctx context.Context, limit int32) ([]backfill.TicketRow, error) {
	rows, err := s.PGStore.ListTicketsMissingEAN13(ctx, limit)
	if err != nil {
		return nil, err
	}
	var out []backfill.TicketRow
	for _, r := range rows {
		if s.keep[r.ID] {
			out = append(out, r)
		}
	}
	return out, nil
}

// An invitation issued BEFORE the fix has no barcode. tickets.backfill_ean13
// already covers it, because its candidate query filters on the ticket's
// status and the missing credential only, never on where the ticket came from.
func TestComplimentaryU3_BackfillCoversAnInvitationWithoutACode(t *testing.T) {
	e := newInviteE2E(t)
	org := e.seed.orgID.String()
	ctx := context.Background()
	q := gen.New(e.pool)

	code, raw := e.call(ecOwnerTG, http.MethodPost, "/v1/organizations/"+org+"/complimentary", map[string]any{
		"session_id": e.seed.session, "tier_id": e.seed.vip, "qty": 1,
		"batch_id": "u3b-" + e.seed.suffix, "recipients": []string{e.email("u3old")},
	})
	if code != http.StatusCreated {
		t.Fatalf("issue: %d %s", code, raw)
	}
	var created struct {
		Tickets []struct{ ID uuid.UUID } `json:"tickets"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || len(created.Tickets) != 1 {
		t.Fatalf("issue answer: %v %s", err, raw)
	}
	id := created.Tickets[0].ID

	// Put the ticket back into its pre-fix state: no barcode, no credential.
	for _, sql := range []string{
		`DELETE FROM barcodes WHERE ticket_id = $1`,
		`DELETE FROM ticket_credentials WHERE ticket_id = $1 AND type = 'ean13'`,
	} {
		if _, err := e.pool.Exec(ctx, sql, id); err != nil {
			t.Fatal(err)
		}
	}

	store := onlyTickets{PGStore: backfill.NewPGStore(q), keep: map[uuid.UUID]bool{id: true}}
	n, err := backfill.Run(ctx, store, 5000)
	if err != nil || n != 1 {
		t.Fatalf("backfill.Run = %d, %v; want the one invitation repaired", n, err)
	}
	cred, err := q.GetCredentialByTicketID(ctx, id, "ean13")
	if err != nil || !ean13.Valid(cred.Payload) {
		t.Fatalf("credential after backfill: %+v, %v", cred, err)
	}
	if found, err := q.GetBarcodeByExternalRefAny(ctx, cred.Payload); err != nil || found.TicketID == nil || *found.TicketID != id {
		t.Fatalf("the backfilled code is not found by SCAN_TICKET's lookup: %+v, %v", found, err)
	}
	// A second pass has nothing left to do.
	if n, err := backfill.Run(ctx, store, 5000); err != nil || n != 0 {
		t.Fatalf("second backfill pass = %d, %v; want 0", n, err)
	}
}
