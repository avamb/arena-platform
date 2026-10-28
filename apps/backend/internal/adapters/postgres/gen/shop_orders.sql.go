// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: shop orders and stock of migration 0172 (spec 08_architecture/47
// sections 2 and 3)

package gen

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ShopOrderRow is one shop_orders row.
type ShopOrderRow struct {
	ID              uuid.UUID       `json:"id"`
	OrgID           uuid.UUID       `json:"org_id"`
	Number          int64           `json:"number"`
	OrderToken      string          `json:"order_token"`
	CustomerID      *uuid.UUID      `json:"customer_id"`
	Status          string          `json:"status"`
	BuyerName       string          `json:"buyer_name"`
	BuyerEmail      string          `json:"buyer_email"`
	BuyerPhone      string          `json:"buyer_phone"`
	ShipName        string          `json:"ship_name"`
	ShipAddress1    string          `json:"ship_address1"`
	ShipAddress2    string          `json:"ship_address2"`
	ShipCity        string          `json:"ship_city"`
	ShipPostal      string          `json:"ship_postal"`
	ShipCountry     string          `json:"ship_country"`
	DeliveryKind    string          `json:"delivery_kind"`
	DeliveryNote    string          `json:"delivery_note"`
	Currency        string          `json:"currency"`
	Subtotal        int64           `json:"subtotal"`
	Discount        int64           `json:"discount"`
	ShippingFee     int64           `json:"shipping_fee"`
	Total           int64           `json:"total"`
	RefundedMinor   int64           `json:"refunded_minor"`
	PaymentIntentID *uuid.UUID      `json:"payment_intent_id"`
	Provider        *string         `json:"provider"`
	BuyerLocale     *string         `json:"buyer_locale"`
	Source          string          `json:"source"`
	ExternalRef     *string         `json:"external_ref"`
	PaidAt          *time.Time      `json:"paid_at"`
	ShippedAt       *time.Time      `json:"shipped_at"`
	CancelledAt     *time.Time      `json:"cancelled_at"`
	TrackingNote    string          `json:"tracking_note"`
	ExpiresAt       *time.Time      `json:"expires_at"`
	Metadata        json.RawMessage `json:"metadata"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

const shopOrderColumns = `id, org_id, number, order_token, customer_id, status, buyer_name, buyer_email, buyer_phone,
       ship_name, ship_address1, ship_address2, ship_city, ship_postal, ship_country, delivery_kind,
       delivery_note, currency, subtotal, discount, shipping_fee, total, refunded_minor,
       payment_intent_id, provider, buyer_locale, source, external_ref, paid_at, shipped_at,
       cancelled_at, tracking_note, expires_at, metadata, created_at, updated_at`

func scanShopOrderRow(row interface{ Scan(dest ...any) error }) (ShopOrderRow, error) {
	var o ShopOrderRow
	err := row.Scan(&o.ID, &o.OrgID, &o.Number, &o.OrderToken, &o.CustomerID, &o.Status, &o.BuyerName,
		&o.BuyerEmail, &o.BuyerPhone, &o.ShipName, &o.ShipAddress1, &o.ShipAddress2, &o.ShipCity,
		&o.ShipPostal, &o.ShipCountry, &o.DeliveryKind, &o.DeliveryNote, &o.Currency, &o.Subtotal,
		&o.Discount, &o.ShippingFee, &o.Total, &o.RefundedMinor, &o.PaymentIntentID, &o.Provider,
		&o.BuyerLocale, &o.Source, &o.ExternalRef, &o.PaidAt, &o.ShippedAt, &o.CancelledAt,
		&o.TrackingNote, &o.ExpiresAt, &o.Metadata, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func collectShopOrders(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}) ([]ShopOrderRow, error) {
	defer rows.Close()
	var out []ShopOrderRow
	for rows.Next() {
		o, err := scanShopOrderRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// InsertShopOrderParams are the columns of a new order. Status "" means
// pending_payment, Source "" means web.
type InsertShopOrderParams struct {
	OrgID        uuid.UUID
	OrderToken   string
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
	BuyerLocale  *string
	Source       string
	ExternalRef  *string
	ExpiresAt    *time.Time
	CustomerID   *uuid.UUID
}

const lockShopBuyer = `-- name: LockShopBuyer :exec
SELECT pg_advisory_xact_lock(hashtextextended('shop_buyer:' || $1::text || ':' || lower($2::text), 0))`

// LockShopBuyer serializes the checkouts of one buyer e-mail in one shop for
// the rest of the transaction, so the pending-order cap cannot be raced.
func (q *Queries) LockShopBuyer(ctx context.Context, orgID uuid.UUID, email string) error {
	_, err := q.db.Exec(ctx, lockShopBuyer, orgID, email)
	return err
}

const countLivePendingShopOrders = `-- name: CountLivePendingShopOrders :one
SELECT count(*)::int FROM shop_orders
WHERE  org_id = $1 AND lower(buyer_email) = lower($2) AND status = 'pending_payment' AND expires_at > now()`

// CountLivePendingShopOrders counts a buyer's orders in one shop that still
// hold goods.
func (q *Queries) CountLivePendingShopOrders(ctx context.Context, orgID uuid.UUID, email string) (int, error) {
	var n int
	err := q.db.QueryRow(ctx, countLivePendingShopOrders, orgID, email).Scan(&n)
	return n, err
}

const insertShopOrder = `-- name: InsertShopOrder :one
INSERT INTO shop_orders (org_id, order_token, status, buyer_name, buyer_email, buyer_phone, ship_name,
                         ship_address1, ship_address2, ship_city, ship_postal, ship_country,
                         delivery_kind, delivery_note, currency, subtotal, discount, shipping_fee, total,
                         buyer_locale, source, external_ref, expires_at, customer_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
RETURNING ` + shopOrderColumns

// InsertShopOrder creates an order; its number comes from
// compatibility_system_id_seq.
func (q *Queries) InsertShopOrder(ctx context.Context, p InsertShopOrderParams) (ShopOrderRow, error) {
	if p.Status == "" {
		p.Status = "pending_payment"
	}
	if p.Source == "" {
		p.Source = "web"
	}
	if p.DeliveryKind == "" {
		p.DeliveryKind = "shipping"
	}
	return scanShopOrderRow(q.db.QueryRow(ctx, insertShopOrder, p.OrgID, p.OrderToken, p.Status, p.BuyerName,
		p.BuyerEmail, p.BuyerPhone, p.ShipName, p.ShipAddress1, p.ShipAddress2, p.ShipCity, p.ShipPostal,
		p.ShipCountry, p.DeliveryKind, p.DeliveryNote, p.Currency, p.Subtotal, p.Discount, p.ShippingFee,
		p.Total, p.BuyerLocale, p.Source, p.ExternalRef, p.ExpiresAt, p.CustomerID))
}

// ShopOrderItemRow is one line of an order.
type ShopOrderItemRow struct {
	ID             uuid.UUID  `json:"id"`
	OrderID        uuid.UUID  `json:"order_id"`
	VariantID      *uuid.UUID `json:"variant_id"`
	ProductTitle   string     `json:"product_title"`
	VariantTitle   string     `json:"variant_title"`
	SKU            *string    `json:"sku"`
	Qty            int32      `json:"qty"`
	UnitPriceMinor int64      `json:"unit_price_minor"`
	TotalMinor     int64      `json:"total_minor"`
	Ordinal        int32      `json:"ordinal"`
}

const insertShopOrderItem = `-- name: InsertShopOrderItem :exec
INSERT INTO shop_order_items (order_id, variant_id, product_title, variant_title, sku, qty,
                              unit_price_minor, total_minor, ordinal)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

// InsertShopOrderItem adds a line to an order (ID is ignored).
func (q *Queries) InsertShopOrderItem(ctx context.Context, it ShopOrderItemRow) error {
	_, err := q.db.Exec(ctx, insertShopOrderItem, it.OrderID, it.VariantID, it.ProductTitle, it.VariantTitle,
		it.SKU, it.Qty, it.UnitPriceMinor, it.TotalMinor, it.Ordinal)
	return err
}

const listShopOrderItems = `-- name: ListShopOrderItems :many
SELECT id, order_id, variant_id, product_title, variant_title, sku, qty, unit_price_minor, total_minor, ordinal
FROM   shop_order_items
WHERE  order_id = ANY($1::uuid[])
ORDER  BY order_id, ordinal`

// ListShopOrderItems lists the lines of the given orders in order.
func (q *Queries) ListShopOrderItems(ctx context.Context, orderIDs []uuid.UUID) ([]ShopOrderItemRow, error) {
	rows, err := q.db.Query(ctx, listShopOrderItems, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopOrderItemRow
	for rows.Next() {
		var it ShopOrderItemRow
		if err := rows.Scan(&it.ID, &it.OrderID, &it.VariantID, &it.ProductTitle, &it.VariantTitle, &it.SKU,
			&it.Qty, &it.UnitPriceMinor, &it.TotalMinor, &it.Ordinal); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

const insertShopStockMove = `-- name: InsertShopStockMove :exec
INSERT INTO shop_stock_moves (org_id, variant_id, order_id, delta, reason)
VALUES ($1, $2, $3, $4, $5)`

// InsertShopStockMove journals one stock or hold movement.
func (q *Queries) InsertShopStockMove(ctx context.Context, orgID, variantID uuid.UUID, orderID *uuid.UUID, delta int32, reason string) error {
	_, err := q.db.Exec(ctx, insertShopStockMove, orgID, variantID, orderID, delta, reason)
	return err
}

const getShopOrder = `-- name: GetShopOrder :one
SELECT ` + shopOrderColumns + ` FROM shop_orders WHERE id = $1 AND org_id = $2`

// GetShopOrder reads an order of the organization.
func (q *Queries) GetShopOrder(ctx context.Context, id, orgID uuid.UUID) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, getShopOrder, id, orgID))
}

const getShopOrderForUpdate = `-- name: GetShopOrderForUpdate :one
SELECT ` + shopOrderColumns + ` FROM shop_orders WHERE id = $1 AND org_id = $2 FOR UPDATE`

// GetShopOrderForUpdate reads and locks an order of the organization.
func (q *Queries) GetShopOrderForUpdate(ctx context.Context, id, orgID uuid.UUID) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, getShopOrderForUpdate, id, orgID))
}

