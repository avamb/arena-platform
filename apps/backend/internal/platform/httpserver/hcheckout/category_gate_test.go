// category_gate_test.go pins the ONE "may this category still be sold?"
// rule (plan 08_architecture/23 step 4, decisions 1/4/5/10) that every
// entry point creating a NEW hold runs.
package hcheckout

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestCategorySellable_OpenClosedAndSaleWindow(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	tierID := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	cases := []struct {
		name     string
		tier     gen.TicketTierRow
		wantCode string
	}{
		{
			name: "open category with no window sells",
			tier: gen.TicketTierRow{ID: tierID, IsOpen: true},
		},
		{
			name:     "closed category refuses",
			tier:     gen.TicketTierRow{ID: tierID, IsOpen: false},
			wantCode: "tier.closed",
		},
		{
			name:     "closed beats an open sale window",
			tier:     gen.TicketTierRow{ID: tierID, IsOpen: false, SaleWindowStart: &past, SaleWindowEnd: &future},
			wantCode: "tier.closed",
		},
		{
			name: "inside the window sells",
			tier: gen.TicketTierRow{ID: tierID, IsOpen: true, SaleWindowStart: &past, SaleWindowEnd: &future},
		},
		{
			name:     "before the window refuses",
			tier:     gen.TicketTierRow{ID: tierID, IsOpen: true, SaleWindowStart: &future},
			wantCode: "tier.not_on_sale",
		},
		{
			name:     "after the window refuses",
			tier:     gen.TicketTierRow{ID: tierID, IsOpen: true, SaleWindowEnd: &past},
			wantCode: "tier.not_on_sale",
		},
		{
			// Boundaries are inclusive: a window is [start, end], so a hold
			// at either instant is still inside it.
			name: "exactly at the window start sells",
			tier: gen.TicketTierRow{ID: tierID, IsOpen: true, SaleWindowStart: &now},
		},
		{
			name: "exactly at the window end sells",
			tier: gen.TicketTierRow{ID: tierID, IsOpen: true, SaleWindowEnd: &now},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CategorySellable(tc.tier, now)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("CategorySellable = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CategorySellable = nil, want %s", tc.wantCode)
			}
			if got := CategoryGateErrorCode(err); got != tc.wantCode {
				t.Errorf("error code = %q, want %q (err %v)", got, tc.wantCode, err)
			}
			if got := CategoryGateTierID(err); got != tierID {
				t.Errorf("gate error names tier %s, want %s", got, tierID)
			}
			if CategoryGateMessage(err) == "" {
				t.Error("gate error has no human-readable message")
			}
		})
	}
}

// The sentinels are what the gateway's error mapping switches on, so a
// refusal must match them through errors.Is even though the value handlers
// see is the struct.
func TestCategoryNotSellableError_MatchesItsSentinel(t *testing.T) {
	now := time.Now().UTC()
	closed := CategorySellable(gen.TicketTierRow{IsOpen: false}, now)
	if !errors.Is(closed, ErrCategoryClosed) {
		t.Errorf("closed refusal does not match ErrCategoryClosed: %v", closed)
	}
	if errors.Is(closed, ErrCategoryNotOnSale) {
		t.Errorf("closed refusal must not match ErrCategoryNotOnSale: %v", closed)
	}

	past := now.Add(-time.Hour)
	offSale := CategorySellable(gen.TicketTierRow{IsOpen: true, SaleWindowEnd: &past}, now)
	if !errors.Is(offSale, ErrCategoryNotOnSale) {
		t.Errorf("off-sale refusal does not match ErrCategoryNotOnSale: %v", offSale)
	}
	if errors.Is(offSale, ErrCategoryClosed) {
		t.Errorf("off-sale refusal must not match ErrCategoryClosed: %v", offSale)
	}

	if CategoryGateErrorCode(errors.New("boom")) != "" {
		t.Error("an unrelated error must not map to a gate code")
	}
}

// A GA line without a category matches no place at all since migration
// 0101, so the allocator rejects it as the input error it is instead of
// letting it read as a sold-out category.
func TestAllocateGAUnitsTx_RejectsLineWithoutCategory(t *testing.T) {
	_, err := AllocateGAUnitsTx(t.Context(), nil, uuid.New(), uuid.New(), 1,
		[]GAUnitLine{{TierID: nil, Quantity: 1}})
	if !errors.Is(err, ErrHoldInvalidInput) {
		t.Fatalf("AllocateGAUnitsTx with a nil tier = %v, want ErrHoldInvalidInput", err)
	}

	tid := uuid.New()
	_, err = AllocateGAUnitsTx(t.Context(), nil, uuid.New(), uuid.New(), 1,
		[]GAUnitLine{{TierID: &tid, Quantity: 0}})
	if !errors.Is(err, ErrHoldInvalidInput) {
		t.Fatalf("AllocateGAUnitsTx with quantity 0 = %v, want ErrHoldInvalidInput", err)
	}
}

// UXH-23: a cancelled session keeps its row and its (open) categories, so
// the session's own status is what refuses a new hold on it. Only
// 'scheduled' sells; the codes distinguish a cancelled date from any other
// status.
func TestSessionStatusRefusal(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	tierID := uuid.MustParse("00000000-0000-0000-0000-0000000000a2")

	if err := SessionStatusRefusal("scheduled", tierID, now); err != nil {
		t.Fatalf("scheduled session refused: %v", err)
	}

	cases := []struct {
		status   string
		sentinel error
		wantCode string
	}{
		{"cancelled", ErrSessionCancelled, "session.cancelled"},
		{"completed", ErrSessionNotOnSale, "session.not_on_sale"},
		{"draft", ErrSessionNotOnSale, "session.not_on_sale"},
		{"", ErrSessionNotOnSale, "session.not_on_sale"},
	}
	for _, tc := range cases {
		t.Run("status="+tc.status, func(t *testing.T) {
			err := SessionStatusRefusal(tc.status, tierID, now)
			if err == nil {
				t.Fatalf("status %q was not refused", tc.status)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("refusal %v does not match its sentinel %v", err, tc.sentinel)
			}
			if errors.Is(err, ErrCategoryNotOnSale) || errors.Is(err, ErrCategoryClosed) {
				t.Errorf("a session refusal must not read as a category refusal: %v", err)
			}
			if got := CategoryGateErrorCode(err); got != tc.wantCode {
				t.Errorf("error code = %q, want %q", got, tc.wantCode)
			}
			if CategoryGateMessage(err) == "" || err.Error() == "" {
				t.Error("refusal has no message")
			}
			if got := CategoryGateTierID(err); got != tierID {
				t.Errorf("refusal names tier %s, want %s", got, tierID)
			}
		})
	}
}
