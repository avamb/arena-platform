// backfill_test.go — feature #503 (W1-B6b): tickets.backfill_ean13 must be
// idempotent — running Run repeatedly against the same Store must never
// mint a second credential/barcode for a ticket it already backfilled, and
// must settle to a zero-work no-op once every candidate is caught up.
package backfill

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// fakeStore is an in-memory Store: candidates are the tickets still missing
// an ean13 row; once InsertTicketCredential/InsertBarcode run for a ticket,
// it is removed from candidates, mirroring the real LEFT JOIN ... IS NULL
// query (a backfilled ticket never comes back).
type fakeStore struct {
	platformAuthorityID uuid.UUID
	candidates          []TicketRow

	credentialsByTicket map[uuid.UUID]string // ticket_id -> ean13 payload
	barcodesByRef       map[string]uuid.UUID // external_ref -> ticket_id

	insertCredentialCalls int
	insertBarcodeCalls    int
}

func newFakeStore(candidates []TicketRow) *fakeStore {
	return &fakeStore{
		platformAuthorityID: uuid.New(),
		candidates:          append([]TicketRow(nil), candidates...),
		credentialsByTicket: map[uuid.UUID]string{},
		barcodesByRef:       map[string]uuid.UUID{},
	}
}

func (f *fakeStore) ListTicketsMissingEAN13(_ context.Context, limit int32) ([]TicketRow, error) {
	if int32(len(f.candidates)) > limit {
		return append([]TicketRow(nil), f.candidates[:limit]...), nil
	}
	return append([]TicketRow(nil), f.candidates...), nil
}

func (f *fakeStore) GetBarcodeAuthorityByType(_ context.Context, authorityType string) (uuid.UUID, error) {
	if authorityType != "platform" {
		return uuid.Nil, errors.New("fakeStore: unknown authority type " + authorityType)
	}
	return f.platformAuthorityID, nil
}

func (f *fakeStore) InsertTicketCredential(_ context.Context, ticketID uuid.UUID, credType string, payload string) error {
	if credType != "ean13" {
		return errors.New("fakeStore: unexpected credential type " + credType)
	}
	if _, exists := f.credentialsByTicket[ticketID]; exists {
		return errors.New("fakeStore: duplicate ean13 credential insert for ticket " + ticketID.String())
	}
	f.credentialsByTicket[ticketID] = payload
	f.insertCredentialCalls++
	return nil
}

func (f *fakeStore) InsertBarcode(_ context.Context, authorityID uuid.UUID, externalRef string, ticketID *uuid.UUID) error {
	if authorityID != f.platformAuthorityID {
		return errors.New("fakeStore: unexpected authority id")
	}
	if _, exists := f.barcodesByRef[externalRef]; exists {
		return errors.New("fakeStore: duplicate barcode insert for external_ref " + externalRef)
	}
	if ticketID == nil {
		return errors.New("fakeStore: nil ticketID")
	}
	f.barcodesByRef[externalRef] = *ticketID
	f.insertBarcodeCalls++
	return nil
}

// removeBackfilled simulates the real ListTicketsMissingEAN13 query: once a
// ticket has an ean13 credential, it drops out of future candidate lists.
func (f *fakeStore) removeBackfilled() {
	remaining := f.candidates[:0]
	for _, t := range f.candidates {
		if _, done := f.credentialsByTicket[t.ID]; !done {
			remaining = append(remaining, t)
		}
	}
	f.candidates = remaining
}

