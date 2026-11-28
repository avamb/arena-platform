// sales_mode_gate_test.go pins spec 63 §14.2: a session a partner box office
// sells (sessions.sales_mode = 'external', migration 0221) never takes a
// hold, and the refusal maps to 409 session.sold_externally.
package hcheckout

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestSessionSellable(t *testing.T) {
	cases := map[string]bool{
		gen.SessionSalesModePlatform:    true,
		"":                              true, // a row read without the column
		gen.SessionSalesModeExternal:    false,
		gen.SessionSalesModeControlOnly: false,
		"something_unknown":             false,
	}
	for mode, want := range cases {
		if got := SessionSellable(mode); got != want {
			t.Errorf("SessionSellable(%q) = %v, want %v", mode, got, want)
		}
	}
}

func TestSalesModeRefusal(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	tierID := uuid.MustParse("00000000-0000-0000-0000-0000000000b1")

	if err := SalesModeRefusal(gen.SessionSalesModePlatform, tierID, now); err != nil {
		t.Fatalf("platform session refused: %v", err)
	}
	if err := SalesModeRefusal(gen.SessionSalesModeControlOnly, tierID, now); !errors.Is(err, ErrSessionSoldExternally) {
		t.Fatalf("control_only session: %v, want ErrSessionSoldExternally", err)
	}
	err := SalesModeRefusal(gen.SessionSalesModeExternal, tierID, now)
	if err == nil {
		t.Fatal("external session was not refused")
	}
	if !errors.Is(err, ErrSessionSoldExternally) {
		t.Errorf("refusal %v does not match ErrSessionSoldExternally", err)
	}
	if errors.Is(err, ErrCategoryNotOnSale) || errors.Is(err, ErrCategoryClosed) || errors.Is(err, ErrSessionCancelled) {
		t.Errorf("an external refusal must not read as another refusal: %v", err)
	}
	if got := CategoryGateErrorCode(err); got != "session.sold_externally" {
		t.Errorf("error code = %q, want session.sold_externally", got)
	}
	if CategoryGateMessage(err) == "" || err.Error() == "" {
		t.Error("refusal has no message")
	}
	if got := CategoryGateTierID(err); got != tierID {
		t.Errorf("refusal names tier %s, want %s", got, tierID)
	}
	if got := CategoryGateSalesState(err); got != "" {
		t.Errorf("external refusal carries a sales state %q", got)
	}
}

func TestWriteCategoryGateError_SoldExternallyIs409(t *testing.T) {
	err := SalesModeRefusal(gen.SessionSalesModeExternal, uuid.Nil, time.Now())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feed/x/checkout/start", nil)
	WriteCategoryGateError(rec, req, err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"session.sold_externally"`) {
		t.Errorf("body %s does not carry session.sold_externally", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"tier_id"`) {
		t.Errorf("a session refusal without a tier must not name one: %s", rec.Body.String())
	}
}

func TestCheckSessionSellable_NilQueriesIsLeftToLaterChecks(t *testing.T) {
	if err := CheckSessionSellable(t.Context(), nil, uuid.New(), time.Now()); err != nil {
		t.Fatalf("nil queries: %v", err)
	}
}
