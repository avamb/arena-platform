package shop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
)

// CheckoutItem is one line of the buyer's cart.
type CheckoutItem struct {
	VariantID uuid.UUID
	Qty       int
}

// Buyer is who pays.
type Buyer struct {
	Name  string
	Email string
	Phone string
}

// Address is where the goods go. Name empty means the buyer's name.
type Address struct {
	Name     string
	Address1 string
	Address2 string
	City     string
	Postal   string
	Country  string
}

// CheckoutInput is one storefront purchase.
type CheckoutInput struct {
	OrgID        uuid.UUID
	Items        []CheckoutItem
	Buyer        Buyer
	Ship         Address
	DeliveryNote string
	Locale       string
	// AgeConfirmed is the buyer's "I am old enough" tick; required when the
	// shop has an age gate.
	AgeConfirmed bool
	// HoldFor is how long the order holds its goods: the provider's payment
	// window plus a grace, so the hold outlives the hosted page.
	HoldFor time.Duration
	// PaymentReady is the verdict of the payment pre-flight, gathered on the
	// pool BEFORE the transaction (never inside it). A non-nil value refuses
	// a cart with a total above zero before it takes any stock.
	PaymentReady error
	// ChannelID is the sales channel taking the payment, when known; it is
	// only passed on to the customer resolution.
	ChannelID uuid.UUID
	// Now is the clock (tests); zero means time.Now().
	Now time.Time
}

// MaxPendingPerBuyer is how many orders one buyer e-mail may have waiting
// for payment in one shop at a time; the next checkout answers 409
// shop.too_many_pending until one is paid or expires.
const MaxPendingPerBuyer = 3

// PlacedOrder is the committed order with its lines. Paid is true when the
// order cost nothing and was completed inline.
type PlacedOrder struct {
	Order gen.ShopOrderRow
	Items []gen.ShopOrderItemRow
	Paid  bool
}

// normalizeCheckout validates the input and merges repeated variants.
func normalizeCheckout(in *CheckoutInput) error {
	if len(in.Items) == 0 || len(in.Items) > MaxCheckoutLines {
		return invalid("shop.invalid_items", "the cart must hold between 1 and 50 lines")
	}
	merged := map[uuid.UUID]int{}
	var order []uuid.UUID
	for _, it := range in.Items {
		if it.VariantID == uuid.Nil || it.Qty < 1 || it.Qty > MaxLineQty {
			return invalid("shop.invalid_items", "every line needs a variant_id and a qty between 1 and 99")
		}
		if _, ok := merged[it.VariantID]; !ok {
			order = append(order, it.VariantID)
		}
		merged[it.VariantID] += it.Qty
		if merged[it.VariantID] > MaxLineQty {
			return invalid("shop.invalid_items", "at most 99 of one variant per order")
		}
	}
	in.Items = in.Items[:0]
	for _, id := range order {
		in.Items = append(in.Items, CheckoutItem{VariantID: id, Qty: merged[id]})
	}

	b := &in.Buyer
	b.Name, b.Email, b.Phone = strings.TrimSpace(b.Name), strings.TrimSpace(b.Email), strings.TrimSpace(b.Phone)
	if b.Name == "" || !textOK(b.Name, MaxShortTextLen) {
		return invalid("shop.invalid_buyer", "buyer.name is required and limited to 200 characters")
	}
	if !ValidEmail(b.Email) {
		return invalid("shop.invalid_buyer", "buyer.email is not a valid e-mail address")
	}
	if !textOK(b.Phone, MaxPhoneLen) {
		return invalid("shop.invalid_buyer", "buyer.phone is limited to 50 characters")
	}

	a := &in.Ship
	a.Name, a.Address1, a.Address2 = strings.TrimSpace(a.Name), strings.TrimSpace(a.Address1), strings.TrimSpace(a.Address2)
	a.City, a.Postal = strings.TrimSpace(a.City), strings.TrimSpace(a.Postal)
	a.Country = strings.ToUpper(strings.TrimSpace(a.Country))
	if a.Name == "" {
		a.Name = b.Name
	}
	if a.Address1 == "" || a.City == "" || !ValidCountry(a.Country) {
		return invalid("shop.invalid_address", "ship.address1, ship.city and a two-letter ship.country are required")
	}
	if !textOK(a.Name, MaxShortTextLen) || !textOK(a.Address1, MaxAddressLen) || !textOK(a.Address2, MaxAddressLen) ||
		!textOK(a.City, MaxCityLen) || !textOK(a.Postal, MaxPostalLen) {
		return invalid("shop.invalid_address", "an address field is too long")
	}
	in.DeliveryNote = strings.TrimSpace(in.DeliveryNote)
	if !textOK(in.DeliveryNote, MaxNoteLen) {
		return invalid("shop.text_too_long", "delivery_note is limited to 2000 characters")
	}
	in.Locale = strings.ToLower(strings.TrimSpace(in.Locale))
	if len(in.Locale) > 2 {
		in.Locale = in.Locale[:2]
	}
	if in.HoldFor <= 0 {
		return fmt.Errorf("shop: checkout without a hold duration")
	}
	return nil
}

