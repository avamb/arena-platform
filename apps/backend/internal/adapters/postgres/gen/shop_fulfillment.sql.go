package gen

// shop_fulfillment.sql.go — the delivery steps of a shop order (migration
// 0199, spec 08_architecture/61 section 7b): the step column of shop_orders and
// its history in shop_order_events. Hand-written like shop_orders.sql.go.

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ShopOrderEventRow is one row of shop_order_events: a step change of an
// order, who made it and from where.
type ShopOrderEventRow struct {
	ID       uuid.UUID `json:"id"`
	OrgID    uuid.UUID `json:"org_id"`
	OrderID  uuid.UUID `json:"order_id"`
	Step     string    `json:"step"`
	FromStep *string   `json:"from_step"`
	Tracking string    `json:"tracking"`
	Reason   string    `json:"reason"`
	Note     string    `json:"note"`
	// ActorUserID is the platform user who set the step; ActorTelegramID the
	// Telegram account of a delivery group member. ActorName is what is shown.
	ActorUserID     *uuid.UUID `json:"actor_user_id"`
	ActorTelegramID *int64     `json:"actor_telegram_id"`
	ActorName       string     `json:"actor_name"`
	Via             string     `json:"via"`
	CreatedAt       time.Time  `json:"created_at"`
}

const shopOrderEventColumns = `id, org_id, order_id, step, from_step, tracking, reason, note,
       actor_user_id, actor_telegram_id, actor_name, via, created_at`

func scanShopOrderEvent(row interface{ Scan(dest ...any) error }) (ShopOrderEventRow, error) {
	var e ShopOrderEventRow
	err := row.Scan(&e.ID, &e.OrgID, &e.OrderID, &e.Step, &e.FromStep, &e.Tracking, &e.Reason, &e.Note,
		&e.ActorUserID, &e.ActorTelegramID, &e.ActorName, &e.Via, &e.CreatedAt)
	return e, err
}

const setShopOrderFulfillmentStep = `-- name: SetShopOrderFulfillmentStep :one
UPDATE shop_orders SET fulfillment_step = $2, updated_at = now()
WHERE  id = $1
RETURNING ` + shopOrderColumns

// SetShopOrderFulfillmentStep writes the order's delivery step. The caller
// decides whether the move is allowed, under the order's lock.
func (q *Queries) SetShopOrderFulfillmentStep(ctx context.Context, id uuid.UUID, step string) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, setShopOrderFulfillmentStep, id, step))
}

const setShopOrderTracking = `-- name: SetShopOrderTracking :one
UPDATE shop_orders SET tracking_note = $2, updated_at = now()
WHERE  id = $1
RETURNING ` + shopOrderColumns

// SetShopOrderTracking replaces the tracking note alone: status, shipped_at
// and the step stay.
func (q *Queries) SetShopOrderTracking(ctx context.Context, id uuid.UUID, note string) (ShopOrderRow, error) {
	return scanShopOrderRow(q.db.QueryRow(ctx, setShopOrderTracking, id, note))
}

const insertShopOrderEvent = `-- name: InsertShopOrderEvent :one
INSERT INTO shop_order_events (org_id, order_id, step, from_step, tracking, reason, note,
                               actor_user_id, actor_telegram_id, actor_name, via)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING ` + shopOrderEventColumns

// InsertShopOrderEvent records one step change. ID and CreatedAt of e are
// ignored.
func (q *Queries) InsertShopOrderEvent(ctx context.Context, e ShopOrderEventRow) (ShopOrderEventRow, error) {
	return scanShopOrderEvent(q.db.QueryRow(ctx, insertShopOrderEvent, e.OrgID, e.OrderID, e.Step, e.FromStep,
		e.Tracking, e.Reason, e.Note, e.ActorUserID, e.ActorTelegramID, e.ActorName, e.Via))
}

const listShopOrderEvents = `-- name: ListShopOrderEvents :many
SELECT ` + shopOrderEventColumns + `
FROM   shop_order_events
WHERE  order_id = $1 AND org_id = $2
ORDER  BY created_at, id`

// ListShopOrderEvents is the step history of one order, oldest first.
func (q *Queries) ListShopOrderEvents(ctx context.Context, orderID, orgID uuid.UUID) ([]ShopOrderEventRow, error) {
	rows, err := q.db.Query(ctx, listShopOrderEvents, orderID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShopOrderEventRow{}
	for rows.Next() {
		e, err := scanShopOrderEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const latestShopOrderEvent = `-- name: LatestShopOrderEvent :one
SELECT ` + shopOrderEventColumns + `
FROM   shop_order_events
WHERE  order_id = $1
ORDER  BY created_at DESC, id DESC
LIMIT  1`

// LatestShopOrderEvent is the newest step change of an order; ok is false
// when it has none.
func (q *Queries) LatestShopOrderEvent(ctx context.Context, orderID uuid.UUID) (e ShopOrderEventRow, ok bool, err error) {
	e, err = scanShopOrderEvent(q.db.QueryRow(ctx, latestShopOrderEvent, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ShopOrderEventRow{}, false, nil
	}
	return e, err == nil, err
}

const shopOrderHadStep = `-- name: ShopOrderHadStep :one
SELECT EXISTS (SELECT 1 FROM shop_order_events WHERE order_id = $1 AND step = $2)`

// ShopOrderHadStep reports whether the order ever reached step.
func (q *Queries) ShopOrderHadStep(ctx context.Context, orderID uuid.UUID, step string) (bool, error) {
	var had bool
	err := q.db.QueryRow(ctx, shopOrderHadStep, orderID, step).Scan(&had)
	return had, err
}
