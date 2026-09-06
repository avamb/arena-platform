// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: webhook_subscribers.sql
//
// W1-B7c (feature #506, spec §9.2) — bil24_wp subscriber ROUTING.
//
// The MACS wrapper (macs_webhook_subscribers.sql.go) reads the same table by
// org; these two read it by SALES CHANNEL, which is how the WordPress sites
// subscribe (migration 0094). They select a narrow projection rather than
// WebhookSubscriberRow because that row type predates channel_id and widening
// it would touch every MACS query that shares scanWebhookSubscriberRow.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// WPSubscriberRow is the routing projection of one active bil24_wp
// subscriber: who to POST to, and with which secret to sign.
type WPSubscriberRow struct {
	ID            uuid.UUID `json:"id"`
	ChannelID     uuid.UUID `json:"channel_id"`
	CallbackURL   string    `json:"callback_url"`
	SigningSecret string    `json:"signing_secret"`
}

// ─────────────────────────────────────────────────────────────────────────────
// GetWPSubscriberByChannel
// ─────────────────────────────────────────────────────────────────────────────

const getWPSubscriberByChannelSQL = `-- name: GetWPSubscriberByChannel :one
SELECT id, channel_id, callback_url, signing_secret
FROM   webhook_subscribers
WHERE  channel_id = $1
  AND  kind       = 'bil24_wp'
  AND  active     = TRUE`