const getShopOrderByToken = `-- name: GetShopOrderByToken :one
SELECT ` + shopOrderColumns + ` FROM shop_orders WHERE order_token = $1`

// GetShopOrderByToken reads the order a buyer holds the token of.
func (q *Queries) GetShopOrderByToken(ctx context.Context, token string) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, getShopOrderByToken, token))
}

const getShopOrderByPaymentIntentForUpdate = `-- name: GetShopOrderByPaymentIntentForUpdate :one
SELECT ` + shopOrderColumns + ` FROM shop_orders WHERE payment_intent_id = $1 FOR UPDATE`

// GetShopOrderByPaymentIntentForUpdate reads and locks the order a payment
// intent pays for. pgx.ErrNoRows: the payment is not a shop payment.
func (q *Queries) GetShopOrderByPaymentIntentForUpdate(ctx context.Context, paymentIntentID uuid.UUID) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, getShopOrderByPaymentIntentForUpdate, paymentIntentID))
}

// ListShopOrdersParams filters the order list. Empty Status lists every
// status; Query matches the order number exactly or the buyer's e-mail or
// name as a case-insensitive substring; From/To bound created_at
// (inclusive, exclusive).
type ListShopOrdersParams struct {
	OrgID  uuid.UUID
	Status string
	Query  string
	From   *time.Time
	To     *time.Time
	Limit  int32
	Offset int32
}

