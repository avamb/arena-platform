// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: guest_list.sql (migration 0187, owner decision 2026-11-02)

package gen

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Guest list rows
// ─────────────────────────────────────────────────────────────────────────────

// guestListFrom is the active tickets of session $1 whose event belongs to
// organization $2. A guest list never shows a cancelled, revoked or
// transferred ticket: it is the list of people who may come in.
const guestListFrom = `
FROM      tickets t
JOIN      sessions s ON s.id = t.session_id
JOIN      events   e ON e.id = s.event_id AND e.org_id = $2
LEFT JOIN orders   o ON o.id = t.order_id
WHERE  t.session_id = $1
  AND  t.status = 'active'
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL`

const countGuestListRows = `-- name: CountGuestListRows :one
SELECT count(*)` + guestListFrom

// CountGuestListRows counts the rows ListGuestListRows would return.
func (q *Queries) CountGuestListRows(ctx context.Context, sessionID, orgID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countGuestListRows, sessionID, orgID).Scan(&n)
	return n, err
}

const listGuestListRows = `-- name: ListGuestListRows :many
SELECT t.system_ticket_id,
       COALESCE(NULLIF(btrim(t.holder_name), ''), NULLIF(btrim(o.buyer_name), ''), '') AS guest_name,
       tt.name AS tier_name,
       t.seat_sector, t.seat_row, t.seat_number,
       tc.payload AS barcode,
       t.vip,
       COALESCE(t.used_at,
                (SELECT min(se.scanned_at) FROM scan_events se
                 WHERE se.ticket_id = t.id AND se.result = 'admitted' AND se.org_id = $2),
                (SELECT max(b.scanned_at) FROM barcodes b WHERE b.ticket_id = t.id)) AS entered_at,
       o.system_id AS order_number,
       COALESCE(NULLIF(btrim(o.buyer_email), ''), NULLIF(btrim(t.holder_email), '')) AS email,
       NULLIF(btrim(o.buyer_phone), '') AS phone,
       (t.complimentary_issuance_id IS NOT NULL OR COALESCE(o.source = 'complimentary', false)) AS invitation,
       COALESCE(o.is_test, false) AS is_test
FROM      tickets t
JOIN      sessions s ON s.id = t.session_id
JOIN      events   e ON e.id = s.event_id AND e.org_id = $2
LEFT JOIN orders   o ON o.id = t.order_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN LATERAL (
    SELECT c.payload FROM ticket_credentials c
    WHERE  c.ticket_id = t.id AND c.type = 'ean13'
    ORDER  BY c.issued_at DESC
    LIMIT  1
) tc ON true
WHERE  t.session_id = $1
  AND  t.status = 'active'
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL
ORDER  BY lower(COALESCE(NULLIF(btrim(t.holder_name), ''), NULLIF(btrim(o.buyer_name), ''), '')) = '',
          lower(COALESCE(NULLIF(btrim(t.holder_name), ''), NULLIF(btrim(o.buyer_name), ''), '')),
          t.system_ticket_id
LIMIT  $3`

// GuestListRow is one active ticket of a session as the guest list shows it.
// The guest is the name on the ticket (an invitation's guest) or the buyer's;
// order-side fields are nil for an invitation without an order.
type GuestListRow struct {
	SystemTicketID int64      `json:"system_ticket_id"`
	GuestName      string     `json:"guest_name"`
	TierName       *string    `json:"tier_name"`
	SeatSector     *string    `json:"seat_sector"`
	SeatRow        *string    `json:"seat_row"`
	SeatNumber     *string    `json:"seat_number"`
	Barcode        *string    `json:"barcode"`
	VIP            bool       `json:"vip"`
	EnteredAt      *time.Time `json:"entered_at"`
	OrderNumber    *int64     `json:"order_number"`
	Email          *string    `json:"email"`
	Phone          *string    `json:"phone"`
	Invitation     bool       `json:"invitation"`
	IsTest         bool       `json:"is_test"`
}

