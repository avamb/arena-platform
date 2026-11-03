// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: white_label.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// WhiteLabelPeriodRow is one white_label_periods row (migration 0188).
// StartsOn and EndsOn are UTC calendar days, both inclusive; a nil EndsOn has
// no end. MonthlyPriceMinor is the individual monthly price (nil = the price
// list) in Currency, the billing account's currency when the period was made
// (both nil or both set).
type WhiteLabelPeriodRow struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	PromoterID        *uuid.UUID `json:"promoter_id"`
	StartsOn          time.Time  `json:"starts_on"`
	EndsOn            *time.Time `json:"ends_on"`
	MonthlyPriceMinor *int64     `json:"monthly_price_minor"`
	Currency          *string    `json:"currency"`
	Reason            string     `json:"reason"`
	CreatedBy         *uuid.UUID `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	EndedBy           *uuid.UUID `json:"ended_by"`
	EndedAt           *time.Time `json:"ended_at"`
}

const whiteLabelPeriodColumns = `w.id, w.org_id, w.promoter_id, w.starts_on, w.ends_on, w.monthly_price_minor,
       w.currency, w.reason, w.created_by, w.created_at, w.ended_by, w.ended_at`

func scanWhiteLabelPeriod(row interface{ Scan(...any) error }, extra ...any) (WhiteLabelPeriodRow, error) {
	var r WhiteLabelPeriodRow
	dest := append([]any{&r.ID, &r.OrgID, &r.PromoterID, &r.StartsOn, &r.EndsOn, &r.MonthlyPriceMinor,
		&r.Currency, &r.Reason, &r.CreatedBy, &r.CreatedAt, &r.EndedBy, &r.EndedAt}, extra...)
	err := row.Scan(dest...)
	return r, err
}

const lockWhiteLabelBrand = `-- name: LockWhiteLabelBrand :exec
SELECT pg_advisory_xact_lock(hashtextextended('white_label:' || $1::text || ':' || COALESCE($2::text, ''), 0))`

// LockWhiteLabelBrand serializes the writes of one brand's periods for the
// rest of the transaction, so the overlap check and the insert cannot race.
func (q *Queries) LockWhiteLabelBrand(ctx context.Context, orgID uuid.UUID, promoterID *uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockWhiteLabelBrand, orgID, promoterID)
	return err
}

const countOverlappingWhiteLabelPeriods = `-- name: CountOverlappingWhiteLabelPeriods :one
SELECT count(*)
FROM   white_label_periods w
WHERE  w.org_id = $1
  AND  w.promoter_id IS NOT DISTINCT FROM $2
  AND  (w.ends_on IS NULL OR w.ends_on >= w.starts_on)
  AND  w.starts_on <= COALESCE($4::date, 'infinity'::date)
  AND  COALESCE(w.ends_on, 'infinity'::date) >= $3::date`

// CountOverlappingWhiteLabelPeriods counts the non-empty periods of a brand
// that share a day with [startsOn, endsOn] (nil endsOn: no end).
func (q *Queries) CountOverlappingWhiteLabelPeriods(ctx context.Context, orgID uuid.UUID, promoterID *uuid.UUID, startsOn time.Time, endsOn *time.Time) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countOverlappingWhiteLabelPeriods, orgID, promoterID, startsOn, endsOn).Scan(&n)
	return n, err
}

const insertWhiteLabelPeriod = `-- name: InsertWhiteLabelPeriod :one
INSERT INTO white_label_periods AS w (org_id, promoter_id, starts_on, ends_on, monthly_price_minor, currency, reason, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING ` + whiteLabelPeriodColumns

// InsertWhiteLabelPeriodParams are the columns of a new period.
type InsertWhiteLabelPeriodParams struct {
	OrgID             uuid.UUID
	PromoterID        *uuid.UUID
	StartsOn          time.Time
	EndsOn            *time.Time
	MonthlyPriceMinor *int64
	Currency          *string
	Reason            string
	CreatedBy         *uuid.UUID
}

// InsertWhiteLabelPeriod writes a new period.
func (q *Queries) InsertWhiteLabelPeriod(ctx context.Context, p InsertWhiteLabelPeriodParams) (WhiteLabelPeriodRow, error) {
	return scanWhiteLabelPeriod(q.db.QueryRow(ctx, insertWhiteLabelPeriod,
		p.OrgID, p.PromoterID, p.StartsOn, p.EndsOn, p.MonthlyPriceMinor, p.Currency, p.Reason, p.CreatedBy))
}

const getWhiteLabelPeriod = `-- name: GetWhiteLabelPeriod :one
SELECT ` + whiteLabelPeriodColumns + `
FROM   white_label_periods w
WHERE  w.id = $1`

// GetWhiteLabelPeriod returns one period, pgx.ErrNoRows when there is none.
func (q *Queries) GetWhiteLabelPeriod(ctx context.Context, id uuid.UUID) (WhiteLabelPeriodRow, error) {
	return scanWhiteLabelPeriod(q.db.QueryRow(ctx, getWhiteLabelPeriod, id))
}

const endWhiteLabelPeriod = `-- name: EndWhiteLabelPeriod :one
UPDATE white_label_periods AS w
SET    ends_on = $2, ended_by = $3, ended_at = now()
WHERE  w.id = $1
  AND  (w.ends_on IS NULL OR w.ends_on > $2::date)
RETURNING ` + whiteLabelPeriodColumns

// EndWhiteLabelPeriod moves the end of a period EARLIER (or sets one);
// pgx.ErrNoRows when the period is unknown or already ends on or before
// endsOn. A period's end is never pushed later.
func (q *Queries) EndWhiteLabelPeriod(ctx context.Context, id uuid.UUID, endsOn time.Time, endedBy *uuid.UUID) (WhiteLabelPeriodRow, error) {
	return scanWhiteLabelPeriod(q.db.QueryRow(ctx, endWhiteLabelPeriod, id, endsOn, endedBy))
}

// WhiteLabelPeriodListRow is a period with the names of its brand.
type WhiteLabelPeriodListRow struct {
	WhiteLabelPeriodRow
	OrgName      string  `json:"org_name"`
	PromoterName *string `json:"promoter_name"`
}

const listWhiteLabelPeriods = `-- name: ListWhiteLabelPeriods :many
SELECT ` + whiteLabelPeriodColumns + `, o.name, p.name
FROM   white_label_periods w
JOIN   organizations o ON o.id = w.org_id
LEFT   JOIN org_promoters p ON p.id = w.promoter_id
WHERE  ($1::uuid IS NULL OR w.org_id = $1)
ORDER  BY o.name, p.name NULLS FIRST, w.starts_on DESC, w.id
LIMIT  1000`

// ListWhiteLabelPeriods lists the periods of one organization (nil: all),
// newest first per brand.
func (q *Queries) ListWhiteLabelPeriods(ctx context.Context, orgID *uuid.UUID) ([]WhiteLabelPeriodListRow, error) {
	rows, err := q.db.Query(ctx, listWhiteLabelPeriods, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WhiteLabelPeriodListRow
	for rows.Next() {
		var r WhiteLabelPeriodListRow
		base, err := scanWhiteLabelPeriod(rows, &r.OrgName, &r.PromoterName)
		if err != nil {
			return nil, err
		}
		r.WhiteLabelPeriodRow = base
		out = append(out, r)
	}
	return out, rows.Err()
}

const listWhiteLabelPeriodsForMonth = `-- name: ListWhiteLabelPeriodsForMonth :many
SELECT ` + whiteLabelPeriodColumns + `
FROM   white_label_periods w
WHERE  w.starts_on <= $2::date
  AND  (w.ends_on IS NULL OR w.ends_on >= $1::date)
  AND  (w.ends_on IS NULL OR w.ends_on >= w.starts_on)
  AND  ($3::uuid IS NULL OR w.org_id = $3)
  AND  NOT EXISTS (SELECT 1 FROM organizations o
                   WHERE o.id = w.org_id AND o.deleted_at < ($1::date)::timestamp AT TIME ZONE 'UTC')
  AND  NOT EXISTS (SELECT 1 FROM org_promoters p
                   WHERE p.id = w.promoter_id AND p.archived_at < ($1::date)::timestamp AT TIME ZONE 'UTC')
ORDER  BY w.id`

// ListWhiteLabelPeriodsForMonth returns the non-empty periods that cover at
// least one day of [firstDay, lastDay]; orgID narrows to one organization
// (tests), nil means all. A brand archived before the month (organization
// deleted, promoter archived) is not charged for it: an open period of a
// brand that left would otherwise be charged forever. The month of the
// archive is still charged for its days.
func (q *Queries) ListWhiteLabelPeriodsForMonth(ctx context.Context, firstDay, lastDay time.Time, orgID *uuid.UUID) ([]WhiteLabelPeriodRow, error) {
	rows, err := q.db.Query(ctx, listWhiteLabelPeriodsForMonth, firstDay, lastDay, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WhiteLabelPeriodRow
	for rows.Next() {
		r, err := scanWhiteLabelPeriod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listWhiteLabelEvents = `-- name: ListWhiteLabelEvents :many
SELECT e.id
FROM   events e
LEFT   JOIN event_promoters ep ON ep.event_id = e.id
LEFT   JOIN org_promoters op ON op.id = ep.promoter_id AND op.archived_at IS NULL
WHERE  e.id = ANY($1::uuid[])
  AND  EXISTS (
    SELECT 1 FROM white_label_periods w
    WHERE  w.org_id = e.org_id
      AND  w.promoter_id IS NOT DISTINCT FROM op.id
      AND  w.starts_on <= $2::date
      AND  (w.ends_on IS NULL OR w.ends_on >= $2::date))`

// ListWhiteLabelEvents returns the events among eventIDs whose brand (the
// event's promoter, else its organization) has white label on day. An
// archived promoter is not a brand: its events fall back to the organization,
// as their public page addresses do.
func (q *Queries) ListWhiteLabelEvents(ctx context.Context, eventIDs []uuid.UUID, day time.Time) ([]uuid.UUID, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, listWhiteLabelEvents, eventIDs, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const eventIDForTicket = `-- name: EventIDForTicket :one
SELECT e.id
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events e   ON e.id = s.event_id
WHERE  t.id = $1`

// EventIDForTicket returns the event of a ticket (deleted sessions and
// events included), pgx.ErrNoRows when the ticket is unknown.
func (q *Queries) EventIDForTicket(ctx context.Context, ticketID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, eventIDForTicket, ticketID).Scan(&id)
	return id, err
}

const whiteLabelActiveForOrg = `-- name: WhiteLabelActiveForOrg :one
SELECT EXISTS (
    SELECT 1 FROM white_label_periods w
    WHERE  w.org_id = $1
      AND  w.promoter_id IS NULL
      AND  w.starts_on <= $2::date
      AND  (w.ends_on IS NULL OR w.ends_on >= $2::date))`

// WhiteLabelActiveForOrg reports whether the organization itself (the brand
// of its events without a promoter, and of its shop) has white label on day.
func (q *Queries) WhiteLabelActiveForOrg(ctx context.Context, orgID uuid.UUID, day time.Time) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, whiteLabelActiveForOrg, orgID, day).Scan(&ok)
	return ok, err
}
