package ordering

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Spec 43 section 4.4: the order copies the attribution the caller resolved
// before its transaction; CreateOrderFromCheckout only writes it.
func TestCreateOrderFromCheckout_WritesAttribution(t *testing.T) {
	f, _ := storeWithGACart(t)
	link := uuid.New()
	attr := json.RawMessage(`{"ref":"abcdefg","utm_source":"qr"}`)

	res, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID,
		EventID:           uuid.New(),
		Source:            SourcePublicFeed,
		SalesLinkID:       &link,
		Attribution:       attr,
	})
	if err != nil {
		t.Fatalf("CreateOrderFromCheckout: %v", err)
	}
	if res.Order.SalesLinkID == nil || *res.Order.SalesLinkID != link {
		t.Fatalf("sales_link_id = %v, want %s", res.Order.SalesLinkID, link)
	}
	if string(res.Order.Attribution) != string(attr) {
		t.Fatalf("attribution = %s, want %s", res.Order.Attribution, attr)
	}
}

func TestCreateOrderFromCheckout_NoAttribution(t *testing.T) {
	f, _ := storeWithGACart(t)
	res, err := CreateOrderFromCheckout(context.Background(), f, CreateInput{
		CheckoutSessionID: f.checkout.ID,
		EventID:           uuid.New(),
		Source:            SourcePublicFeed,
	})
	if err != nil {
		t.Fatalf("CreateOrderFromCheckout: %v", err)
	}
	if res.Order.SalesLinkID != nil || len(res.Order.Attribution) != 0 {
		t.Fatalf("want no link and no attribution, got %v %s", res.Order.SalesLinkID, res.Order.Attribution)
	}
}

// updateFake adds the update-in-place surface to fakeStore.
type updateFake struct {
	*fakeStore
	setCalls []json.RawMessage
}

func (u *updateFake) UpdateOrderCheckout(
	_ context.Context,
	id, orgID uuid.UUID,
	externalRef *string,
	checkoutSessionID, reservationID uuid.UUID,
	currency string,
	subtotal, discount, charge, total int64,
	chargePercentBP int32,
	promoCodeID *uuid.UUID,
	_, _, _ *string,
	expiresAt *time.Time,
) (gen.OrderRow, error) {
	o := u.orders[id]
	o.OrgID, o.ExternalRef = orgID, externalRef
	o.CheckoutSessionID, o.ReservationID = checkoutSessionID, reservationID
	o.Currency, o.Subtotal, o.Discount, o.Charge, o.Total = currency, subtotal, discount, charge, total
	o.ChargePercentBP, o.PromoCodeID, o.ExpiresAt = chargePercentBP, promoCodeID, expiresAt
	u.orders[id] = o
	return o, nil
}

func (u *updateFake) DeleteOrderItemsByOrder(_ context.Context, _ uuid.UUID) error { return nil }

func (u *updateFake) SetOrderAttribution(_ context.Context, id, _ uuid.UUID, link *uuid.UUID, attr json.RawMessage) (gen.OrderRow, error) {
	u.setCalls = append(u.setCalls, attr)
	o := u.orders[id]
	o.SalesLinkID, o.Attribution = link, attr
	u.orders[id] = o
	return o, nil
}

func TestUpdateOrderFromCheckout_Attribution(t *testing.T) {
	oldLink := uuid.New()
	newLink := uuid.New()
	setup := func(t *testing.T) (*updateFake, uuid.UUID) {
		f, _ := storeWithGACart(t)
		id := uuid.New()
		f.orders[id] = gen.OrderRow{
			ID: id, OrgID: f.checkout.OrgID, Status: StatusPendingPayment,
			SalesLinkID: &oldLink, Attribution: json.RawMessage(`{"ref":"aaaaaaa"}`),
		}
		return &updateFake{fakeStore: f}, id
	}

	t.Run("a new attribution replaces the stored one", func(t *testing.T) {
		u, id := setup(t)
		attr := json.RawMessage(`{"ref":"bbbbbbb","first":{"ref":"aaaaaaa"}}`)
		res, err := UpdateOrderFromCheckout(context.Background(), u, UpdateInput{
			OrderID: id, OrgID: u.checkout.OrgID,
			CreateInput: CreateInput{CheckoutSessionID: u.checkout.ID, SalesLinkID: &newLink, Attribution: attr},
		})
		if err != nil {
			t.Fatalf("UpdateOrderFromCheckout: %v", err)
		}
		if res.Order.SalesLinkID == nil || *res.Order.SalesLinkID != newLink || string(res.Order.Attribution) != string(attr) {
			t.Fatalf("got link %v attribution %s", res.Order.SalesLinkID, res.Order.Attribution)
		}
	})

	t.Run("no attribution keeps the stored one", func(t *testing.T) {
		u, id := setup(t)
		res, err := UpdateOrderFromCheckout(context.Background(), u, UpdateInput{
			OrderID: id, OrgID: u.checkout.OrgID,
			CreateInput: CreateInput{CheckoutSessionID: u.checkout.ID},
		})
		if err != nil {
			t.Fatalf("UpdateOrderFromCheckout: %v", err)
		}
		if len(u.setCalls) != 0 {
			t.Fatalf("SetOrderAttribution called without an attribution")
		}
		if res.Order.SalesLinkID == nil || *res.Order.SalesLinkID != oldLink {
			t.Fatalf("stored link lost: %v", res.Order.SalesLinkID)
		}
	})
}