// GetWPSubscriberByChannel returns the active bil24_wp subscriber of one sales
// channel. Returns pgx.ErrNoRows when the channel has no registered WordPress
// site — the common case for every channel that is not a migrated WP shop.
func (q *Queries) GetWPSubscriberByChannel(ctx context.Context, channelID uuid.UUID) (WPSubscriberRow, error) {
	row := q.db.QueryRow(ctx, getWPSubscriberByChannelSQL, channelID)
	var r WPSubscriberRow
	err := row.Scan(&r.ID, &r.ChannelID, &r.CallbackURL, &r.SigningSecret)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// ListWPSubscribersForEvent
// ─────────────────────────────────────────────────────────────────────────────

const listWPSubscribersForEventSQL = `-- name: ListWPSubscribersForEvent :many
SELECT DISTINCT ws.id, ws.channel_id, ws.callback_url, ws.signing_secret
FROM   webhook_subscribers ws
JOIN   agent_feed_tokens   aft ON aft.sales_channel_id = ws.channel_id
JOIN   event_publications  ep  ON ep.feed_token_id     = aft.id
WHERE  ep.event_id  = $1
  AND  ws.kind      = 'bil24_wp'
  AND  ws.active    = TRUE
  AND  aft.is_active = TRUE
  AND  aft.revoked_at IS NULL
ORDER  BY ws.id`

// ListWPSubscribersForEvent returns every active bil24_wp subscriber whose
// sales channel carries a publication of the event. Catalog events fan out to
// all of them; an empty slice means no WordPress site shows this event.
func (q *Queries) ListWPSubscribersForEvent(ctx context.Context, eventID uuid.UUID) ([]WPSubscriberRow, error) {
	rows, err := q.db.Query(ctx, listWPSubscribersForEventSQL, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []WPSubscriberRow
	for rows.Next() {
		var r WPSubscriberRow
		if err := rows.Scan(&r.ID, &r.ChannelID, &r.CallbackURL, &r.SigningSecret); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin CRUD (feature #507, W1-B7d, spec §9.2)
//
// The routing projection above (WPSubscriberRow) predates the admin surface
// and is kept narrow on purpose; these queries serve the
// PUT/GET/DELETE .../wp-webhook endpoints and need the full admin-facing
// column set (active, created_at, updated_at) that routing never reads.
// ─────────────────────────────────────────────────────────────────────────────

// WPWebhookAdminRow is the admin-facing projection of one webhook_subscribers
// row of kind='bil24_wp': everything an operator needs to see or hand back
// after a PUT, short of anything routing-only.
type WPWebhookAdminRow struct {
	ID            uuid.UUID `json:"id"`
	ChannelID     uuid.UUID `json:"channel_id"`
	CallbackURL   string    `json:"callback_url"`
	SigningSecret string    `json:"signing_secret"`
	Active        bool      `json:"active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func scanWPWebhookAdminRow(row interface {
	Scan(dest ...any) error
}) (WPWebhookAdminRow, error) {
	var r WPWebhookAdminRow
	err := row.Scan(&r.ID, &r.ChannelID, &r.CallbackURL, &r.SigningSecret, &r.Active, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// CreateWPWebhookSubscriber
// ─────────────────────────────────────────────────────────────────────────────

const createWPWebhookSubscriberSQL = `-- name: CreateWPWebhookSubscriber :one
INSERT INTO webhook_subscribers (
    site_url,
    callback_url,
    signing_secret,
    event_types,
    active,
    kind,
    channel_id
) VALUES (
    '', $2, $3, '{}', TRUE, 'bil24_wp', $1
)
RETURNING id, channel_id, callback_url, signing_secret, active, created_at, updated_at`

// CreateWPWebhookSubscriber inserts a new active bil24_wp subscriber for a
// sales channel. Callers should call DeactivateWPSubscriberByChannel first —
// at most one ACTIVE bil24_wp subscriber per channel is enforced by
// uq_webhook_subscribers_bil24_wp_per_channel (migration 0094), so skipping
// the deactivation step surfaces as a unique-violation instead of a silent
// second-active row.
func (q *Queries) CreateWPWebhookSubscriber(
	ctx context.Context,
	channelID uuid.UUID,
	callbackURL string,
	signingSecret string,
) (WPWebhookAdminRow, error) {
	row := q.db.QueryRow(ctx, createWPWebhookSubscriberSQL, channelID, callbackURL, signingSecret)
	return scanWPWebhookAdminRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetActiveWPSubscriberByChannel
// ─────────────────────────────────────────────────────────────────────────────

const getActiveWPSubscriberByChannelSQL = `-- name: GetActiveWPSubscriberByChannel :one
SELECT id, channel_id, callback_url, signing_secret, active, created_at, updated_at
FROM   webhook_subscribers
WHERE  channel_id = $1
  AND  kind       = 'bil24_wp'
  AND  active     = TRUE`

// GetActiveWPSubscriberByChannel is the admin-surface GET query: the full row
// (minus site_url/event_types, which the admin surface never shows) for the
// active bil24_wp subscriber of a channel. Returns pgx.ErrNoRows when the
// channel has none registered.
func (q *Queries) GetActiveWPSubscriberByChannel(ctx context.Context, channelID uuid.UUID) (WPWebhookAdminRow, error) {
	row := q.db.QueryRow(ctx, getActiveWPSubscriberByChannelSQL, channelID)
	return scanWPWebhookAdminRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// DeactivateWPSubscriberByChannel
// ─────────────────────────────────────────────────────────────────────────────

const deactivateWPSubscriberByChannelSQL = `-- name: DeactivateWPSubscriberByChannel :one
UPDATE webhook_subscribers
SET    active     = FALSE,
       updated_at = NOW()
WHERE  channel_id = $1
  AND  kind       = 'bil24_wp'
  AND  active     = TRUE
RETURNING id, channel_id, callback_url, signing_secret, active, created_at, updated_at`

// DeactivateWPSubscriberByChannel soft-deletes the active bil24_wp subscriber
// of a channel (re-registration or DELETE). Returns pgx.ErrNoRows when the
// channel has no active subscriber — the normal case on first registration,
// and the caller (PUT) ignores that error, while DELETE surfaces it as 404.
func (q *Queries) DeactivateWPSubscriberByChannel(ctx context.Context, channelID uuid.UUID) (WPWebhookAdminRow, error) {
	row := q.db.QueryRow(ctx, deactivateWPSubscriberByChannelSQL, channelID)
	return scanWPWebhookAdminRow(row)
}
