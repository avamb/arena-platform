// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: sessions.sql
//
// The two queries here are the Bil24-compat GET_ALL_ACTIONS catalog pair
// (feature #497 W1-B3a, spec §7.1). They exist so the handler can build the
// whole nested response — every action, every actionEvent, every GA category
// — from a fixed number of round-trips instead of one query per session:
//
//	ListActionEventsByOrg     — one row per sellable session (+ availability)
//	ListActionEventTiersByOrg — one row per live tier of those sessions
//
// Together with ListEventsByOrg, ListActionVenuesByOrg and one batched
// priceresolve.ForTiers call that is five queries for the entire catalog.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// ListActionEventsByOrg — sessions + venue geo + availability (spec §7.1)
// ─────────────────────────────────────────────────────────────────────────────

// ActionEventRow is one sellable session projected for the GET_ALL_ACTIONS
// actionEventList block.
//
// Timezone is venues.timezone and MAY be nil: spec §7.1 requires such a
// session to be dropped from the response with a warn log, so the SQL projects
// the column instead of filtering on it and the handler owns the decision
// (VenueName is carried for the log line).
//
// Availability is expressed two ways because the platform has two inventory
// shapes. When SeatsTotal > 0 the session has a materialised session_seats
// pool (assigned seats and/or ga_units) and SeatsAvailable is the truth;
// otherwise the session sells against inventory_ledger and LedgerAvailable
// (capacity_total − sold − held, 0 when uncapped) is.
//
// SellEndAt is min(sale_window_end) over the session's live tiers, nil when no
// tier bounds its sale window — the handler then falls back to StartAt as the
// spec requires.
type ActionEventRow struct {
	SessionID          uuid.UUID  `json:"session_id"`
	EventID            uuid.UUID  `json:"event_id"`
	VenueID            uuid.UUID  `json:"venue_id"`
	CityID             *uuid.UUID `json:"city_id"`
	VenueName          string     `json:"venue_name"`
	Timezone           *string    `json:"timezone"`
	StartAt            time.Time  `json:"start_at"`
	Currency           string     `json:"currency"`
	AdmissionMode      string     `json:"admission_mode"`
	SeatingPlanName    *string    `json:"seating_plan_name"`
	PosterMediaID      *uuid.UUID `json:"poster_media_id"`
	EventPosterMediaID *uuid.UUID `json:"event_poster_media_id"`
	EventImageURL      *string    `json:"event_image_url"`
	SellEndAt          *time.Time `json:"sell_end_at"`
	SeatsTotal         int32      `json:"seats_total"`
	SeatsAvailable     int32      `json:"seats_available"`
	LedgerAvailable    int32      `json:"ledger_available"`
}

const listActionEventsByOrg = `-- name: ListActionEventsByOrg :many
SELECT s.id                                        AS session_id,
       s.event_id,
       s.venue_id,
       v.city_id,
       v.name                                      AS venue_name,
       v.timezone,
       s.start_at,
       trim(s.currency)::text                      AS currency,
       s.admission_mode,
       sp.name                                     AS seating_plan_name,
       s.poster_media_id,
       e.poster_media_id                           AS event_poster_media_id,
       e.image_url                                 AS event_image_url,
       (SELECT min(tt.sale_window_end)
          FROM   ticket_tiers tt
          WHERE  tt.session_id = s.id
            AND  tt.deleted_at IS NULL
            AND  tt.sale_window_end IS NOT NULL)   AS sell_end_at,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = s.id)::int        AS seats_total,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = s.id
            AND  ss.status = 'available')::int     AS seats_available,
       COALESCE((SELECT il.capacity_total - il.capacity_sold - il.capacity_held
                   FROM   inventory_ledger il
                   WHERE  il.session_id = s.id
                     AND  il.tier_id IS NULL), 0)::int AS ledger_available
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
JOIN   venues   v ON v.id = s.venue_id
LEFT JOIN seating_plan_versions spv ON spv.id = s.seating_plan_version_id
LEFT JOIN seating_plans         sp  ON sp.id  = spv.seating_plan_id
WHERE  e.org_id     = $1
  AND  e.status     = 'published'
  AND  e.deleted_at IS NULL
  AND  s.deleted_at IS NULL
  AND  s.status     = 'scheduled'
  AND  s.start_at   > now() - interval '6 hours'
ORDER BY s.start_at ASC, s.id ASC`