const listShopOrders = `-- name: ListShopOrders :many
SELECT ` + shopOrderColumns + `
FROM   shop_orders
WHERE  org_id = $1
  AND  ($2::text = '' OR status = $2)
  AND  ($3::text = '' OR number::text = $3
        OR buyer_email ILIKE '%' || $3 || '%' OR buyer_name ILIKE '%' || $3 || '%')
  AND  ($4::timestamptz IS NULL OR created_at >= $4)
  AND  ($5::timestamptz IS NULL OR created_at < $5)
ORDER  BY created_at DESC, id DESC
LIMIT  $6 OFFSET $7`

// ListShopOrders lists one page of an organization's orders, newest first.
func (q *Queries) ListShopOrders(ctx context.Context, p ListShopOrdersParams) ([]ShopOrderRow, error) {
	rows, err := q.db.Query(ctx, listShopOrders, p.OrgID, p.Status, p.Query, p.From, p.To, p.Limit, p.Offset)
	if err != nil {
		return nil, err
	}
	return collectShopOrders(rows)
}

const setShopOrderPayment = `-- name: SetShopOrderPayment :exec
UPDATE shop_orders SET payment_intent_id = $2, provider = $3, updated_at = now() WHERE id = $1`

// SetShopOrderPayment links an order to the payment intent that pays for it.
func (q *Queries) SetShopOrderPayment(ctx context.Context, id, paymentIntentID uuid.UUID, provider string) error {
	_, err := q.db.Exec(ctx, setShopOrderPayment, id, paymentIntentID, provider)
	return err
}

