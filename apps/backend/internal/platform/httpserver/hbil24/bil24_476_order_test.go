// bil24_476_order_test.go — Feature #476 W1-A2b slice 20 tests for the
// GET_ORDER_INFO response wire shape (spec §7.8 / §9.3).
//
// The projection under test — buildGetOrderInfoBody — is pure over
// gen.CheckoutSessionRow + ticket count, so no *pgxpool.Pool, no Handler,
// and no live DB are needed. This keeps the slice under the sub-feature
// budget and matches the pattern of the earlier buildActionEntry /
// buildCountryCityLists tests in this package (bil24_476_catalog_test.go).
package hbil24

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestBil24_476_BuildGetOrderInfoBody_KeyRenames_Spec78 pins the spec §7.8
// / §9.3 key renames landed by slice 20: `orderId` → `id`, `state` →
// `status`, `ticketCount` → `ticketQuantity`. Also pins that the deferred
// keys (`expiration` — reservation join not yet wired) stay absent so the
// wire shape is honest until a follow-up slice lands them.
func TestBil24_476_BuildGetOrderInfoBody_KeyRenames_Spec78(t *testing.T) {
	sub := int64(500)
	disc := int64(0)
	pf := int64(0)
	prov := int64(0)
	tot := int64(500)
	cur := "CZK"
	cs := gen.CheckoutSessionRow{
		ID:          uuid.MustParse("018f1e2a-3c4d-7e5f-a6b7-c8d9e0f1a2b4"),
		State:       "created",
		Subtotal:    &sub,
		Discount:    &disc,
		PlatformFee: &pf,
		ProviderFee: &prov,
		Total:       &tot,
		Currency:    &cur,
	}

	got := buildGetOrderInfoBody(cs, 1)

	// Spec §9.3: outer body keys are `id`, `status`, `ticketQuantity`
	// (renamed from the pre-slice `orderId`, `state`, `ticketCount`).
	if _, has := got["id"]; !has {
		t.Errorf("id key MUST be present per spec §9.3 (was `orderId` pre-slice)")
	}
	if _, has := got["orderId"]; has {
		t.Errorf("orderId key MUST be absent per spec §9.3 (renamed to `id`), got %v", got["orderId"])
	}
	if got["status"] != "created" {
		t.Errorf("status = %v, want %q (renamed from `state`)", got["status"], "created")
	}
	if _, has := got["state"]; has {
		t.Errorf("state key MUST be absent per spec §9.3 (renamed to `status`), got %v", got["state"])
	}
	if got["ticketQuantity"] != 1 {
		t.Errorf("ticketQuantity = %v, want 1 (renamed from `ticketCount`)", got["ticketQuantity"])
	}
	if _, has := got["ticketCount"]; has {
		t.Errorf("ticketCount key MUST be absent per spec §9.3 (renamed to `ticketQuantity`), got %v", got["ticketCount"])
	}

	// Financial keys unchanged from pre-slice: sum, discount, charge,
	// totalSum, currency.
	if got["sum"] != int64(500) {
		t.Errorf("sum = %v, want 500", got["sum"])
	}
	if got["discount"] != int64(0) {
		t.Errorf("discount = %v, want 0", got["discount"])
	}
	if got["charge"] != int64(0) {
		t.Errorf("charge = %v, want 0", got["charge"])
	}
	if got["totalSum"] != int64(500) {
		t.Errorf("totalSum = %v, want 500", got["totalSum"])
	}
	if got["currency"] != "CZK" {
		t.Errorf("currency = %v, want %q", got["currency"], "CZK")
	}

	// Deferred to later slices; absent so absence is honest.
	if _, has := got["expiration"]; has {
		t.Errorf("expiration key MUST be absent this slice (reservation join deferred), got %v", got["expiration"])
	}
}

// TestBil24_476_BuildGetOrderInfoBody_ChargeSumsFees pins the Bil24 semantic
// that `charge` in the response is the sum of platform_fee + provider_fee
// (both are separate columns on checkout_sessions but merged on the wire).
// Regression guard for a future refactor that might accidentally emit only
// one of the two.
func TestBil24_476_BuildGetOrderInfoBody_ChargeSumsFees(t *testing.T) {
	pf := int64(30)
	prov := int64(20)
	cs := gen.CheckoutSessionRow{
		ID:          uuid.New(),
		State:       "pricing_confirmed",
		PlatformFee: &pf,
		ProviderFee: &prov,
	}

	got := buildGetOrderInfoBody(cs, 0)

	if got["charge"] != int64(50) {
		t.Errorf("charge = %v, want 50 (platform_fee 30 + provider_fee 20)", got["charge"])
	}
	if got["ticketQuantity"] != 0 {
		t.Errorf("ticketQuantity = %v, want 0", got["ticketQuantity"])
	}
}

// TestBil24_476_BuildGetOrderInfoBody_CurrencyAbsentWhenNil pins that the
// currency key is OMITTED (not emitted as an empty string) when the
// checkout session is pre-pricing_confirmed and Currency is nil. Matches
// the pre-slice behaviour to avoid churning wire consumers.
func TestBil24_476_BuildGetOrderInfoBody_CurrencyAbsentWhenNil(t *testing.T) {
	cs := gen.CheckoutSessionRow{
		ID:    uuid.New(),
		State: "created",
	}

	got := buildGetOrderInfoBody(cs, 0)

	if _, has := got["currency"]; has {
		t.Errorf("currency key MUST be absent when Currency is nil, got %v", got["currency"])
	}
	// Zero-valued financial keys still present with 0 — the pre-slice
	// contract emits them even when nil, so tests / consumers can key
	// on presence to detect a successful envelope.
	if got["sum"] != int64(0) {
		t.Errorf("sum = %v, want 0", got["sum"])
	}
	if got["totalSum"] != int64(0) {
		t.Errorf("totalSum = %v, want 0", got["totalSum"])
	}
}
