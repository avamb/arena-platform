package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// The order history of a Shopify store becomes shop_orders with source
// "shopify". An imported order is a RECORD of what happened: it has no
// payment intent (the money went through Shopify's own gateway), it never
// touches stock, sends no letter and no Telegram message, and the shop
// refuses to ship, e-mail or refund it (shop.ErrOrderHistoric). Re-running
// the import adds the orders that are new and brings the money and delivery
// fields of known ones up to date; it never duplicates.

const shopifyOrderSource = "shopify"

// Column limits of shop_orders (shop_orders_texts_check).
const (
	maxBuyerName = 200
	maxPhone     = 50
	maxAddress   = 300
	maxCity      = 120
	maxPostal    = 30
	maxNote      = 2000
)

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ─────────────────────────────────────────────────────────────────────────────
// Matching a line to a variant of the shop
// ─────────────────────────────────────────────────────────────────────────────

// variantRef is what an imported line keeps of a shop variant.
type variantRef struct {
	ID           uuid.UUID
	ProductTitle string
	VariantTitle string
	SKU          string
	Kind         string
}

// variantIndex finds the variant a Shopify line name ("Product - Variant", or
// just "Product" for a product without options) stands for. The orders file
// carries no SKU, so the name is the only key; a name that two variants
// share is ambiguous and matches nothing.
type variantIndex struct {
	byName    map[string]*variantRef
	ambiguous map[string]bool
}

