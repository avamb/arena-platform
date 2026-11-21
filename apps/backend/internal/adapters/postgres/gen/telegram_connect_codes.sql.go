// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: telegram_connect_codes.sql (migration 0213)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const revokeOpenTelegramConnectCodes = `-- name: RevokeOpenTelegramConnectCodes :exec
UPDATE telegram_connect_codes
SET    revoked_at = now()
WHERE  user_id = $1 AND used_at IS NULL AND revoked_at IS NULL
`

// RevokeOpenTelegramConnectCodes takes back every unused "Connect Telegram"
// code of a person: only the newest link on the screen works.
func (q *Queries) RevokeOpenTelegramConnectCodes(ctx context.Context, userID uuid.UUID) error {
	_, err := q.db.Exec(ctx, revokeOpenTelegramConnectCodes, userID)
	return err
}

const insertTelegramConnectCode = `-- name: InsertTelegramConnectCode :exec
INSERT INTO telegram_connect_codes (code_hash, user_id, org_id, expires_at)
VALUES ($1, $2, $3, $4)
`

// InsertTelegramConnectCode stores the SHA-256 of a new code.
func (q *Queries) InsertTelegramConnectCode(ctx context.Context, codeHash string, userID, orgID uuid.UUID, expiresAt time.Time) error {
	_, err := q.db.Exec(ctx, insertTelegramConnectCode, codeHash, userID, orgID, expiresAt)
	return err
}

// TelegramConnectCodeRow is a live code with the names the bot shows.
type TelegramConnectCodeRow struct {
	ID            uuid.UUID  `json:"id"`
	UserID        uuid.UUID  `json:"user_id"`
	OrgID         uuid.UUID  `json:"org_id"`
	ExpiresAt     time.Time  `json:"expires_at"`
	OrgName       string     `json:"org_name"`
	OrgArchived   bool       `json:"org_archived"`
	Email         string     `json:"email"`
	DeactivatedAt *time.Time `json:"deactivated_at"`
}

const telegramConnectCodeColumns = `c.id, c.user_id, c.org_id, c.expires_at, o.name, (o.deleted_at IS NOT NULL), u.email, u.deactivated_at`

func scanTelegramConnectCodeRow(row interface{ Scan(dest ...any) error }) (TelegramConnectCodeRow, error) {
	var r TelegramConnectCodeRow
	err := row.Scan(&r.ID, &r.UserID, &r.OrgID, &r.ExpiresAt, &r.OrgName, &r.OrgArchived, &r.Email, &r.DeactivatedAt)
	return r, err
}

const getLiveTelegramConnectCode = `-- name: GetLiveTelegramConnectCode :one
SELECT ` + telegramConnectCodeColumns + `
FROM   telegram_connect_codes c
JOIN   organizations o ON o.id = c.org_id
JOIN   users u ON u.id = c.user_id
WHERE  c.code_hash = $1
  AND  c.used_at IS NULL
  AND  c.revoked_at IS NULL
  AND  c.expires_at > now()
`

// GetLiveTelegramConnectCode reads a code that may still be used, without
// using it (the bot's confirmation screen). An unknown, used, revoked or
// expired code is pgx.ErrNoRows.
func (q *Queries) GetLiveTelegramConnectCode(ctx context.Context, codeHash string) (TelegramConnectCodeRow, error) {
	return scanTelegramConnectCodeRow(q.db.QueryRow(ctx, getLiveTelegramConnectCode, codeHash))
}

const lockLiveTelegramConnectCode = `-- name: LockLiveTelegramConnectCode :one
SELECT ` + telegramConnectCodeColumns + `
FROM   telegram_connect_codes c
JOIN   organizations o ON o.id = c.org_id
JOIN   users u ON u.id = c.user_id
WHERE  c.code_hash = $1
  AND  c.used_at IS NULL
  AND  c.revoked_at IS NULL
  AND  c.expires_at > now()
FOR UPDATE OF c
`

// LockLiveTelegramConnectCode is GetLiveTelegramConnectCode under a row lock
// of the code: two presses of the same link serialize, and the second finds
// the code used.
func (q *Queries) LockLiveTelegramConnectCode(ctx context.Context, codeHash string) (TelegramConnectCodeRow, error) {
	return scanTelegramConnectCodeRow(q.db.QueryRow(ctx, lockLiveTelegramConnectCode, codeHash))
}

const useTelegramConnectCode = `-- name: UseTelegramConnectCode :exec
UPDATE telegram_connect_codes
SET    used_at = now(), used_telegram_user_id = $2
WHERE  id = $1 AND used_at IS NULL
`

// UseTelegramConnectCode marks a code used by a Telegram account.
func (q *Queries) UseTelegramConnectCode(ctx context.Context, id uuid.UUID, telegramUserID int64) error {
	_, err := q.db.Exec(ctx, useTelegramConnectCode, id, telegramUserID)
	return err
}

// TelegramLinkOfUser is one live Telegram account of a person.
type TelegramLinkOfUser struct {
	TelegramUserID   int64     `json:"telegram_user_id"`
	TelegramUsername *string   `json:"telegram_username"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const listLiveTelegramLinksOfUser = `-- name: ListLiveTelegramLinksOfUser :many
SELECT telegram_user_id, telegram_username, updated_at
FROM   bot_telegram_links
WHERE  user_id = $1 AND revoked_at IS NULL
ORDER  BY updated_at DESC
LIMIT  5
`

// ListLiveTelegramLinksOfUser lists the Telegram accounts linked to a person,
// newest first (the web says whether Telegram is already connected).
func (q *Queries) ListLiveTelegramLinksOfUser(ctx context.Context, userID uuid.UUID) ([]TelegramLinkOfUser, error) {
	rows, err := q.db.Query(ctx, listLiveTelegramLinksOfUser, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelegramLinkOfUser
	for rows.Next() {
		var r TelegramLinkOfUser
		if err := rows.Scan(&r.TelegramUserID, &r.TelegramUsername, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
