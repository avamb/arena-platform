// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: gateway_sessions.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// W1-A4a (feature #479): gateway_sessions queries — the persistent Bil24
// sessionId cookie that a WordPress site keeps for a customer within one
// (org, channel). Spec sections §3.2 / §5 / §6.
// ─────────────────────────────────────────────────────────────────────────────

// GatewaySessionRow mirrors the gateway_sessions table. session_token is
// the 43-char base64url representation of 32 crypto/rand bytes and is
// what Bil24 clients see on the wire as sessionId. promo_codes accumulates
// as ADD_PROMO_CODES is called; expires_at is anchored at creation time
// unless the gateway explicitly extends it (see ExtendGatewaySessionExpiry).
type GatewaySessionRow struct {
	ID           uuid.UUID `json:"id"`
	SessionToken string    `json:"session_token"`
	CustomerID   uuid.UUID `json:"customer_id"`
	OrgID        uuid.UUID `json:"org_id"`
	ChannelID    uuid.UUID `json:"channel_id"`
	Locale       string    `json:"locale"`
	PromoCodes   []string  `json:"promo_codes"`
	CreatedAt    time.Time `json:"created_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func scanGatewaySessionRow(row interface {
	Scan(dest ...any) error
}) (GatewaySessionRow, error) {
	var r GatewaySessionRow
	err := row.Scan(
		&r.ID,
		&r.SessionToken,
		&r.CustomerID,
		&r.OrgID,
		&r.ChannelID,
		&r.Locale,
		&r.PromoCodes,
		&r.CreatedAt,
		&r.LastSeenAt,
		&r.ExpiresAt,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertGatewaySession
// ─────────────────────────────────────────────────────────────────────────────

const insertGatewaySession = `-- name: InsertGatewaySession :one
INSERT INTO gateway_sessions
    (session_token, customer_id, org_id, channel_id, locale, promo_codes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, session_token, customer_id, org_id, channel_id, locale,
          promo_codes, created_at, last_seen_at, expires_at`

// InsertGatewaySession persists a freshly-minted session. session_token is
// pre-computed by the caller (32 bytes crypto/rand → base64url). Uniqueness
// on session_token is enforced by the column's UNIQUE constraint — a
// 23505 conflict means the token was already handed out and callers should
// mint a new one.
func (q *Queries) InsertGatewaySession(
	ctx context.Context,
	sessionToken string,
	customerID uuid.UUID,
	orgID uuid.UUID,
	channelID uuid.UUID,
	locale string,
	promoCodes []string,
	expiresAt time.Time,
) (GatewaySessionRow, error) {
	row := q.db.QueryRow(ctx, insertGatewaySession,
		sessionToken, customerID, orgID, channelID, locale, promoCodes, expiresAt)
	return scanGatewaySessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetGatewaySessionByToken
// ─────────────────────────────────────────────────────────────────────────────

const getGatewaySessionByToken = `-- name: GetGatewaySessionByToken :one
SELECT id, session_token, customer_id, org_id, channel_id, locale,
       promo_codes, created_at, last_seen_at, expires_at
FROM   gateway_sessions
WHERE  session_token = $1`

// GetGatewaySessionByToken resolves a wire sessionId back to its row.
// Returns pgx.ErrNoRows when the token is unknown; the gateway maps that
// (plus expiry) to resultCode=1 per spec §6.
func (q *Queries) GetGatewaySessionByToken(ctx context.Context, sessionToken string) (GatewaySessionRow, error) {
	row := q.db.QueryRow(ctx, getGatewaySessionByToken, sessionToken)
	return scanGatewaySessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetGatewaySessionByID
// ─────────────────────────────────────────────────────────────────────────────

const getGatewaySessionByID = `-- name: GetGatewaySessionByID :one
SELECT id, session_token, customer_id, org_id, channel_id, locale,
       promo_codes, created_at, last_seen_at, expires_at
FROM   gateway_sessions
WHERE  id = $1`

// GetGatewaySessionByID loads a session by platform uuid. Used by
// admin/debug surfaces — production wire traffic always keys off
// session_token.
func (q *Queries) GetGatewaySessionByID(ctx context.Context, id uuid.UUID) (GatewaySessionRow, error) {
	row := q.db.QueryRow(ctx, getGatewaySessionByID, id)
	return scanGatewaySessionRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// TouchGatewaySession
// ─────────────────────────────────────────────────────────────────────────────

const touchGatewaySession = `-- name: TouchGatewaySession :exec
UPDATE gateway_sessions
SET    last_seen_at = now()
WHERE  id = $1`

// TouchGatewaySession bumps last_seen_at after a successful wire dispatch.
// Does NOT extend expires_at — expiry is anchored at creation time so
// the site rotates its cookie predictably (spec §5).
func (q *Queries) TouchGatewaySession(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, touchGatewaySession, id)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// ExtendGatewaySessionExpiry
// ─────────────────────────────────────────────────────────────────────────────

const extendGatewaySessionExpiry = `-- name: ExtendGatewaySessionExpiry :exec
UPDATE gateway_sessions
SET    expires_at   = $2,
       last_seen_at = now()
WHERE  id = $1`

// ExtendGatewaySessionExpiry advances expires_at for the rare case where
// the gateway needs to refresh a still-active session (spec §5). Callers
// pass the desired expires_at explicitly so TTL policy lives in Go.
func (q *Queries) ExtendGatewaySessionExpiry(ctx context.Context, id uuid.UUID, expiresAt time.Time) error {
	_, err := q.db.Exec(ctx, extendGatewaySessionExpiry, id, expiresAt)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// AppendGatewaySessionPromoCode
// ─────────────────────────────────────────────────────────────────────────────

const appendGatewaySessionPromoCode = `-- name: AppendGatewaySessionPromoCode :exec
UPDATE gateway_sessions
SET    promo_codes = ARRAY(SELECT DISTINCT unnest(promo_codes || $2::text[]))
WHERE  id = $1`

// AppendGatewaySessionPromoCode appends one or more promo codes to the
// session's promo_codes array, deduplicating along the way. Idempotent —
// spec §7 (ADD_PROMO_CODES) treats an already-registered code as "exist",
// not an error.
func (q *Queries) AppendGatewaySessionPromoCode(ctx context.Context, id uuid.UUID, promoCodes []string) error {
	_, err := q.db.Exec(ctx, appendGatewaySessionPromoCode, id, promoCodes)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteExpiredGatewaySessions
// ─────────────────────────────────────────────────────────────────────────────

const deleteExpiredGatewaySessions = `-- name: DeleteExpiredGatewaySessions :exec
DELETE FROM gateway_sessions
WHERE expires_at < now()`

// DeleteExpiredGatewaySessions is the reaper query — drops rows whose
// expires_at is in the past. Intended for a periodic housekeeping job.
// Safe against reservations.gateway_session_id because the FK is
// ON DELETE SET NULL.
func (q *Queries) DeleteExpiredGatewaySessions(ctx context.Context) error {
	_, err := q.db.Exec(ctx, deleteExpiredGatewaySessions)
	return err
}