func normName(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func newVariantIndex() *variantIndex {
	return &variantIndex{byName: map[string]*variantRef{}, ambiguous: map[string]bool{}}
}

func (x *variantIndex) add(key string, v *variantRef) {
	key = normName(key)
	if key == "" || x.ambiguous[key] {
		return
	}
	if prev, ok := x.byName[key]; ok && prev.ID != v.ID {
		delete(x.byName, key)
		x.ambiguous[key] = true
		return
	}
	x.byName[key] = v
}

func (x *variantIndex) find(lineName string) *variantRef {
	if x == nil {
		return nil
	}
	return x.byName[normName(lineName)]
}

// loadVariantIndex reads every variant of the organization's shop.
func (im *Importer) loadVariantIndex(ctx context.Context) (*variantIndex, error) {
	idx := newVariantIndex()
	if im.offline() {
		return idx, nil
	}
	rows, err := im.pool.Query(ctx, `
		SELECT v.id, p.id, p.title, v.title, COALESCE(v.sku, ''), p.kind
		FROM   shop_variants v JOIN shop_products p ON p.id = v.product_id
		WHERE  p.org_id = $1
		ORDER  BY (v.deleted_at IS NOT NULL), p.created_at, v.position, v.id`, im.opt.OrgID)
	if err != nil {
		return nil, fmt.Errorf("read the shop's variants: %w", err)
	}
	defer rows.Close()
	type rec struct {
		ref  variantRef
		prod uuid.UUID
	}
	var all []rec
	perProduct := map[uuid.UUID]int{}
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.ref.ID, &r.prod, &r.ref.ProductTitle, &r.ref.VariantTitle, &r.ref.SKU, &r.ref.Kind); err != nil {
			return nil, err
		}
		all = append(all, r)
		perProduct[r.prod]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range all {
		ref := &all[i].ref
		isDefault := strings.EqualFold(ref.VariantTitle, "Default Title")
		if isDefault {
			idx.add(ref.ProductTitle, ref)
			continue
		}
		idx.add(ref.ProductTitle+" - "+ref.VariantTitle, ref)
		if perProduct[all[i].prod] == 1 {
			idx.add(ref.ProductTitle, ref)
		}
	}
	return idx, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Planning one order: the pure part
// ─────────────────────────────────────────────────────────────────────────────

type plannedLine struct {
	VariantID    *uuid.UUID
	ProductTitle string
	VariantTitle string
	SKU          *string
	Qty          int32
	UnitMinor    int64
	TotalMinor   int64
	Kind         string
}

type plannedOrder struct {
	ExternalRef  string
	Status       string
	BuyerName    string
	BuyerEmail   string
	BuyerPhone   string
	ShipName     string
	ShipAddress1 string
	ShipAddress2 string
	ShipCity     string
	ShipPostal   string
	ShipCountry  string
	DeliveryKind string
	DeliveryNote string
	Currency     string
	Subtotal     int64
	Discount     int64
	ShippingFee  int64
	Total        int64
	Refunded     int64
	PaidAt       *time.Time
	ShippedAt    *time.Time
	CancelledAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StaffNote    string
	Metadata     []byte
	Lines        []plannedLine
	// ShippedAssumed is a paid order Shopify never marked as fulfilled: the
	// owner decided (2026-11-07) that these were shipped and only forgotten, so
	// the import stores them as shipped, at the time of the payment.
	ShippedAssumed bool
	// Unmatched counts lines no variant of the shop could be matched to.
	Unmatched int
}

// planOrder turns one parsed Shopify order into the row the shop stores. skip
// is a reason the order is deliberately left out (not paid in Shopify); err
// means the order cannot be imported as it stands.
func planOrder(o ImportOrder, idx *variantIndex, importedAt time.Time) (p plannedOrder, skip string, err error) {
	if o.timeErr != "" {
		return p, "", errors.New(o.timeErr)
	}
	if o.CreatedAt.IsZero() {
		return p, "", errors.New("the order has no creation time")
	}
	p.ExternalRef = o.Name
	p.Currency = orderCurrency(&o)
	p.CreatedAt = o.CreatedAt

	// Status.
	paid := true
	switch o.FinancialState {
	case "paid":
		p.Status = shop.StatusPaid
	case "refunded":
		p.Status = shop.StatusRefunded
	case "partially_refunded":
		p.Status = shop.StatusPartiallyRefunded
	case "voided", "expired":
		p.Status, paid = shop.StatusCancelled, false
	default:
		return p, "not paid in Shopify (" + stateLabel(o.FinancialState) + ")", nil
	}

	// Lines and money.
	if len(o.Lines) == 0 {
		return p, "", errors.New("the order has no lines")
	}
	for i, l := range o.Lines {
		if l.Qty <= 0 {
			return p, "", fmt.Errorf("line %d has no quantity", i+1)
		}
		pl := plannedLine{ProductTitle: l.Name, Qty: int32(l.Qty), UnitMinor: l.PriceMinor, TotalMinor: l.PriceMinor * int64(l.Qty), Kind: "physical"} //nolint:gosec // a small quantity
		if ref := idx.find(l.Name); ref != nil {
			id := ref.ID
			pl.VariantID, pl.ProductTitle, pl.VariantTitle, pl.Kind = &id, ref.ProductTitle, ref.VariantTitle, ref.Kind
			if strings.EqualFold(pl.VariantTitle, "Default Title") {
				pl.VariantTitle = ""
			}
			if ref.SKU != "" {
				sku := ref.SKU
				pl.SKU = &sku
			}
		} else {
			p.Unmatched++
			if l.SKU != "" {
				sku := l.SKU
				pl.SKU = &sku
			}
		}
		p.Subtotal += pl.TotalMinor
		p.Lines = append(p.Lines, pl)
	}
	p.Discount, p.ShippingFee, p.Total = o.DiscountMinor, o.ShippingMinor, o.TotalMinor
	if p.Subtotal-p.Discount+p.ShippingFee != p.Total {
		return p, "", fmt.Errorf("amounts do not add up: lines %s - discount %s + shipping %s is not total %s",
			FormatMinor(p.Subtotal, p.Currency), FormatMinor(p.Discount, p.Currency),
			FormatMinor(p.ShippingFee, p.Currency), FormatMinor(p.Total, p.Currency))
	}

	// Refunds.
	switch p.Status {
	case shop.StatusRefunded:
		p.Refunded = p.Total
	case shop.StatusPartiallyRefunded:
		p.Refunded = o.RefundedMinor
		switch {
		case p.Refunded <= 0:
			p.Status = shop.StatusPaid // Shopify says "partly refunded" but names no amount
		case p.Refunded >= p.Total:
			p.Status, p.Refunded = shop.StatusRefunded, p.Total
		}
	}

	// Moments.
	if paid {
		t := o.PaidAt
		if t.IsZero() {
			t = o.CreatedAt
		}
		p.PaidAt = &t
	}
	if !o.CancelledAt.IsZero() {
		t := o.CancelledAt
		p.CancelledAt = &t
		if p.Status == shop.StatusPaid {
			p.Status = shop.StatusCancelled // cancelled after payment with no refund recorded
		}
	}
	if o.Fulfillment == "fulfilled" {
		t := o.FulfilledAt
		if t.IsZero() {
			t = o.CreatedAt
		}
		p.ShippedAt = &t
		if p.Status == shop.StatusPaid {
			p.Status = shop.StatusShipped
		}
	}
	if o.Fulfillment != "fulfilled" && p.PaidAt != nil && (p.Status == shop.StatusPaid || p.Status == shop.StatusPartiallyRefunded) {
		t := *p.PaidAt
		p.ShippedAt, p.ShippedAssumed = &t, true
		if p.Status == shop.StatusPaid {
			p.Status = shop.StatusShipped
		}
	}
	p.UpdatedAt = latestOf(o.CreatedAt, o.PaidAt, o.FulfilledAt, o.CancelledAt)

	// Buyer and delivery.
	p.BuyerName = clip(o.BuyerName, maxBuyerName)
	p.BuyerEmail = o.Email
	p.BuyerPhone = clip(o.Phone, maxPhone)
	p.ShipName = clip(o.ShipName, maxBuyerName)
	p.ShipAddress1 = clip(o.ShipAddress1, maxAddress)
	p.ShipAddress2 = clip(o.ShipAddress2, maxAddress)
	p.ShipCity = clip(o.ShipCity, maxCity)
	p.ShipPostal = clip(o.ShipPostal, maxPostal)
	p.ShipCountry = o.ShipCountry
	p.DeliveryKind = shop.DeliveryPickup
	if o.ShipAddress1 != "" || o.ShippingMinor > 0 {
		p.DeliveryKind = shop.DeliveryShipping
	}
	p.DeliveryNote = clip(o.Note, maxNote)
	p.StaffNote = "Shopify " + o.Name + " (imported order history)"

	meta := map[string]any{
		"shopify": map[string]any{
			"name": o.Name, "id": o.ShopifyID,
			"financial_status": o.FinancialState, "fulfillment_status": o.Fulfillment,
			"payment_method": o.PaymentMethod, "payment_reference": o.PaymentRef,
			"discount_code": o.DiscountCode, "shipping_method": o.ShippingMethod,
			"taxes_minor": o.TaxesMinor,
		},
		"shipped_assumed": p.ShippedAssumed,
		"imported_at":     importedAt.Format(time.RFC3339),
	}
	if p.Metadata, err = json.Marshal(meta); err != nil {
		return p, "", err
	}
	return p, "", nil
}

func stateLabel(s string) string {
	if s == "" {
		return "no status"
	}
	return s
}

func latestOf(first time.Time, rest ...time.Time) time.Time {
	t := first
	for _, r := range rest {
		if r.After(t) {
			t = r
		}
	}
	return t
}

// ─────────────────────────────────────────────────────────────────────────────
// Writing
// ─────────────────────────────────────────────────────────────────────────────

// ImportOrders loads the orders of a Shopify export. Each order is its own
// transaction, so a bad row costs one row. In a dry run nothing is written:
// the orders are planned and compared with what is stored.
func (im *Importer) ImportOrders(ctx context.Context, of *OrdersFile) error {
	s := SummarizeOrders(of)
	im.rep.Orders = &s
	for _, p := range of.Problems {
		im.rep.problem("orders: %s", p)
		im.rep.OrdersImport.add(outFailed, "unreadable row")
	}
	idx, err := im.loadVariantIndex(ctx)
	if err != nil {
		return err
	}
	if im.offline() {
		im.rep.note("no database: order lines could not be matched to the shop's products")
	}
	now := im.now()
	for _, o := range of.Orders {
		plan, skip, perr := planOrder(o, idx, now)
		if skip != "" {
			im.rep.OrdersImport.add(outSkipped, skip)
			continue
		}
		if perr != nil {
			im.rep.OrdersImport.add(outFailed, "cannot be imported")
			im.rep.problem("orders: %s: %v", o.Name, perr)
			continue
		}
		im.rep.OrderLinesMatched += len(plan.Lines) - plan.Unmatched
		im.rep.OrderLinesUnmatched += plan.Unmatched
		if plan.ShippedAssumed {
			im.rep.OrdersShippedAssumed++
		}
		if im.offline() {
			im.rep.OrdersImport.add(outCreated, "")
			continue
		}
		outcome, err := im.storeOrder(ctx, o, plan)
		if err != nil {
			im.rep.OrdersImport.add(outFailed, "database error")
			im.log.Warn("order import failed", "order", o.Name, "error", err.Error())
			im.rep.problem("orders: %s: %v", o.Name, err)
			continue
		}
		im.rep.OrdersImport.add(outcome, "")
	}
	return nil
}

// existingOrder is what a known order stores of the fields a re-run refreshes.
type existingOrder struct {
	ID        uuid.UUID
	Status    string
	Refunded  int64
	PaidAt    *time.Time
	ShippedAt *time.Time
	Cancelled *time.Time
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (e existingOrder) matches(p plannedOrder) bool {
	return e.Status == p.Status && e.Refunded == p.Refunded &&
		sameTime(e.PaidAt, p.PaidAt) && sameTime(e.ShippedAt, p.ShippedAt) && sameTime(e.Cancelled, p.CancelledAt)
}

// storeOrder writes one planned order (or, in a dry run, only classifies it).
func (im *Importer) storeOrder(ctx context.Context, o ImportOrder, p plannedOrder) (string, error) {
	tx, err := im.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var have existingOrder
	err = tx.QueryRow(ctx, `SELECT id, status, refunded_minor, paid_at, shipped_at, cancelled_at
		FROM shop_orders WHERE org_id = $1 AND source = $2 AND external_ref = $3`,
		im.opt.OrgID, shopifyOrderSource, p.ExternalRef).Scan(&have.ID, &have.Status, &have.Refunded, &have.PaidAt, &have.ShippedAt, &have.Cancelled)
	switch {
	case err == nil:
		if have.matches(p) {
			return outUnchanged, nil
		}
		if im.opt.DryRun {
			return outUpdated, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE shop_orders SET status = $2, refunded_minor = $3, paid_at = $4, shipped_at = $5,
				cancelled_at = $6, updated_at = $7 WHERE id = $1`,
			have.ID, p.Status, p.Refunded, p.PaidAt, p.ShippedAt, p.CancelledAt, p.UpdatedAt); err != nil {
			return "", fmt.Errorf("update: %w", err)
		}
		return outUpdated, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("look the order up: %w", err)
	}
	if im.opt.DryRun {
		return outCreated, nil
	}

	customerID, err := im.resolveOrderCustomer(ctx, tx, o)
	if err != nil {
		return "", err
	}
	token, err := shop.NewOrderToken()
	if err != nil {
		return "", err
	}
	var locale *string
	if im.opt.Locale != "" {
		l := im.opt.Locale
		locale = &l
	}
	importedAt := im.now()
	var orderID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO shop_orders (org_id, order_token, customer_id, status, buyer_name, buyer_email, buyer_phone,
			ship_name, ship_address1, ship_address2, ship_city, ship_postal, ship_country, delivery_kind, delivery_note,
			currency, subtotal, discount, shipping_fee, total, refunded_minor, buyer_locale, source, external_ref,
			paid_at, shipped_at, cancelled_at, metadata, created_at, updated_at, staff_note, staff_note_by, staff_note_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
			$25, $26, $27, $28::jsonb, $29, $30, $31, 'import', $32)
		ON CONFLICT (org_id, source, external_ref) WHERE external_ref IS NOT NULL DO NOTHING
		RETURNING id`,
		im.opt.OrgID, token, customerID, p.Status, p.BuyerName, p.BuyerEmail, p.BuyerPhone,
		p.ShipName, p.ShipAddress1, p.ShipAddress2, p.ShipCity, p.ShipPostal, p.ShipCountry, p.DeliveryKind, p.DeliveryNote,
		p.Currency, p.Subtotal, p.Discount, p.ShippingFee, p.Total, p.Refunded, locale, shopifyOrderSource, p.ExternalRef,
		p.PaidAt, p.ShippedAt, p.CancelledAt, string(p.Metadata), p.CreatedAt, p.UpdatedAt, p.StaffNote, importedAt).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return outUnchanged, nil // a parallel run wrote it first
	}
	if err != nil {
		return "", fmt.Errorf("insert order: %w", err)
	}
	q := gen.New(tx)
	for i, l := range p.Lines {
		if err := q.InsertShopOrderItem(ctx, gen.ShopOrderItemRow{
			OrderID: orderID, VariantID: l.VariantID, ProductTitle: l.ProductTitle, VariantTitle: l.VariantTitle,
			SKU: l.SKU, Qty: l.Qty, UnitPriceMinor: l.UnitMinor, TotalMinor: l.TotalMinor,
			Ordinal: int32(i + 1), ProductKind: l.Kind, //nolint:gosec // at most a few dozen lines
		}); err != nil {
			return "", fmt.Errorf("insert line %d: %w", i+1, err)
		}
	}
	return outCreated, tx.Commit(ctx)
}

// resolveOrderCustomer finds or makes the customer of an order through the
// platform's one resolver, so the order joins the person's other purchases.
// It runs in a savepoint: a buyer whose details the resolver refuses costs
// the order its customer link, never the order itself.
func (im *Importer) resolveOrderCustomer(ctx context.Context, tx pgx.Tx, o ImportOrder) (*uuid.UUID, error) {
	if o.Email == "" && o.Phone == "" {
		return nil, nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	region := im.opt.DefaultRegion
	if o.ShipCountry != "" {
		region = o.ShipCountry
	}
	store := customers.NewStoreFromQueries(gen.New(sp))
	res, err := customers.Resolve(ctx, store, customers.ResolveInput{
		Email: o.Email, Phone: o.Phone, Name: o.BuyerName, DefaultRegion: region,
		Locale: im.opt.Locale, Source: importSource, Now: im.now(),
	})
	if err == nil {
		err = customers.LinkOrg(ctx, store, res.Customer.ID, im.opt.OrgID, "import")
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		im.rep.OrdersImport.add("", "buyer details refused by the customer resolver, order imported without a customer link")
		return nil, nil
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, err
	}
	id := res.Customer.ID
	return &id, nil
}