// ListGuestListRows returns the session's active tickets sorted by guest name
// (nameless rows last), then by ticket number; at most limit rows.
func (q *Queries) ListGuestListRows(ctx context.Context, sessionID, orgID uuid.UUID, limit int32) ([]GuestListRow, error) {
	rows, err := q.db.Query(ctx, listGuestListRows, sessionID, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []GuestListRow
	for rows.Next() {
		var i GuestListRow
		if err := rows.Scan(
			&i.SystemTicketID, &i.GuestName, &i.TierName,
			&i.SeatSector, &i.SeatRow, &i.SeatNumber,
			&i.Barcode, &i.VIP, &i.EnteredAt,
			&i.OrderNumber, &i.Email, &i.Phone, &i.Invitation, &i.IsTest,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Who may receive a list
// ─────────────────────────────────────────────────────────────────────────────

// guestListReceiverSQL is the ONE rule of who may receive the automatic list
// of a session: a live account (not deactivated, not anonymized) that still
// has the setting on and is an owner or a manager (membership roles org_admin
// and organizer, active) of an organization that is not archived. The worker
// writes a delivery only for such a person, and both claims ask again at send
// time, so a role, an account or an organization lost after the row was
// written stops the send. org, user are the SQL expressions of the ids.
func guestListReceiverSQL(org, user string) string {
	return `EXISTS (SELECT 1 FROM users ru
                WHERE ru.id = ` + user + ` AND ru.deactivated_at IS NULL AND ru.anonymized_at IS NULL)
  AND  EXISTS (SELECT 1 FROM organizations ro WHERE ro.id = ` + org + ` AND ro.deleted_at IS NULL)
  AND  EXISTS (SELECT 1 FROM guest_list_subscriptions rs
                WHERE rs.org_id = ` + org + ` AND rs.user_id = ` + user + `)
  AND  EXISTS (SELECT 1 FROM memberships rm
                WHERE rm.org_id = ` + org + ` AND rm.user_id = ` + user + `
                  AND rm.status = 'active' AND rm.role IN ('org_admin', 'organizer'))`
}

// guestListSessionLiveSQL: the session is live, not cancelled, and its event
// is a published event of that organization.
func guestListSessionLiveSQL(org, session string) string {
	return `EXISTS (SELECT 1 FROM sessions ls JOIN events le ON le.id = ls.event_id
                WHERE ls.id = ` + session + ` AND ls.deleted_at IS NULL AND ls.status <> 'cancelled'
                  AND le.org_id = ` + org + ` AND le.deleted_at IS NULL AND le.status = 'published')`
}

// guestListSlotDueSQL is the moment of a slot of session s.
const guestListSlotDueSQL = `CASE %s
       WHEN 'h24' THEN s.start_at - interval '24 hours'
       WHEN 'h3'  THEN s.start_at - interval '3 hours'
       ELSE COALESCE(s.doors_open_at, s.start_at) END`

// ─────────────────────────────────────────────────────────────────────────────
// Subscriptions
// ─────────────────────────────────────────────────────────────────────────────

const getGuestListSubscription = `-- name: GetGuestListSubscription :one
SELECT EXISTS (SELECT 1 FROM guest_list_subscriptions WHERE org_id = $1 AND user_id = $2),
       EXISTS (SELECT 1 FROM bot_telegram_links WHERE user_id = $2 AND revoked_at IS NULL),
       COALESCE((SELECT btrim(email) <> '' FROM users WHERE id = $2), false),
       EXISTS (SELECT 1 FROM memberships
               WHERE org_id = $1 AND user_id = $2 AND status = 'active'
                 AND role IN ('org_admin', 'organizer'))`

// GuestListSubscriptionRow says whether a member receives the guest lists
// automatically, how they would reach them and whether their role may get
// them at all (owners and managers only).
type GuestListSubscriptionRow struct {
	Enabled        bool `json:"enabled"`
	TelegramLinked bool `json:"telegram_linked"`
	HasEmail       bool `json:"has_email"`
	Eligible       bool `json:"eligible"`
}

// GetGuestListSubscription reads one member's guest list subscription.
func (q *Queries) GetGuestListSubscription(ctx context.Context, orgID, userID uuid.UUID) (GuestListSubscriptionRow, error) {
	var i GuestListSubscriptionRow
	err := q.db.QueryRow(ctx, getGuestListSubscription, orgID, userID).Scan(&i.Enabled, &i.TelegramLinked, &i.HasEmail, &i.Eligible)
	return i, err
}

const insertGuestListSubscription = `-- name: InsertGuestListSubscription :exec
INSERT INTO guest_list_subscriptions (org_id, user_id) VALUES ($1, $2)
ON CONFLICT (org_id, user_id) DO NOTHING`

// InsertGuestListSubscription turns the automatic guest list on; a repeat
// changes nothing.
func (q *Queries) InsertGuestListSubscription(ctx context.Context, orgID, userID uuid.UUID) error {
	_, err := q.db.Exec(ctx, insertGuestListSubscription, orgID, userID)
	return err
}

const deleteGuestListSubscription = `-- name: DeleteGuestListSubscription :exec
DELETE FROM guest_list_subscriptions WHERE org_id = $1 AND user_id = $2`

// DeleteGuestListSubscription turns the automatic guest list off. A list
// already written but not yet sent is not sent any more: the claims ask for
// the setting again.
func (q *Queries) DeleteGuestListSubscription(ctx context.Context, orgID, userID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteGuestListSubscription, orgID, userID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Deliveries
// ─────────────────────────────────────────────────────────────────────────────

// sqlf puts one SQL expression in the place of the %s of a fragment.
func sqlf(fragment, expr string) string { return strings.Replace(fragment, "%s", expr, 1) }

// GuestListGrace is how late a moment may still be acted on: a worker that
// was down longer sends nothing for that moment, the next one comes.
const GuestListGrace = 30 * time.Minute

// guestListLiveLink: the person has a live link to the event center bot.
const guestListLiveLink = `EXISTS (SELECT 1 FROM bot_telegram_links bl
                         WHERE bl.user_id = %s AND bl.revoked_at IS NULL)`

var scheduleGuestListDeliveries = `-- name: ScheduleGuestListDeliveries :execrows
INSERT INTO guest_list_deliveries (org_id, session_id, user_id, slot, channel, due_at)
SELECT gs.org_id, s.id, gs.user_id, sl.slot,
       CASE WHEN ` + sqlf(guestListLiveLink, "gs.user_id") + ` THEN 'telegram' ELSE 'email' END,
       sl.due
FROM   guest_list_subscriptions gs
JOIN   users    u ON u.id = gs.user_id
JOIN   events   e ON e.org_id = gs.org_id AND e.deleted_at IS NULL AND e.status = 'published'
JOIN   sessions s ON s.event_id = e.id AND s.deleted_at IS NULL AND s.status <> 'cancelled'
               AND s.start_at >= $1::timestamptz - $2::interval
               AND s.start_at <= $1::timestamptz + interval '24 hours' + $2::interval
CROSS  JOIN LATERAL (VALUES
           ('h24',   s.start_at - interval '24 hours'),
           ('h3',    s.start_at - interval '3 hours'),
           ('doors', COALESCE(s.doors_open_at, s.start_at))) AS sl(slot, due)
WHERE  sl.due <= $1::timestamptz
  AND  sl.due >  $1::timestamptz - $2::interval
  AND  sl.due >= gs.created_at - $2::interval
  -- Doors at the very moment of the 3-hour or 24-hour list: that list is the
  -- same file, so the doors one is not sent a second time.
  AND  NOT (sl.slot = 'doors' AND sl.due IN (s.start_at - interval '3 hours', s.start_at - interval '24 hours'))
  AND  ` + guestListReceiverSQL("gs.org_id", "gs.user_id") + `
  AND  EXISTS (SELECT 1 FROM tickets t WHERE t.session_id = s.id AND t.status = 'active')
  AND  (btrim(u.email) <> '' OR ` + sqlf(guestListLiveLink, "gs.user_id") + `)
ON CONFLICT ON CONSTRAINT guest_list_deliveries_once DO UPDATE
SET    due_at     = EXCLUDED.due_at,
       channel    = EXCLUDED.channel,
       attempts   = 0,
       claimed_at = NULL,
       last_error = NULL
-- A session moved after its row was written gets the row back for its new
-- moment, as long as nothing was sent for that slot.
WHERE  guest_list_deliveries.sent_at IS NULL
  AND  guest_list_deliveries.due_at <> EXCLUDED.due_at`

// ScheduleGuestListDeliveries writes one delivery for every subscriber of
// every session whose moment (24 h, 3 h before the start, doors open) came
// within grace before now. The unique key keeps a second pass from writing a
// moment again, so a list is never sent twice; a session moved before its
// list went out gets the row back for its new moment. Only receivers of
// guestListReceiverSQL, a published event, a live session and at least one
// active ticket get a row.
func (q *Queries) ScheduleGuestListDeliveries(ctx context.Context, now time.Time, grace time.Duration) (int64, error) {
	tag, err := q.db.Exec(ctx, scheduleGuestListDeliveries, now, grace)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GuestListDelivery is one automatic guest list to send. Attempt is the claim
// token: the finish must name it, so a late report of an older claim cannot
// close or reopen a newer one.
type GuestListDelivery struct {
	ID              uuid.UUID `json:"id"`
	OrgID           uuid.UUID `json:"org_id"`
	SessionID       uuid.UUID `json:"session_id"`
	UserID          uuid.UUID `json:"user_id"`
	Slot            string    `json:"slot"`
	Attempt         int32     `json:"attempt"`
	TelegramUserID  int64     `json:"telegram_user_id"`
	Email           string    `json:"email"`
	Locale          string    `json:"locale"`
	EventName       string    `json:"event_name"`
	SessionStartAt  time.Time `json:"session_start_at"`
	SessionTimezone string    `json:"session_timezone"`
}

// guestListClaimable: not sent, not given up, not taken in the last 10
// minutes, not so late the list is useless (2 hours after its moment), still
// at the moment its session has now (a session moved later waits for the new
// moment), and the person and the session still qualify.
var guestListClaimable = `d.sent_at IS NULL
      AND  d.attempts < 3
      AND  (d.claimed_at IS NULL OR d.claimed_at < now() - interval '10 minutes')
      AND  d.due_at > now() - interval '2 hours'
      AND  d.due_at = (SELECT ` + sqlf(guestListSlotDueSQL, "d.slot") + ` FROM sessions s WHERE s.id = d.session_id)
      AND  ` + guestListReceiverSQL("d.org_id", "d.user_id") + `
      AND  ` + guestListSessionLiveSQL("d.org_id", "d.session_id")

const guestListClaimTail = `
), marked AS (
    UPDATE guest_list_deliveries d
    SET    claimed_at = now(), attempts = d.attempts + 1
    FROM   picked p
    WHERE  d.id = p.id
    RETURNING d.id, d.attempts
)
SELECT p.id, p.org_id, p.session_id, p.user_id, p.slot, m.attempts, p.tg, p.email, p.locale,
       e.name, s.start_at, COALESCE(v.timezone, 'UTC')
FROM   picked p
JOIN   marked m ON m.id = p.id
JOIN   sessions s ON s.id = p.session_id
JOIN   events   e ON e.id = s.event_id
LEFT   JOIN venues v ON v.id = s.venue_id
ORDER  BY p.due_at, p.id`

var claimGuestListTelegramDeliveries = `-- name: ClaimGuestListTelegramDeliveries :many
WITH picked AS (
    SELECT d.id, d.org_id, d.session_id, d.user_id, d.slot, d.due_at,
           l.telegram_user_id AS tg, ''::text AS email, l.locale
    FROM   guest_list_deliveries d
    CROSS  JOIN LATERAL (
               SELECT bl.telegram_user_id, bl.locale
               FROM   bot_telegram_links bl
               WHERE  bl.user_id = d.user_id AND bl.revoked_at IS NULL
               ORDER  BY bl.updated_at DESC
               LIMIT  1) l
    WHERE  d.channel = 'telegram'
      AND  ` + guestListClaimable + `
    ORDER  BY d.due_at, d.id
    LIMIT  $1
    FOR UPDATE OF d SKIP LOCKED` + guestListClaimTail

// ClaimGuestListTelegramDeliveries hands the bot up to limit guest lists to
// send to private chats, taking each for 10 minutes. A row the bot does not
// report back on may be taken again after that, three times at most.
func (q *Queries) ClaimGuestListTelegramDeliveries(ctx context.Context, limit int32) ([]GuestListDelivery, error) {
	return q.claimGuestList(ctx, claimGuestListTelegramDeliveries, limit)
}

// The e-mail claim also takes a 'telegram' row whose person is no longer
// linked to the bot: e-mail is the fallback for people without Telegram.
var claimGuestListEmailDeliveries = `-- name: ClaimGuestListEmailDeliveries :many
WITH picked AS (
    SELECT d.id, d.org_id, d.session_id, d.user_id, d.slot, d.due_at,
           0::bigint AS tg, btrim(u.email) AS email, COALESCE(u.preferred_locale, 'en') AS locale
    FROM   guest_list_deliveries d
    JOIN   users u ON u.id = d.user_id
    WHERE  (d.channel = 'email' OR NOT ` + sqlf(guestListLiveLink, "d.user_id") + `)
      AND  btrim(u.email) <> ''
      AND  ` + guestListClaimable + `
    ORDER  BY d.due_at, d.id
    LIMIT  $1
    FOR UPDATE OF d SKIP LOCKED` + guestListClaimTail

// ClaimGuestListEmailDeliveries hands the worker up to limit guest lists to
// mail, with the same lease as the bot's.
func (q *Queries) ClaimGuestListEmailDeliveries(ctx context.Context, limit int32) ([]GuestListDelivery, error) {
	return q.claimGuestList(ctx, claimGuestListEmailDeliveries, limit)
}

func (q *Queries) claimGuestList(ctx context.Context, sql string, limit int32) ([]GuestListDelivery, error) {
	rows, err := q.db.Query(ctx, sql, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GuestListDelivery
	for rows.Next() {
		var d GuestListDelivery
		if err := rows.Scan(&d.ID, &d.OrgID, &d.SessionID, &d.UserID, &d.Slot, &d.Attempt, &d.TelegramUserID,
			&d.Email, &d.Locale, &d.EventName, &d.SessionStartAt, &d.SessionTimezone); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// A failed attempt keeps its claim time, so the row is taken again only when
// the 10-minute lease is over. A send the provider may have delivered after
// all (a timeout) is therefore not repeated at once. 'email' moves the row to
// the e-mail channel (the person blocked the bot) and frees it.
const finishGuestListDelivery = `-- name: FinishGuestListDelivery :execrows
UPDATE guest_list_deliveries
SET    sent_at    = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END,
       last_error = CASE WHEN $2 = 'sent' THEN NULL ELSE $3 END,
       claimed_at = CASE WHEN $2 = 'email' THEN NULL ELSE claimed_at END,
       channel    = CASE WHEN $2 = 'email' THEN 'email' ELSE channel END,
       attempts   = CASE WHEN $2 = 'skipped' THEN 99 ELSE attempts END
WHERE  id = $1 AND sent_at IS NULL AND attempts = $4`

// Outcomes of FinishGuestListDelivery.
const (
	// GuestListSent: the list reached the person.
	GuestListSent = "sent"
	// GuestListFailed: a passing failure; the row may be taken again when its
	// lease is over.
	GuestListFailed = "failed"
	// GuestListSkipped: it will never work (no rights any more, nothing to
	// send); the row is closed without a send.
	GuestListSkipped = "skipped"
	// GuestListToEmail: Telegram refuses this person for good (they blocked
	// the bot); the row goes to the e-mail channel instead.
	GuestListToEmail = "email"
)

// FinishGuestListDelivery records how a taken delivery ended. attempt is the
// claim token GuestListDelivery.Attempt: a report of an older claim changes
// nothing and answers 0 rows. reason is a short code, never data from the
// file. A row already sent is not touched.
func (q *Queries) FinishGuestListDelivery(ctx context.Context, id uuid.UUID, attempt int32, outcome string, reason *string) (int64, error) {
	tag, err := q.db.Exec(ctx, finishGuestListDelivery, id, outcome, reason, attempt)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const purgeGuestListDeliveries = `-- name: PurgeGuestListDeliveries :execrows
DELETE FROM guest_list_deliveries
WHERE  id IN (SELECT id FROM guest_list_deliveries
              WHERE due_at < $1::timestamptz - interval '90 days'
              ORDER BY due_at LIMIT 5000)`

// PurgeGuestListDeliveries deletes delivery rows whose moment is more than 90
// days before now, 5000 at a time (worker_jobs.cleanup runs it hourly). The
// rows only keep a list from going twice, which a past session cannot need.
func (q *Queries) PurgeGuestListDeliveries(ctx context.Context, now time.Time) (int64, error) {
	tag, err := q.db.Exec(ctx, purgeGuestListDeliveries, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