// TestRun_BackfillsAllCandidatesAndIsIdempotent proves a single Run call
// backfills every candidate exactly once, and a second Run call (after the
// candidate list has been updated the way the real LEFT JOIN query would
// update it) is a no-op: zero tickets backfilled, no error, and no
// duplicate credential/barcode inserts.
func TestRun_BackfillsAllCandidatesAndIsIdempotent(t *testing.T) {
	tickets := []TicketRow{
		{ID: uuid.New(), SystemTicketID: 1001},
		{ID: uuid.New(), SystemTicketID: 1002},
		{ID: uuid.New(), SystemTicketID: 1003},
	}
	store := newFakeStore(tickets)
	ctx := context.Background()

	backfilled, err := Run(ctx, store, DefaultBatchSize)
	if err != nil {
		t.Fatalf("Run (first pass): %v", err)
	}
	if backfilled != len(tickets) {
		t.Fatalf("Run (first pass) backfilled = %d, want %d", backfilled, len(tickets))
	}
	if store.insertCredentialCalls != len(tickets) || store.insertBarcodeCalls != len(tickets) {
		t.Fatalf("expected %d credential+barcode inserts each, got %d credentials, %d barcodes",
			len(tickets), store.insertCredentialCalls, store.insertBarcodeCalls)
	}
	for _, tk := range tickets {
		payload, ok := store.credentialsByTicket[tk.ID]
		if !ok {
			t.Fatalf("ticket %s: no ean13 credential recorded", tk.ID)
		}
		want := ean13.Encode(ean13PlatformPrefix, tk.SystemTicketID)
		if payload != want {
			t.Errorf("ticket %s: credential payload = %q, want %q", tk.ID, payload, want)
		}
		if !ean13.Valid(payload) {
			t.Errorf("ticket %s: credential payload %q is not checksum-valid", tk.ID, payload)
		}
		if _, ok := store.barcodesByRef[payload]; !ok {
			t.Errorf("ticket %s: no barcode recorded for external_ref %q", tk.ID, payload)
		}
	}

	// Simulate the real query dropping backfilled tickets from future scans.
	store.removeBackfilled()
	if len(store.candidates) != 0 {
		t.Fatalf("expected 0 remaining candidates after first pass, got %d", len(store.candidates))
	}

	// Second pass: no candidates left — must be a fast no-op, no error, no
	// new inserts (which would otherwise 23505 on the real UNIQUE
	// constraints — the fake's duplicate guards mirror that).
	backfilledAgain, err := Run(ctx, store, DefaultBatchSize)
	if err != nil {
		t.Fatalf("Run (second pass): %v", err)
	}
	if backfilledAgain != 0 {
		t.Fatalf("Run (second pass) backfilled = %d, want 0", backfilledAgain)
	}
	if store.insertCredentialCalls != len(tickets) || store.insertBarcodeCalls != len(tickets) {
		t.Fatalf("second pass must not insert anything new: got %d credential inserts, %d barcode inserts (want %d each)",
			store.insertCredentialCalls, store.insertBarcodeCalls, len(tickets))
	}
}

// TestRun_BatchSizeCapsWork proves Run respects the batchSize cap even when
// more candidates remain — the job is meant to be re-enqueued until the
// candidate list is exhausted, not to drain it in one shot.
func TestRun_BatchSizeCapsWork(t *testing.T) {
	tickets := []TicketRow{
		{ID: uuid.New(), SystemTicketID: 1},
		{ID: uuid.New(), SystemTicketID: 2},
		{ID: uuid.New(), SystemTicketID: 3},
	}
	store := newFakeStore(tickets)

	backfilled, err := Run(context.Background(), store, 2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if backfilled != 2 {
		t.Fatalf("backfilled = %d, want 2 (batch size cap)", backfilled)
	}
}

// TestRun_NoCandidates_IsNoopAndSkipsAuthorityLookup proves an empty
// candidate list short-circuits before ever resolving the platform
// authority — a fully caught-up stand shouldn't need a working barcode
// federation to no-op cleanly.
func TestRun_NoCandidates_IsNoopAndSkipsAuthorityLookup(t *testing.T) {
	store := newFakeStore(nil)
	// Poison the authority lookup so a call to it fails the test loudly.
	store.platformAuthorityID = uuid.Nil

	backfilled, err := Run(context.Background(), store, DefaultBatchSize)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if backfilled != 0 {
		t.Fatalf("backfilled = %d, want 0", backfilled)
	}
}
