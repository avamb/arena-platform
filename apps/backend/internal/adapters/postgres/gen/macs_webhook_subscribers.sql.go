// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: macs_webhook_subscribers.sql
//
// AB-50c — MACS webhook subscriber CRUD.
// These queries manage the per-org MACS webhook registration stored in
// webhook_subscribers (kind='macs', org_id=<org>).

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// CreateMACSSubscriber
// ─────────────────────────────────────────────────────────────────────────────

const createMACSSubscriberSQL = `-- name: CreateMACSSubscriber :one
INSERT INTO webhook_subscribers (
    site_url,
    callback_url,
    signing_secret,
    event_types,
    active,
    kind,
    org_id
) VALUES (
    '', $2, $3, '{}', TRUE, 'macs', $1
)
RETURNING id, site_url, callback_url, signing_secret, event_types, active, created_at, updated_at, kind, org_id`

// CreateMACSSubscriber inserts a new active MACS subscriber for an org.
// There can be at most one active MACS subscriber per org (enforced by
// uq_webhook_subscribers_macs_per_org). Callers should call
// DeactivateMACSSubscriberByOrg first if they want to replace an existing one.
func (q *Queries) CreateMACSSubscriber(
	ctx context.Context,
	orgID uuid.UUID,
	callbackURL string,
	signingSecret string,
) (WebhookSubscriberRow, error) {
	row := q.db.QueryRow(ctx, createMACSSubscriberSQL, orgID, callbackURL, signingSecret)
	return scanWebhookSubscriberRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetMACSSubscriberByOrg
// ─────────────────────────────────────────────────────────────────────────────

const getMACSSubscriberByOrgSQL = `-- name: GetMACSSubscriberByOrg :one
SELECT id, site_url, callback_url, signing_secret, event_types, active, created_at, updated_at, kind, org_id
FROM   webhook_subscribers
WHERE  org_id = $1
  AND  kind   = 'macs'
  AND  active = TRUE`

// GetMACSSubscriberByOrg returns the active MACS subscriber for an org.
// Returns pgx.ErrNoRows when no active MACS subscriber is registered for the org.
func (q *Queries) GetMACSSubscriberByOrg(ctx context.Context, orgID uuid.UUID) (WebhookSubscriberRow, error) {
	row := q.db.QueryRow(ctx, getMACSSubscriberByOrgSQL, orgID)
	return scanWebhookSubscriberRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListActiveMACSSubscribers
// ─────────────────────────────────────────────────────────────────────────────

const listActiveMACSSubscribersSQL = `-- name: ListActiveMACSSubscribers :many
SELECT id, site_url, callback_url, signing_secret, event_types, active, created_at, updated_at, kind, org_id
FROM   webhook_subscribers
WHERE  kind   = 'macs'
  AND  active = TRUE
ORDER  BY created_at ASC`

// ListActiveMACSSubscribers returns all active MACS subscribers across all orgs.
// Used by the dispatcher to enumerate MACS delivery targets.
func (q *Queries) ListActiveMACSSubscribers(ctx context.Context) ([]WebhookSubscriberRow, error) {
	rows, err := q.db.Query(ctx, listActiveMACSSubscribersSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []WebhookSubscriberRow
	for rows.Next() {
		r, err := scanWebhookSubscriberRow(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// DeactivateMACSSubscriberByOrg
// ─────────────────────────────────────────────────────────────────────────────

const deactivateMACSSubscriberByOrgSQL = `-- name: DeactivateMACSSubscriberByOrg :one
UPDATE webhook_subscribers
SET    active     = FALSE,
       updated_at = NOW()
WHERE  org_id = $1
  AND  kind   = 'macs'
  AND  active = TRUE
RETURNING id, site_url, callback_url, signing_secret, event_types, active, created_at, updated_at, kind, org_id`

// DeactivateMACSSubscriberByOrg soft-deletes the active MACS subscriber for an org.
// Returns pgx.ErrNoRows when no active subscriber exists for the org.
func (q *Queries) DeactivateMACSSubscriberByOrg(ctx context.Context, orgID uuid.UUID) (WebhookSubscriberRow, error) {
	row := q.db.QueryRow(ctx, deactivateMACSSubscriberByOrgSQL, orgID)
	return scanWebhookSubscriberRow(row)
}
