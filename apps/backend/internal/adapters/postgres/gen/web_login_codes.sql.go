// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: web_login_codes.sql (migration 0151)

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const revokeOpenWebLoginCodes = `-- name: RevokeOpenWebLoginCodes :exec
UPDATE web_login_codes
SET    revoked_at = now()
WHERE  user_id = $1 AND used_at IS NULL AND revoked_at IS NULL
`

// RevokeOpenWebLoginCodes takes back every unused code of a person: a new
// code replaces the old one, and "Sign out everywhere" kills all of them.
func (q *Queries) RevokeOpenWebLoginCodes(ctx context.Context, userID uuid.UUID) error {
	_, err := q.db.Exec(ctx, revokeOpenWebLoginCodes, userID)
	return err
}

const insertWebLoginCode = `-- name: InsertWebLoginCode :exec
INSERT INTO web_login_codes (code_hash, user_id, telegram_user_id, expires_at)
VALUES ($1, $2, $3, $4)
`

// InsertWebLoginCode stores the SHA-256 of a new code.
func (q *Queries) InsertWebLoginCode(ctx context.Context, codeHash string, userID uuid.UUID, telegramUserID int64, expiresAt time.Time) error {
	_, err := q.db.Exec(ctx, insertWebLoginCode, codeHash, userID, telegramUserID, expiresAt)
	return err
}

// UsedWebLoginCode is a code just exchanged for a session.
type UsedWebLoginCode struct {
	ID             uuid.UUID `json:"id"`
	UserID         uuid.UUID `json:"user_id"`
	TelegramUserID int64     `json:"telegram_user_id"`
}

const useWebLoginCode = `-- name: UseWebLoginCode :one
UPDATE web_login_codes
SET    used_at = now()
WHERE  code_hash = $1
  AND  used_at IS NULL
  AND  revoked_at IS NULL
  AND  expires_at > now()
RETURNING id, user_id, telegram_user_id
`

// UseWebLoginCode marks a live code used and returns it. A code that is
// unknown, used, taken back or expired answers pgx.ErrNoRows, so two
// exchanges of one code cannot both win.
func (q *Queries) UseWebLoginCode(ctx context.Context, codeHash string) (UsedWebLoginCode, error) {
	var r UsedWebLoginCode
	err := q.db.QueryRow(ctx, useWebLoginCode, codeHash).Scan(&r.ID, &r.UserID, &r.TelegramUserID)
	return r, err
}

// WebLoginNotice is a sign-in the bot has to tell the person about.
type WebLoginNotice struct {
	ID             uuid.UUID `json:"id"`
	TelegramUserID int64     `json:"telegram_user_id"`
	UsedAt         time.Time `json:"used_at"`
}

const claimWebLoginNotices = `-- name: ClaimWebLoginNotices :many
UPDATE web_login_codes
SET    notice_claimed_at = now()
WHERE  id IN (
           SELECT id FROM web_login_codes
           WHERE  used_at IS NOT NULL AND notice_claimed_at IS NULL
             AND  used_at > now() - interval '1 day'
           ORDER  BY used_at
           LIMIT  $1
           FOR UPDATE SKIP LOCKED)
RETURNING id, telegram_user_id, used_at
`

// ClaimWebLoginNotices marks up to limit unannounced sign-ins of the last
// day as announced and returns them. A sign-in is announced at most once: a
// crash between the claim and the message loses one message, never repeats
// it.
func (q *Queries) ClaimWebLoginNotices(ctx context.Context, limit int32) ([]WebLoginNotice, error) {
	rows, err := q.db.Query(ctx, claimWebLoginNotices, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebLoginNotice
	for rows.Next() {
		var n WebLoginNotice
		if err := rows.Scan(&n.ID, &n.TelegramUserID, &n.UsedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

const revokeWebRefreshTokens = `-- name: RevokeWebRefreshTokens :execrows
UPDATE refresh_tokens
SET    expires_at = now()
WHERE  user_id = $1 AND client_channel = 'event_center_web'
  AND  revoked_at IS NULL AND expires_at > now()
`

// RevokeWebRefreshTokens ends every web event center session of a person
// ("Sign out everywhere"). Sessions of admin-web and of the bot are kept.
//
// It EXPIRES the tokens instead of setting revoked_at: an open web tab will
// still send its token once, and a revoked token coming back is treated as
// theft (POST /v1/auth/refresh revokes every session of the person, the
// admin console included). An expired token is just refused.
func (q *Queries) RevokeWebRefreshTokens(ctx context.Context, userID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, revokeWebRefreshTokens, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