const setShopOrderStatus = `-- name: SetShopOrderStatus :one
UPDATE shop_orders
SET    status = $2,
       paid_at = CASE WHEN $2 IN ('paid', 'manual_review') THEN COALESCE(paid_at, now()) ELSE paid_at END,
       shipped_at = CASE WHEN $2 = 'shipped' THEN now() ELSE shipped_at END,
       cancelled_at = CASE WHEN $2 = 'cancelled' THEN now() ELSE cancelled_at END,
       tracking_note = COALESCE($3, tracking_note),
       metadata = metadata || COALESCE($4::jsonb, '{}'::jsonb),
       updated_at = now()
WHERE  id = $1
RETURNING ` + shopOrderColumns

// SetShopOrderStatus moves an order to status, stamping paid_at (paid, and
// manual_review: a late payment parked for the operator), shipped_at or
// cancelled_at for those statuses. trackingNote nil keeps the note; meta is
// merged into metadata (nil merges nothing). The caller decides whether the
// transition is allowed, under the order's lock.
func (q *Queries) SetShopOrderStatus(ctx context.Context, id uuid.UUID, status string, trackingNote *string, meta json.RawMessage) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, setShopOrderStatus, id, status, trackingNote, nullIfEmptyJSON(meta)))
}

const setShopOrderRefunded = `-- name: SetShopOrderRefunded :one
UPDATE shop_orders SET status = $2, refunded_minor = $3, updated_at = now()
WHERE  id = $1
RETURNING ` + shopOrderColumns

// SetShopOrderRefunded records how much of the order went back and the status
// that follows from it.
func (q *Queries) SetShopOrderRefunded(ctx context.Context, id uuid.UUID, status string, refunded int64) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, setShopOrderRefunded, id, status, refunded))
}

const lockShopOrderVariants = `-- name: LockShopOrderVariants :exec
SELECT v.id FROM shop_variants v
WHERE  v.id IN (SELECT oi.variant_id FROM shop_order_items oi WHERE oi.order_id = $1 AND oi.variant_id IS NOT NULL)
ORDER  BY v.id
FOR UPDATE OF v`

// LockShopOrderVariants locks the variant rows of an order's lines, in id
// order like the checkout, before anything reads or changes their stock.
func (q *Queries) LockShopOrderVariants(ctx context.Context, orderID uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockShopOrderVariants, orderID)
	return err
}

const shopOrderShortages = `-- name: ShopOrderShortages :many
SELECT l.variant_id, l.qty, v.stock, ` + ShopHeldSQL + `
FROM   (SELECT oi.variant_id, sum(oi.qty)::int AS qty
        FROM   shop_order_items oi
        WHERE  oi.order_id = $1 AND oi.variant_id IS NOT NULL
        GROUP  BY oi.variant_id) l
JOIN   shop_variants v ON v.id = l.variant_id
WHERE  v.track_stock
ORDER  BY v.id`

// ShopOrderLine is one tracked variant of an order with its shelf stock and
// what other pending orders hold of it.
type ShopOrderLine struct {
	VariantID uuid.UUID `json:"variant_id"`
	Qty       int32     `json:"qty"`
	Stock     int32     `json:"stock"`
	Held      int32     `json:"held"`
}

