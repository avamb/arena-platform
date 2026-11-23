// sales_mode_external_test.go pins spec 63 §14.2 on the gateway wire: a hold
// on a session a partner box office sells answers 101, and GET_ALL_ACTIONS
// leaves such a session (and an event with no other session) out.
package hbil24

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

func externalRefusal() error {
	return hcheckout.SalesModeRefusal(gen.SessionSalesModeExternal, uuid.Nil, time.Now())
}

func TestWriteHoldError_SoldExternallyIs101(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	w := httptest.NewRecorder()
	h.writeHoldError(context.Background(), w, "RESERVATION", externalRefusal())
	if got := decodeResultCode(t, w.Body.Bytes()); got != float64(ResultCodeUserVisible) {
		t.Fatalf("resultCode = %v, want %v", got, ResultCodeUserVisible)
	}
}

func TestWriteCartHoldError_SoldExternallyIs101(t *testing.T) {
	h := &Handler{logger: discardLogger()}
	w := httptest.NewRecorder()
	h.writeCartHoldError(w, bil24Request{Command: "RESERVATION"}, cartCtx{}, nil, cartPricing{}, externalRefusal())
	if got := decodeResultCode(t, w.Body.Bytes()); got != float64(ResultCodeUserVisible) {
		t.Fatalf("resultCode = %v, want %v", got, ResultCodeUserVisible)
	}
	if ResultCodeUserVisible != 101 {
		t.Fatalf("ResultCodeUserVisible = %d, the spec says 101", ResultCodeUserVisible)
	}
}

func TestWithoutExternalSessions(t *testing.T) {
	mixedEvent, externalEvent, platformEvent := uuid.New(), uuid.New(), uuid.New()
	mixedPlatform, mixedExternal := uuid.New(), uuid.New()
	onlyExternal, onlyPlatform := uuid.New(), uuid.New()
	sessions := []gen.ActionEventRow{
		{SessionID: mixedPlatform, EventID: mixedEvent, SalesMode: gen.SessionSalesModePlatform},
		{SessionID: mixedExternal, EventID: mixedEvent, SalesMode: gen.SessionSalesModeExternal},
		{SessionID: onlyExternal, EventID: externalEvent, SalesMode: gen.SessionSalesModeExternal},
		{SessionID: onlyPlatform, EventID: platformEvent, SalesMode: ""},
	}
	tiers := []gen.ActionEventTierRow{
		{Tier: gen.TicketTierRow{ID: uuid.New(), SessionID: mixedPlatform}},
		{Tier: gen.TicketTierRow{ID: uuid.New(), SessionID: mixedExternal}},
		{Tier: gen.TicketTierRow{ID: uuid.New(), SessionID: onlyPlatform}},
	}

	kept, keptTiers, externalOnly := withoutExternalSessions(sessions, tiers)
	if len(kept) != 2 || kept[0].SessionID != mixedPlatform || kept[1].SessionID != onlyPlatform {
		t.Fatalf("kept sessions = %+v, want the two platform ones", kept)
	}
	for _, tr := range keptTiers {
		if tr.Tier.SessionID == mixedExternal {
			t.Fatalf("a tier of an external session was kept")
		}
	}
	if len(keptTiers) != 2 {
		t.Fatalf("kept %d tiers, want 2", len(keptTiers))
	}
	if !externalOnly[externalEvent] || externalOnly[mixedEvent] || externalOnly[platformEvent] {
		t.Fatalf("externalOnly = %v, want only the all-external event", externalOnly)
	}

	events := []gen.EventRow{{ID: mixedEvent}, {ID: externalEvent}, {ID: platformEvent}}
	left := withoutExternalOnlyEvents(events, externalOnly)
	if len(left) != 2 || left[0].ID != mixedEvent || left[1].ID != platformEvent {
		t.Fatalf("events left = %+v, want the mixed and the platform event", left)
	}
}

func TestWithoutExternalSessions_NoExternalKeepsSlices(t *testing.T) {
	sessions := []gen.ActionEventRow{{SessionID: uuid.New(), SalesMode: gen.SessionSalesModePlatform}}
	kept, _, externalOnly := withoutExternalSessions(sessions, nil)
	if len(kept) != 1 || len(externalOnly) != 0 {
		t.Fatalf("kept=%d externalOnly=%v", len(kept), externalOnly)
	}
}