// ListActionEventsByOrg returns every sellable session of orgID's published
// events, ordered by start time, for the Bil24-compat GET_ALL_ACTIONS
// aggregation (spec §7.1).
//
// "Sellable" per the spec: the event is published and not deleted, the session
// is scheduled and not deleted, and it starts no earlier than six hours ago (a
// session that began within the last six hours is still sold at the door).
// events.visibility is deliberately NOT a filter — the calling site decides
// what to display.
func (q *Queries) ListActionEventsByOrg(ctx context.Context, orgID uuid.UUID) ([]ActionEventRow, error) {
	rows, err := q.db.Query(ctx, listActionEventsByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActionEventRow
	for rows.Next() {
		var r ActionEventRow
		if err := rows.Scan(
			&r.SessionID,
			&r.EventID,
			&r.VenueID,
			&r.CityID,
			&r.VenueName,
			&r.Timezone,
			&r.StartAt,
			&r.Currency,
			&r.AdmissionMode,
			&r.SeatingPlanName,
			&r.PosterMediaID,
			&r.EventPosterMediaID,
			&r.EventImageURL,
			&r.SellEndAt,
			&r.SeatsTotal,
			&r.SeatsAvailable,
			&r.LedgerAvailable,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListActionEventTiersByOrg — tiers of the same session set (spec §7.1)
// ─────────────────────────────────────────────────────────────────────────────

// ActionEventTierRow is one live ticket tier of a sellable session, carrying
// the full TicketTierRow so it can be fed straight to priceresolve.ForTiers,
// plus the GA classification and the tier's own GA-unit counts.
//
// IsGA marks a tier that sells a place without a seat (every tier of a
// general_admission session, plus the tiers stamped on ga_unit rows of a
// hybrid one). Only those tiers become categoryLimitList categories; seated
// tiers are still returned because minPrice / maxPrice span all of them.
//
// GAUnitsTotal separates "this tier owns no ga_unit rows at all" (the common
// shape: a GA pool materialised with tier_id NULL, or no unit rows at all)
// from "sold out". Only in the former case may the handler fall back to the
// session-level remaining count.
type ActionEventTierRow struct {
	Tier             TicketTierRow `json:"tier"`
	IsGA             bool          `json:"is_ga"`
	GAUnitsTotal     int32         `json:"ga_units_total"`
	GAUnitsAvailable int32         `json:"ga_units_available"`
}

const listActionEventTiersByOrg = `-- name: ListActionEventTiersByOrg :many
SELECT tt.id, tt.session_id, tt.name, tt.pricing_mode, tt.price_amount,
       tt.currency, tt.pwyw_min, tt.pwyw_max, tt.capacity,
       tt.sale_window_start, tt.sale_window_end, tt.sort_order,
       tt.created_at, tt.updated_at, tt.deleted_at,
       (s.admission_mode = 'general_admission'
        OR (s.admission_mode = 'hybrid'
            AND EXISTS (SELECT 1
                          FROM   session_seats ss3
                          WHERE  ss3.session_id = tt.session_id
                            AND  ss3.tier_id    = tt.id
                            AND  ss3.kind       = 'ga_unit'))) AS is_ga,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = tt.session_id
            AND  ss.tier_id    = tt.id
            AND  ss.kind       = 'ga_unit')::int      AS ga_units_total,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = tt.session_id
            AND  ss.tier_id    = tt.id
            AND  ss.kind       = 'ga_unit'
            AND  ss.status     = 'available')::int    AS ga_units_available
FROM   ticket_tiers tt
JOIN   sessions s ON s.id = tt.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  e.org_id     = $1
  AND  e.status     = 'published'
  AND  e.deleted_at IS NULL
  AND  s.deleted_at IS NULL
  AND  s.status     = 'scheduled'
  AND  s.start_at   > now() - interval '6 hours'
  AND  tt.deleted_at IS NULL
ORDER BY tt.session_id, tt.sort_order ASC, tt.id ASC`

// ListActionEventTiersByOrg returns every live tier of orgID's sellable
// sessions, GA-classified, for the GET_ALL_ACTIONS categoryLimitList block and
// the minPrice / maxPrice aggregates (spec §7.1).
//
// The caller filters on IsGA for categories: a pure-seating action must expose
// an EMPTY categoryLimitList — that emptiness is exactly how the WordPress
// plugin distinguishes pure seating from a combined event — while still
// advertising a "from" price computed over its seated tiers.
func (q *Queries) ListActionEventTiersByOrg(ctx context.Context, orgID uuid.UUID) ([]ActionEventTierRow, error) {
	rows, err := q.db.Query(ctx, listActionEventTiersByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActionEventTierRow
	for rows.Next() {
		var r ActionEventTierRow
		if err := rows.Scan(
			&r.Tier.ID,
			&r.Tier.SessionID,
			&r.Tier.Name,
			&r.Tier.PricingMode,
			&r.Tier.PriceAmount,
			&r.Tier.Currency,
			&r.Tier.PwywMin,
			&r.Tier.PwywMax,
			&r.Tier.Capacity,
			&r.Tier.SaleWindowStart,
			&r.Tier.SaleWindowEnd,
			&r.Tier.SortOrder,
			&r.Tier.CreatedAt,
			&r.Tier.UpdatedAt,
			&r.Tier.DeletedAt,
			&r.IsGA,
			&r.GAUnitsTotal,
			&r.GAUnitsAvailable,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
