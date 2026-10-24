// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: sales_links.sql

package gen

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DefaultSalesLinkShortHost is the host new sales links are issued under
// (migration 0167 default for sales_links.short_host).
const DefaultSalesLinkShortHost = "go.arenasoldout.com" // allow:shortlink stored default, mirrors the column default of migration 0167

// SalesLinkRow is one tracked sales link (migration 0167, spec 43 section 3).
//
// OrdersCount is the number of orders that came through the link and were
// ever paid (paid, partially_refunded, refunded); every query that returns a
// SalesLinkRow fills it, a freshly inserted link has 0.
type SalesLinkRow struct {
	ID               uuid.UUID       `json:"id"`
	OrgID            uuid.UUID       `json:"org_id"`
	Code             string          `json:"code"`
	ShortHost        string          `json:"short_host"`
	TargetKind       string          `json:"target_kind"`
	EventID          *uuid.UUID      `json:"event_id"`
	SessionID        *uuid.UUID      `json:"session_id"`
	PromoterID       *uuid.UUID      `json:"promoter_id"`
	URL              *string         `json:"url"`
	Label            string          `json:"label"`
	PlacementKind    string          `json:"placement_kind"`
	UTM              json.RawMessage `json:"utm"`
	Status           string          `json:"status"`
	ScanCount        int64           `json:"scan_count"`
	SuspectScanCount int64           `json:"suspect_scan_count"`
	LastScannedAt    *time.Time      `json:"last_scanned_at"`
	CreatedBy        *uuid.UUID      `json:"created_by"`
	CreatedVia       *string         `json:"created_via"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	OrdersCount      int64           `json:"orders_count"`
}

func scanSalesLinkRow(row interface {
	Scan(dest ...any) error
}) (SalesLinkRow, error) {
	var l SalesLinkRow
	err := row.Scan(
		&l.ID, &l.OrgID, &l.Code, &l.ShortHost, &l.TargetKind,
		&l.EventID, &l.SessionID, &l.PromoterID, &l.URL,
		&l.Label, &l.PlacementKind, &l.UTM, &l.Status,
		&l.ScanCount, &l.SuspectScanCount, &l.LastScannedAt,
		&l.CreatedBy, &l.CreatedVia, &l.CreatedAt, &l.UpdatedAt,
		&l.OrdersCount,
	)
	return l, err
}

const salesLinkColumns = `sales_links.id, sales_links.org_id, sales_links.code, sales_links.short_host,
       sales_links.target_kind, sales_links.event_id, sales_links.session_id,
       sales_links.promoter_id, sales_links.url, sales_links.label,
       sales_links.placement_kind, sales_links.utm, sales_links.status,
       sales_links.scan_count, sales_links.suspect_scan_count, sales_links.last_scanned_at,
       sales_links.created_by, sales_links.created_via, sales_links.created_at,
       sales_links.updated_at,
       (SELECT count(*) FROM orders o WHERE o.sales_link_id = sales_links.id
                AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS orders_count`

// ─────────────────────────────────────────────────────────────────────────────
// InsertSalesLinkIfCodeFree
// ─────────────────────────────────────────────────────────────────────────────

const insertSalesLinkIfCodeFree = `-- name: InsertSalesLinkIfCodeFree :one
INSERT INTO sales_links (org_id, code, short_host, target_kind, event_id, session_id,
                         promoter_id, url, label, placement_kind, utm, created_by, created_via)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, COALESCE($11::jsonb, '{}'::jsonb), $12, $13)
ON CONFLICT (code) DO NOTHING
RETURNING ` + salesLinkColumns

// InsertSalesLinkParams are the columns of a new sales link. ShortHost ""
// means DefaultSalesLinkShortHost, PlacementKind "" means "poster", UTM nil
// means "{}".
type InsertSalesLinkParams struct {
	OrgID         uuid.UUID
	Code          string
	ShortHost     string
	TargetKind    string
	EventID       *uuid.UUID
	SessionID     *uuid.UUID
	PromoterID    *uuid.UUID
	URL           *string
	Label         string
	PlacementKind string
	UTM           json.RawMessage
	CreatedBy     *uuid.UUID
	CreatedVia    *string
}

// InsertSalesLinkIfCodeFree claims p.Code atomically. inserted=false with a
// nil error means the code is taken: nothing was written and the caller draws
// a new code (the same shape as InsertBarcodeIfUnique, safe inside a
// transaction because a lost code is zero rows, never a 23505). Any other
// failure, a CHECK violation included, is a non-nil error.
func (q *Queries) InsertSalesLinkIfCodeFree(ctx context.Context, p InsertSalesLinkParams) (SalesLinkRow, bool, error) {
	if p.ShortHost == "" {
		p.ShortHost = DefaultSalesLinkShortHost
	}
	if p.PlacementKind == "" {
		p.PlacementKind = "poster"
	}
	row := q.db.QueryRow(ctx, insertSalesLinkIfCodeFree,
		p.OrgID, p.Code, p.ShortHost, p.TargetKind, p.EventID, p.SessionID,
		p.PromoterID, p.URL, p.Label, p.PlacementKind, nullIfEmptyJSON(p.UTM),
		p.CreatedBy, p.CreatedVia,
	)
	l, err := scanSalesLinkRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SalesLinkRow{}, false, nil
		}
		return SalesLinkRow{}, false, err
	}
	return l, true, nil
}

// nullIfEmptyJSON passes a nil or empty json document as SQL NULL, so the
// query's COALESCE applies the column default.
func nullIfEmptyJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSalesLinkByCode / GetSalesLinkByID
// ─────────────────────────────────────────────────────────────────────────────

const getSalesLinkByCode = `-- name: GetSalesLinkByCode :one
SELECT ` + salesLinkColumns + `
FROM sales_links WHERE code = lower($1)`

// GetSalesLinkByCode finds a link by its printed code, ignoring case. The
// public redirect uses it: it is NOT organization-scoped. Returns
// pgx.ErrNoRows when the code is unknown.
func (q *Queries) GetSalesLinkByCode(ctx context.Context, code string) (SalesLinkRow, error) {
	return scanSalesLinkRow(q.db.QueryRow(ctx, getSalesLinkByCode, code))
}

const getSalesLinkByID = `-- name: GetSalesLinkByID :one
SELECT ` + salesLinkColumns + `
FROM sales_links WHERE id = $1 AND org_id = $2`

// GetSalesLinkByID returns one link of the organization. Returns
// pgx.ErrNoRows for an unknown id or another organization's link.
func (q *Queries) GetSalesLinkByID(ctx context.Context, id, orgID uuid.UUID) (SalesLinkRow, error) {
	return scanSalesLinkRow(q.db.QueryRow(ctx, getSalesLinkByID, id, orgID))
}

// ─────────────────────────────────────────────────────────────────────────────
// ListSalesLinks
// ─────────────────────────────────────────────────────────────────────────────

const listSalesLinks = `-- name: ListSalesLinks :many
SELECT ` + salesLinkColumns + `
FROM sales_links
WHERE org_id = $1
  AND ($2::uuid IS NULL OR event_id = $2)
  AND ($3::uuid IS NULL OR session_id = $3)
  AND ($4::text = '' OR status = $4)
  AND ($5::timestamptz IS NULL OR (created_at, id) < ($5, $6::uuid))
ORDER BY created_at DESC, id DESC
LIMIT $7`

// ListSalesLinksParams filters a page of links. Status is "", "active" or
// "archived". The cursor is the CreatedAt and ID of the last row of the
// previous page (both nil for the first page).
type ListSalesLinksParams struct {
	OrgID           uuid.UUID
	EventID         *uuid.UUID
	SessionID       *uuid.UUID
	Status          string
	CursorCreatedAt *time.Time
	CursorID        *uuid.UUID
	Limit           int32
}

// ListSalesLinks returns links newest first, at most p.Limit of them. The
// caller passes limit+1 to learn whether a next page exists. Status "" means
// every status. The cursor counts only when BOTH CursorCreatedAt and CursorID
// are set; one of them alone is ignored (a half cursor would compare NULL and
// return an empty page).
func (q *Queries) ListSalesLinks(ctx context.Context, p ListSalesLinksParams) ([]SalesLinkRow, error) {
	if p.CursorCreatedAt == nil || p.CursorID == nil {
		p.CursorCreatedAt, p.CursorID = nil, nil
	}
	rows, err := q.db.Query(ctx, listSalesLinks,
		p.OrgID, p.EventID, p.SessionID, p.Status, p.CursorCreatedAt, p.CursorID, p.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SalesLinkRow
	for rows.Next() {
		l, err := scanSalesLinkRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateSalesLink
// ─────────────────────────────────────────────────────────────────────────────

const updateSalesLink = `-- name: UpdateSalesLink :one
UPDATE sales_links
SET label          = CASE WHEN $3::boolean  THEN $4::text  ELSE label          END,
    placement_kind = CASE WHEN $5::boolean  THEN $6::text  ELSE placement_kind END,
    utm            = CASE WHEN $7::boolean  THEN COALESCE($8::jsonb, '{}'::jsonb) ELSE utm END,
    status         = CASE WHEN $9::boolean  THEN $10::text ELSE status         END,
    target_kind    = CASE WHEN $11::boolean THEN $12::text ELSE target_kind    END,
    event_id       = CASE WHEN $11::boolean THEN $13::uuid ELSE event_id       END,
    session_id     = CASE WHEN $11::boolean THEN $14::uuid ELSE session_id     END,
    promoter_id    = CASE WHEN $11::boolean THEN $15::uuid ELSE promoter_id    END,
    url            = CASE WHEN $11::boolean THEN $16::text ELSE url            END,
    updated_at     = now()
WHERE id = $1 AND org_id = $2
RETURNING ` + salesLinkColumns

// UpdateSalesLinkParams is a partial update: a field changes only when its
// Set flag is true, otherwise the stored value stays (the set_<field> pattern
// of UpdateSalesChannel's reservation_ttl_override).
//
// The target is ONE group: SetTarget writes TargetKind, EventID, SessionID,
// PromoterID and URL together (a nil pointer clears that column), so a
// retarget can never leave a stale id of the old kind behind.
type UpdateSalesLinkParams struct {
	ID    uuid.UUID
	OrgID uuid.UUID

	SetLabel bool
	Label    string

	SetPlacementKind bool
	PlacementKind    string

	SetUTM bool
	UTM    json.RawMessage

	SetStatus bool
	Status    string

	SetTarget  bool
	TargetKind string
	EventID    *uuid.UUID
	SessionID  *uuid.UUID
	PromoterID *uuid.UUID
	URL        *string
}

// UpdateSalesLink applies the flagged fields and always bumps updated_at.
// Returns pgx.ErrNoRows for an unknown id or another organization's link.
func (q *Queries) UpdateSalesLink(ctx context.Context, p UpdateSalesLinkParams) (SalesLinkRow, error) {
	row := q.db.QueryRow(ctx, updateSalesLink,
		p.ID, p.OrgID,
		p.SetLabel, p.Label,
		p.SetPlacementKind, p.PlacementKind,
		p.SetUTM, nullIfEmptyJSON(p.UTM),
		p.SetStatus, p.Status,
		p.SetTarget, p.TargetKind, p.EventID, p.SessionID, p.PromoterID, p.URL,
	)
	return scanSalesLinkRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// BumpSalesLinkScan / UpsertSalesLinkHit
// ─────────────────────────────────────────────────────────────────────────────

const bumpSalesLinkScan = `-- name: BumpSalesLinkScan :exec
UPDATE sales_links
SET scan_count         = scan_count + CASE WHEN $2::boolean THEN 0 ELSE 1 END,
    suspect_scan_count = suspect_scan_count + CASE WHEN $2::boolean THEN 1 ELSE 0 END,
    last_scanned_at    = now()
WHERE id = $1`

// BumpSalesLinkScan counts one scan of the link: a suspect scan (a bot-like
// client) goes to suspect_scan_count instead of scan_count.
func (q *Queries) BumpSalesLinkScan(ctx context.Context, id uuid.UUID, suspect bool) error {
	_, err := q.db.Exec(ctx, bumpSalesLinkScan, id, suspect)
	return err
}

const upsertSalesLinkHit = `-- name: UpsertSalesLinkHit :exec
INSERT INTO sales_link_hits (sales_link_id, day, country, hits, suspect_hits)
VALUES ($1, $2::date, $3, CASE WHEN $4::boolean THEN 0 ELSE 1 END, CASE WHEN $4::boolean THEN 1 ELSE 0 END)
ON CONFLICT (sales_link_id, day, country) DO UPDATE
   SET hits         = sales_link_hits.hits         + EXCLUDED.hits,
       suspect_hits = sales_link_hits.suspect_hits + EXCLUDED.suspect_hits`

// UpsertSalesLinkHit adds one scan to the link's counter for the calendar day
// and the two-letter country ("" when unknown). Only the date part of day is
// used.
func (q *Queries) UpsertSalesLinkHit(ctx context.Context, id uuid.UUID, day time.Time, country string, suspect bool) error {
	_, err := q.db.Exec(ctx, upsertSalesLinkHit, id, day, country, suspect)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// SalesLinkOrdersAggregate
// ─────────────────────────────────────────────────────────────────────────────

const salesLinkOrdersAggregate = `-- name: SalesLinkOrdersAggregate :many
WITH link_orders AS (
    SELECT o.id, o.currency, o.total, o.checkout_session_id
    FROM   orders o
    JOIN   sales_links sl ON sl.id = o.sales_link_id AND sl.org_id = $2
    WHERE  o.sales_link_id = $1
      AND  o.org_id        = $2
      AND  o.status IN ('paid', 'partially_refunded', 'refunded')
)
SELECT lo.currency,
       count(*)::bigint AS orders,
       COALESCE(sum(tk.cnt), 0)::bigint AS tickets,
       (COALESCE(sum(lo.total), 0) - COALESCE(sum(rf.amount), 0))::bigint AS revenue_minor
FROM   link_orders lo
LEFT JOIN LATERAL (
    SELECT count(*) AS cnt FROM tickets t WHERE t.order_id = lo.id
) tk ON true
LEFT JOIN LATERAL (
    SELECT COALESCE(sum(r.amount), 0) AS amount
    FROM   refunds r
    WHERE  r.state  = 'succeeded'
      AND  r.org_id = $2
      AND  COALESCE(r.order_id,
                    (SELECT t.order_id FROM tickets t WHERE t.id = r.ticket_id),
                    (SELECT po.id FROM payment_intents pi
                     JOIN   orders po ON po.checkout_session_id = pi.checkout_session_id
                     WHERE  pi.id = r.payment_intent_id)) = lo.id
) rf ON true
GROUP  BY lo.currency
ORDER  BY lo.currency`

// SalesLinkCurrencyTotals is the sales of one link in one currency.
type SalesLinkCurrencyTotals struct {
	Currency     string `json:"currency"`
	Orders       int64  `json:"orders"`
	Tickets      int64  `json:"tickets"`
	RevenueMinor int64  `json:"revenue_minor"`
}

// SalesLinkOrdersAggregate sums the orders that came through the link and
// were ever paid, per currency: their count, their tickets, and the revenue =
// sum of orders.total minus the SUCCEEDED refunds of those orders (minor
// units). A link of another organization gives no rows.
func (q *Queries) SalesLinkOrdersAggregate(ctx context.Context, id, orgID uuid.UUID) ([]SalesLinkCurrencyTotals, error) {
	rows, err := q.db.Query(ctx, salesLinkOrdersAggregate, id, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SalesLinkCurrencyTotals
	for rows.Next() {
		var t SalesLinkCurrencyTotals
		if err := rows.Scan(&t.Currency, &t.Orders, &t.Tickets, &t.RevenueMinor); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// SalesLinkHitsByDay / SalesLinkHitsByCountry
// ─────────────────────────────────────────────────────────────────────────────

const salesLinkHitsByDay = `-- name: SalesLinkHitsByDay :many
SELECT h.day, sum(h.hits)::bigint AS hits, sum(h.suspect_hits)::bigint AS suspect_hits
FROM   sales_link_hits h
JOIN   sales_links sl ON sl.id = h.sales_link_id
WHERE  h.sales_link_id = $1
  AND  sl.org_id       = $2
  AND  h.day >= $3::date AND h.day <= $4::date
GROUP  BY h.day
ORDER  BY h.day`

// SalesLinkDayHits is the scans of one calendar day.
type SalesLinkDayHits struct {
	Day         time.Time `json:"day"`
	Hits        int64     `json:"hits"`
	SuspectHits int64     `json:"suspect_hits"`
}

// SalesLinkHitsByDay returns the scans per calendar day, from and to both
// inclusive, oldest first. A link of another organization gives no rows.
func (q *Queries) SalesLinkHitsByDay(ctx context.Context, id, orgID uuid.UUID, from, to time.Time) ([]SalesLinkDayHits, error) {
	rows, err := q.db.Query(ctx, salesLinkHitsByDay, id, orgID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SalesLinkDayHits
	for rows.Next() {
		var d SalesLinkDayHits
		if err := rows.Scan(&d.Day, &d.Hits, &d.SuspectHits); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const salesLinkHitsByCountry = `-- name: SalesLinkHitsByCountry :many
SELECT h.country, sum(h.hits)::bigint AS hits, sum(h.suspect_hits)::bigint AS suspect_hits
FROM   sales_link_hits h
JOIN   sales_links sl ON sl.id = h.sales_link_id
WHERE  h.sales_link_id = $1
  AND  sl.org_id       = $2
  AND  h.day >= $3::date AND h.day <= $4::date
GROUP  BY h.country
ORDER  BY sum(h.hits) DESC, h.country`

// SalesLinkCountryHits is the scans from one country ("" = unknown).
type SalesLinkCountryHits struct {
	Country     string `json:"country"`
	Hits        int64  `json:"hits"`
	SuspectHits int64  `json:"suspect_hits"`
}

// SalesLinkHitsByCountry returns the scans per country over the day range
// (both inclusive), the biggest first. A link of another organization gives
// no rows.
func (q *Queries) SalesLinkHitsByCountry(ctx context.Context, id, orgID uuid.UUID, from, to time.Time) ([]SalesLinkCountryHits, error) {
	rows, err := q.db.Query(ctx, salesLinkHitsByCountry, id, orgID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SalesLinkCountryHits
	for rows.Next() {
		var c SalesLinkCountryHits
		if err := rows.Scan(&c.Country, &c.Hits, &c.SuspectHits); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