// ShopOrderShortages reads, for every stock-tracking variant of the order,
// the ordered quantity, the stock and the quantity live holds keep. Call it
// after LockShopOrderVariants, as its own statement, so the held quantity is
// read after the lock (READ COMMITTED: a locking statement keeps its older
// snapshot for everything but the locked rows).
func (q *Queries) ShopOrderShortages(ctx context.Context, orderID uuid.UUID) ([]ShopOrderLine, error) {
	rows, err := q.db.Query(ctx, shopOrderShortages, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopOrderLine
	for rows.Next() {
		var l ShopOrderLine
		if err := rows.Scan(&l.VariantID, &l.Qty, &l.Stock, &l.Held); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

const applyShopOrderSale = `-- name: ApplyShopOrderSale :exec
WITH lines AS (
    SELECT oi.variant_id, sum(oi.qty)::int AS qty
    FROM   shop_order_items oi
    WHERE  oi.order_id = $1 AND oi.variant_id IS NOT NULL
    GROUP  BY oi.variant_id
), moved AS (
    UPDATE shop_variants v SET stock = v.stock - l.qty, updated_at = now()
    FROM   lines l
    WHERE  v.id = l.variant_id AND v.track_stock
    RETURNING v.id, l.qty
)
INSERT INTO shop_stock_moves (org_id, variant_id, order_id, delta, reason)
SELECT $2, id, $1, -qty, 'sale' FROM moved`

// ApplyShopOrderSale takes a paid order's quantities off the shelf (variants
// that track stock) and journals each as a sale. Call LockShopOrderVariants
// first so concurrent writers take the variant locks in one order. The order's hold ends by
// itself when its status leaves pending_payment.
func (q *Queries) ApplyShopOrderSale(ctx context.Context, orderID, orgID uuid.UUID) error {
	_, err := q.db.Exec(ctx, applyShopOrderSale, orderID, orgID)
	return err
}

const journalShopOrderHold = `-- name: JournalShopOrderHold :exec
INSERT INTO shop_stock_moves (org_id, variant_id, order_id, delta, reason)
SELECT $2, oi.variant_id, $1, CASE WHEN $3 = 'reserve' THEN -sum(oi.qty) ELSE sum(oi.qty) END, $3
FROM   shop_order_items oi
WHERE  oi.order_id = $1 AND oi.variant_id IS NOT NULL
GROUP  BY oi.variant_id`

// JournalShopOrderHold journals the start (reason "reserve") or the end
// ("release") of an order's hold, one row per variant.
func (q *Queries) JournalShopOrderHold(ctx context.Context, orderID, orgID uuid.UUID, reason string) error {
	_, err := q.db.Exec(ctx, journalShopOrderHold, orderID, orgID, reason)
	return err
}

const expireShopOrders = `-- name: ExpireShopOrders :many
WITH due AS (
    SELECT o.id
    FROM   shop_orders o
    WHERE  o.status = 'pending_payment' AND o.expires_at <= now()
      AND  NOT EXISTS (SELECT 1 FROM payment_intents pi
                       WHERE pi.id = o.payment_intent_id AND pi.state IN ('processing', 'succeeded', 'manual_review'))
    ORDER  BY o.expires_at
    LIMIT  $1
    FOR UPDATE SKIP LOCKED
)
UPDATE shop_orders o SET status = 'expired', updated_at = now()
FROM   due
WHERE  o.id = due.id
RETURNING o.id, o.org_id`

// ExpireShopOrders flips at most limit pending orders whose hold passed to
// expired and returns them (id, org id). An order whose payment is still
// processing (an asynchronous method on its way), already succeeded, or is
// parked for review, is left for the webhook and the operator. The caller journals the releases in the same transaction.
func (q *Queries) ExpireShopOrders(ctx context.Context, limit int32) ([][2]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, expireShopOrders, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]uuid.UUID
	for rows.Next() {
		var id, org uuid.UUID
		if err := rows.Scan(&id, &org); err != nil {
			return nil, err
		}
		out = append(out, [2]uuid.UUID{id, org})
	}
	return out, rows.Err()
}

const sumShopPaymentRefunds = `-- name: SumShopPaymentRefunds :one
SELECT COALESCE(sum(amount) FILTER (WHERE state = 'succeeded'
                OR (state = 'provider_pending' AND provider_refund_id IS NOT NULL)), 0)::bigint,
       COALESCE(sum(amount) FILTER (WHERE state NOT IN ('failed', 'rejected')), 0)::bigint
FROM   refunds
WHERE  payment_intent_id = $1`

// SumShopPaymentRefunds sums a payment's refunds: accepted is what the
// provider took back (succeeded, or pending with a provider refund id), live
// every refund that may still return money (not failed or rejected).
func (q *Queries) SumShopPaymentRefunds(ctx context.Context, paymentIntentID uuid.UUID) (accepted, live int64, err error) {
	err = q.db.QueryRow(ctx, sumShopPaymentRefunds, paymentIntentID).Scan(&accepted, &live)
	return accepted, live, err
}
