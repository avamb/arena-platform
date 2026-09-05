package ordering

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// fakeStore is an in-memory stand-in for *gen.Queries covering every store
// interface in this package. It records what was written so tests can assert
// on the aggregate rather than on call sequences.
type fakeStore struct {
	checkout    gen.CheckoutSessionRow
	checkoutErr error

	reservation    gen.ReservationRow
	reservationErr error

	gaItems    []gen.ReservationGAItemRow
	gaItemsErr error

	seats    []gen.SessionSeatRow
	seatsErr error

	orders    map[uuid.UUID]gen.OrderRow
	openOrder *gen.OrderRow
	openErr   error

	insertedOrders []gen.OrderRow
	items          []gen.OrderItemRow
	events         []gen.OrderEventRow

	expirable   []gen.OrderRow
	expireErrOn map[uuid.UUID]error

	insertOrderErr error
	insertItemErr  error
	insertEventErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		orders:      map[uuid.UUID]gen.OrderRow{},
		expireErrOn: map[uuid.UUID]error{},
	}
}

func (f *fakeStore) GetCheckoutSessionByID(_ context.Context, _ uuid.UUID) (gen.CheckoutSessionRow, error) {
	return f.checkout, f.checkoutErr
}

func (f *fakeStore) GetReservationByID(_ context.Context, _ uuid.UUID) (gen.ReservationRow, error) {
	return f.reservation, f.reservationErr
}

func (f *fakeStore) ListReservationGAItems(_ context.Context, _ uuid.UUID) ([]gen.ReservationGAItemRow, error) {
	return f.gaItems, f.gaItemsErr
}

func (f *fakeStore) ListReservationSeats(_ context.Context, _ uuid.UUID) ([]gen.SessionSeatRow, error) {
	return f.seats, f.seatsErr
}

//nolint:revive // mirrors the wide gen.Queries signature on purpose
func (f *fakeStore) InsertOrder(
	_ context.Context,
	orgID, channelID, eventID, sessionID uuid.UUID,
	customerID *uuid.UUID,
	checkoutSessionID, reservationID uuid.UUID,
	externalRef *string,
	source, status string,
	currency string,
	subtotal, discount, charge, total int64,
	chargePercentBP int32,
	promoCodeID *uuid.UUID,
	buyerName, buyerEmail, buyerPhone, paymentMethod *string,
	expiresAt *time.Time,
	metadata json.RawMessage,
) (gen.OrderRow, error) {
	if f.insertOrderErr != nil {
		return gen.OrderRow{}, f.insertOrderErr
	}
	row := gen.OrderRow{
		ID:                uuid.New(),
		SystemID:          int64(len(f.insertedOrders) + 1),
		OrgID:             orgID,
		ChannelID:         channelID,
		EventID:           eventID,
		SessionID:         sessionID,
		CustomerID:        customerID,
		CheckoutSessionID: checkoutSessionID,
		ReservationID:     reservationID,
		ExternalRef:       externalRef,
		Source:            source,
		Status:            status,
		Currency:          currency,
		Subtotal:          subtotal,
		Discount:          discount,
		Charge:            charge,
		Total:             total,
		ChargePercentBP:   chargePercentBP,
		PromoCodeID:       promoCodeID,
		BuyerName:         buyerName,
		BuyerEmail:        buyerEmail,
		BuyerPhone:        buyerPhone,
		PaymentMethod:     paymentMethod,
		ExpiresAt:         expiresAt,
		Metadata:          metadata,
	}
	f.insertedOrders = append(f.insertedOrders, row)
	f.orders[row.ID] = row
	return row, nil
}

func (f *fakeStore) InsertOrderItem(
	_ context.Context,
	orderID uuid.UUID,
	ordinal int32,
	kind string,
	tierID uuid.UUID,
	sessionSeatID, ticketID *uuid.UUID,
	unitPrice, discount, charge, total int64,
) (gen.OrderItemRow, error) {
	if f.insertItemErr != nil {
		return gen.OrderItemRow{}, f.insertItemErr
	}
	row := gen.OrderItemRow{
		ID:            uuid.New(),
		OrderID:       orderID,
		Ordinal:       ordinal,
		Kind:          kind,
		TierID:        tierID,
		SessionSeatID: sessionSeatID,
		TicketID:      ticketID,
		UnitPrice:     unitPrice,
		Discount:      discount,
		Charge:        charge,
		Total:         total,
	}
	f.items = append(f.items, row)
	return row, nil
}

func (f *fakeStore) InsertOrderEvent(_ context.Context, orderID uuid.UUID, eventType, actor string, payload json.RawMessage) (gen.OrderEventRow, error) {
	if f.insertEventErr != nil {
		return gen.OrderEventRow{}, f.insertEventErr
	}
	row := gen.OrderEventRow{
		ID:      uuid.New(),
		OrderID: orderID,
		Type:    eventType,
		Actor:   actor,
		Payload: payload,
	}
	f.events = append(f.events, row)
	return row, nil
}

func (f *fakeStore) GetOrderByID(_ context.Context, id, _ uuid.UUID) (gen.OrderRow, error) {
	row, ok := f.orders[id]
	if !ok {
		return gen.OrderRow{}, errors.New("no rows")
	}
	return row, nil
}

func (f *fakeStore) UpdateOrderStatus(_ context.Context, id, _ uuid.UUID, status string, paidAt, cancelledAt *time.Time) (gen.OrderRow, error) {
	row, ok := f.orders[id]
	if !ok {
		return gen.OrderRow{}, errors.New("no rows")
	}
	row.Status = status
	if paidAt != nil {
		row.PaidAt = paidAt
	}
	if cancelledAt != nil {
		row.CancelledAt = cancelledAt
	}
	f.orders[id] = row
	return row, nil
}

func (f *fakeStore) FindOpenOrderByCustomerSession(_ context.Context, _, _ uuid.UUID) (gen.OrderRow, error) {
	if f.openErr != nil {
		return gen.OrderRow{}, f.openErr
	}
	if f.openOrder == nil {
		return gen.OrderRow{}, errors.New("unexpected call")
	}
	return *f.openOrder, nil
}

func (f *fakeStore) ListExpirableOrders(_ context.Context, _ time.Time, limit int32) ([]gen.OrderRow, error) {
	if int32(len(f.expirable)) <= limit {
		return f.expirable, nil
	}
	return f.expirable[:limit], nil
}

func (f *fakeStore) ExpireOrderIfStillPending(_ context.Context, id uuid.UUID) (gen.OrderRow, error) {
	if err, ok := f.expireErrOn[id]; ok {
		return gen.OrderRow{}, err
	}
	row, ok := f.orders[id]
	if !ok {
		row = gen.OrderRow{ID: id}
	}
	row.Status = StatusExpired
	f.orders[id] = row
	return row, nil
}

// ptr is a tiny generic helper for the many *T fields on gen rows.
func ptr[T any](v T) *T { return &v }