// shortage is one line the shelf cannot serve.
type shortage struct {
	VariantID uuid.UUID `json:"variant_id"`
	Requested int       `json:"requested"`
	Available int       `json:"available"`
}

// Available is what a variant can still sell: unlimited (-1) when it does not
// track stock, else stock minus what pending orders hold, never below 0.
func Available(v gen.ShopVariantRow) int {
	if !v.TrackStock {
		return -1
	}
	a := int(v.Stock) - int(v.Held)
	if a < 0 {
		return 0
	}
	return a
}

// PlaceOrder creates the order and holds its goods in one transaction: the
// variants are locked in id order, every line is checked against what is
// left, and a shortage refuses the whole cart with 409 shop.out_of_stock
// (Details["items"] lists each short line). A cart with a total above zero
// is refused with in.PaymentReady when the pre-flight failed, before any
// stock is held. A free cart is completed inline (status paid).
func (s *Service) PlaceOrder(ctx context.Context, in CheckoutInput) (PlacedOrder, error) {
	if err := s.ready(); err != nil {
		return PlacedOrder{}, err
	}
	if err := normalizeCheckout(&in); err != nil {
		return PlacedOrder{}, err
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	token, err := NewOrderToken()
	if err != nil {
		return PlacedOrder{}, err
	}

	var placed PlacedOrder
	err = s.inTx(ctx, func(tx pgx.Tx, q *gen.Queries) error {
		settings, err := q.GetShopSettings(ctx, in.OrgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrShopNotFound
		}
		if err != nil {
			return err
		}
		if !settings.Enabled {
			return ErrShopNotFound
		}
		if settings.AgeGateMinAge != nil && !in.AgeConfirmed {
			return invalid("shop.age_confirmation_required",
				fmt.Sprintf("confirm that you are at least %d years old to buy here", *settings.AgeGateMinAge))
		}
		if limit := int(settings.MaxLineQty); limit > 0 {
			for _, it := range in.Items {
				if it.Qty > limit {
					e := invalid("shop.invalid_items", fmt.Sprintf("at most %d of one item per order", limit))
					e.Details = map[string]any{"max_line_qty": limit, "variant_id": it.VariantID.String()}
					return e
				}
			}
		}
		if err := q.LockShopBuyer(ctx, in.OrgID, in.Buyer.Email); err != nil {
			return err
		}
		pending, err := q.CountLivePendingShopOrders(ctx, in.OrgID, in.Buyer.Email)
		if err != nil {
			return err
		}
		if pending >= MaxPendingPerBuyer {
			return newErr(http.StatusConflict, "shop.too_many_pending",
				fmt.Sprintf("you already have %d orders waiting for payment here; pay or wait for one of them to expire", pending))
		}

		ids := make([]uuid.UUID, len(in.Items))
		for i, it := range in.Items {
			ids[i] = it.VariantID
		}
		locked, err := q.LockShopVariantsForCheckout(ctx, in.OrgID, ids)
		if err != nil {
			return err
		}
		byID := map[uuid.UUID]gen.ShopCheckoutVariantRow{}
		for _, v := range locked {
			byID[v.ID] = v
		}

		var short []shortage
		lines := make([]gen.ShopOrderItemRow, 0, len(in.Items))
		var subtotal int64
		for i, it := range in.Items {
			v, ok := byID[it.VariantID]
			if !ok || v.ProductStatus != ProductActive {
				e := invalid("shop.invalid_item", "a product in the cart is no longer sold, remove it and try again")
				e.Details = map[string]any{"variant_id": it.VariantID.String()}
				return e
			}
			if avail := Available(v.ShopVariantRow); avail >= 0 && it.Qty > avail {
				short = append(short, shortage{VariantID: v.ID, Requested: it.Qty, Available: avail})
				continue
			}
			vid := v.ID
			total := v.PriceMinor * int64(it.Qty)
			subtotal += total
			lines = append(lines, gen.ShopOrderItemRow{
				VariantID: &vid, ProductTitle: v.ProductTitle, VariantTitle: v.Title, SKU: v.SKU,
				Qty: int32(it.Qty), UnitPriceMinor: v.PriceMinor, TotalMinor: total, //nolint:gosec // qty <= 99
				Ordinal: int32(i + 1), //nolint:gosec // lines <= 50
			})
		}
		if len(short) > 0 {
			sort.Slice(short, func(a, b int) bool { return short[a].VariantID.String() < short[b].VariantID.String() })
			e := newErr(http.StatusConflict, "shop.out_of_stock", "some items are not available in the quantity requested")
			e.Details = map[string]any{"items": short}
			return e
		}
		total := subtotal + settings.ShippingFeeMinor
		if total > 0 && in.PaymentReady != nil {
			return in.PaymentReady
		}

		var locale *string
		if in.Locale != "" {
			locale = &in.Locale
		}
		customerID := s.resolveCustomerTx(ctx, tx, in, now)
		expires := now.Add(in.HoldFor)
		o, err := q.InsertShopOrder(ctx, gen.InsertShopOrderParams{
			OrgID: in.OrgID, OrderToken: token,
			BuyerName: in.Buyer.Name, BuyerEmail: in.Buyer.Email, BuyerPhone: in.Buyer.Phone,
			ShipName: in.Ship.Name, ShipAddress1: in.Ship.Address1, ShipAddress2: in.Ship.Address2,
			ShipCity: in.Ship.City, ShipPostal: in.Ship.Postal, ShipCountry: in.Ship.Country,
			DeliveryNote: in.DeliveryNote, Currency: settings.Currency,
			Subtotal: subtotal, ShippingFee: settings.ShippingFeeMinor, Total: total,
			BuyerLocale: locale, ExpiresAt: &expires, CustomerID: customerID,
		})
		if err != nil {
			return err
		}
		for i := range lines {
			lines[i].OrderID = o.ID
			if err := q.InsertShopOrderItem(ctx, lines[i]); err != nil {
				return err
			}
		}
		if err := q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveReserve); err != nil {
			return err
		}
		placed = PlacedOrder{Order: o, Items: lines}
		if total == 0 {
			paid, err := s.markPaidTx(ctx, tx, q, o, map[string]any{"free_order": true})
			if err != nil {
				return err
			}
			placed.Order, placed.Paid = paid, true
		}
		return nil
	})
	if err != nil {
		return PlacedOrder{}, err
	}
	return placed, nil
}

// resolveCustomerTx finds or creates the buyer's customer (e-mail and phone
// identities) and links it to the organization, inside a SAVEPOINT of the
// checkout transaction: a failure is logged and discarded, never fails the
// order (AGENTS.md: best-effort writes inside a money transaction).
func (s *Service) resolveCustomerTx(ctx context.Context, tx pgx.Tx, in CheckoutInput, now time.Time) *uuid.UUID {
	sp, err := tx.Begin(ctx)
	if err != nil {
		s.logger.Warn("shop: customer resolution skipped, no savepoint", "error", err.Error())
		return nil
	}
	var id uuid.UUID
	err = func() error {
		store := customers.NewStoreFromQueries(gen.New(sp))
		res, err := customers.Resolve(ctx, store, customers.ResolveInput{
			Email: in.Buyer.Email, Phone: in.Buyer.Phone, Name: in.Buyer.Name,
			ChannelID: in.ChannelID, DefaultRegion: in.Ship.Country, Locale: in.Locale, Now: now.UTC(),
		})
		if err != nil {
			return fmt.Errorf("resolve customer: %w", err)
		}
		id = res.Customer.ID
		return customers.LinkOrg(ctx, store, id, in.OrgID, "order")
	}()
	if err != nil {
		_ = sp.Rollback(ctx)
		s.logger.Warn("shop: customer resolution failed and was discarded (non-fatal)", "org_id", in.OrgID.String(), "error", err.Error())
		return nil
	}
	if err := sp.Commit(ctx); err != nil {
		s.logger.Warn("shop: customer resolution savepoint did not commit (non-fatal)", "error", err.Error())
		return nil
	}
	return &id
}

// HostedPayment is what the provider answered for an order's hosted page.
type HostedPayment struct {
	Provider          string
	ProviderPaymentID string
	URL               string
}

// AttachPayment records the payment intent of a hosted page and links the
// order to it, in one transaction. Until it commits a webhook for the page
// cannot find the payment, so the caller must not hand the buyer the URL
// before it returns nil.
func (s *Service) AttachPayment(ctx context.Context, o gen.ShopOrderRow, p HostedPayment) (gen.PaymentIntentRow, error) {
	var pi gen.PaymentIntentRow
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		var err error
		pi, err = q.InsertHostedPaymentIntent(ctx, nil, o.OrgID, p.Provider, p.ProviderPaymentID, o.Total, o.Currency, p.URL)
		if err != nil {
			return err
		}
		return q.SetShopOrderPayment(ctx, o.ID, pi.ID, p.Provider)
	})
	return pi, err
}

// AbandonOrder ends a pending order whose payment page could not be started:
// it expires at once and its goods go back on sale.
func (s *Service) AbandonOrder(ctx context.Context, orderID, orgID uuid.UUID) error {
	return s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		o, err := q.GetShopOrderForUpdate(ctx, orderID, orgID)
		if err != nil {
			return err
		}
		if o.Status != StatusPendingPayment {
			return nil
		}
		if _, err := q.SetShopOrderStatus(ctx, o.ID, StatusExpired, nil, []byte(`{"abandoned":"payment_start_failed"}`)); err != nil {
			return err
		}
		return q.JournalShopOrderHold(ctx, o.ID, o.OrgID, MoveRelease)
	})
}

// OrderByToken reads the order a buyer holds the token of, with its lines and
// payment page.
func (s *Service) OrderByToken(ctx context.Context, token string) (OrderView, error) {
	if err := s.ready(); err != nil {
		return OrderView{}, err
	}
	token = strings.TrimSpace(token)
	if len(token) != 64 {
		return OrderView{}, ErrOrderNotFound
	}
	o, err := s.q.GetShopOrderByToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrderView{}, ErrOrderNotFound
	}
	if err != nil {
		return OrderView{}, err
	}
	return s.view(ctx, s.q, o)
}
