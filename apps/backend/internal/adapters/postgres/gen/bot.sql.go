package gen

// bot.sql.go — hand-maintained wrappers for queries/bot.sql: the Telegram
// event-center bot's own tables (migration 0115). Nothing here reads a
// business table; the bot itself acts on those through the REST API as the
// linked user (08_architecture/28_telegram_event_center_bot_ru.md §2.2).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// BotInvitationRow mirrors one bot_invitations row. CodeHash is the SHA-256
// of the one-time code; the code itself is never stored.
type BotInvitationRow struct {
	ID                     uuid.UUID  `json:"id"`
	OrgID                  uuid.UUID  `json:"org_id"`
	UserID                 uuid.UUID  `json:"user_id"`
	Email                  string     `json:"email"`
	Role                   string     `json:"role"`
	CodeHash               string     `json:"-"`
	InvitedBy              *uuid.UUID `json:"invited_by"`
	ExpiresAt              time.Time  `json:"expires_at"`
	AcceptedAt             *time.Time `json:"accepted_at"`
	AcceptedTelegramUserID *int64     `json:"accepted_telegram_user_id"`
	CreatedAt              time.Time  `json:"created_at"`
}

const botInvitationColumns = `id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at`

func scanBotInvitationRow(row interface{ Scan(dest ...any) error }) (BotInvitationRow, error) {
	var r BotInvitationRow
	err := row.Scan(&r.ID, &r.OrgID, &r.UserID, &r.Email, &r.Role, &r.CodeHash, &r.InvitedBy,
		&r.ExpiresAt, &r.AcceptedAt, &r.AcceptedTelegramUserID, &r.CreatedAt)
	return r, err
}

const insertBotInvitation = `-- name: InsertBotInvitation :one
INSERT INTO bot_invitations (org_id, user_id, email, role, code_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING ` + botInvitationColumns

// InsertBotInvitation stores a new one-time invitation. invitedBy is nil when
// the platform superadmin issued it outside any organization membership.
func (q *Queries) InsertBotInvitation(ctx context.Context, orgID, userID uuid.UUID, email, role, codeHash string, invitedBy *uuid.UUID, expiresAt time.Time) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, insertBotInvitation, orgID, userID, email, role, codeHash, invitedBy, expiresAt))
}

const getBotInvitationByCodeHash = `-- name: GetBotInvitationByCodeHash :one
SELECT ` + botInvitationColumns + `
FROM   bot_invitations
WHERE  code_hash = $1`

// GetBotInvitationByCodeHash returns the invitation behind a code hash,
// whatever its state — the caller decides how expired/accepted rows answer.
func (q *Queries) GetBotInvitationByCodeHash(ctx context.Context, codeHash string) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, getBotInvitationByCodeHash, codeHash))
}

const acceptBotInvitation = `-- name: AcceptBotInvitation :one
UPDATE bot_invitations
SET    accepted_at               = now(),
       accepted_telegram_user_id = $2
WHERE  id = $1
  AND  accepted_at IS NULL
  AND  expires_at > now()
RETURNING ` + botInvitationColumns

// AcceptBotInvitation marks the invitation used by telegramUserID. It answers
// pgx.ErrNoRows when the row was already accepted or has expired, so two
// concurrent redemptions cannot both succeed.
func (q *Queries) AcceptBotInvitation(ctx context.Context, id uuid.UUID, telegramUserID int64) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, acceptBotInvitation, id, telegramUserID))
}

// BotTelegramLinkRow mirrors one bot_telegram_links row: which arena user a
// Telegram account speaks as, plus the bot's per-user memory.
type BotTelegramLinkRow struct {
	TelegramUserID   int64           `json:"telegram_user_id"`
	UserID           uuid.UUID       `json:"user_id"`
	TelegramUsername *string         `json:"telegram_username"`
	Locale           string          `json:"locale"`
	Defaults         json.RawMessage `json:"defaults"`
	CurrentOrgID     *uuid.UUID      `json:"current_org_id"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	RevokedAt        *time.Time      `json:"revoked_at"`
}

const botTelegramLinkColumns = `telegram_user_id, user_id, telegram_username, locale, defaults, current_org_id,
       created_at, updated_at, revoked_at`

func scanBotTelegramLinkRow(row interface{ Scan(dest ...any) error }) (BotTelegramLinkRow, error) {
	var r BotTelegramLinkRow
	err := row.Scan(&r.TelegramUserID, &r.UserID, &r.TelegramUsername, &r.Locale, &r.Defaults,
		&r.CurrentOrgID, &r.CreatedAt, &r.UpdatedAt, &r.RevokedAt)
	if err == nil && len(r.Defaults) == 0 {
		r.Defaults = json.RawMessage(`{}`)
	}
	return r, err
}

const getBotTelegramLink = `-- name: GetBotTelegramLink :one
SELECT ` + botTelegramLinkColumns + `
FROM   bot_telegram_links
WHERE  telegram_user_id = $1`

// GetBotTelegramLink returns the link of a Telegram account, revoked or not.
func (q *Queries) GetBotTelegramLink(ctx context.Context, telegramUserID int64) (BotTelegramLinkRow, error) {
	return scanBotTelegramLinkRow(q.db.QueryRow(ctx, getBotTelegramLink, telegramUserID))
}

const upsertBotTelegramLink = `-- name: UpsertBotTelegramLink :one
INSERT INTO bot_telegram_links (telegram_user_id, user_id, telegram_username, locale, current_org_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (telegram_user_id) DO UPDATE
SET    user_id           = EXCLUDED.user_id,
       telegram_username = EXCLUDED.telegram_username,
       locale            = EXCLUDED.locale,
       current_org_id    = EXCLUDED.current_org_id,
       revoked_at        = NULL,
       updated_at        = now()
RETURNING ` + botTelegramLinkColumns

// UpsertBotTelegramLink binds (or re-binds) a Telegram account to a user and
// clears any earlier revocation. The caller decides whether re-binding to a
// DIFFERENT user is allowed; the query itself does not refuse it.
func (q *Queries) UpsertBotTelegramLink(ctx context.Context, telegramUserID int64, userID uuid.UUID, telegramUsername *string, locale string, currentOrgID *uuid.UUID) (BotTelegramLinkRow, error) {
	return scanBotTelegramLinkRow(q.db.QueryRow(ctx, upsertBotTelegramLink, telegramUserID, userID, telegramUsername, locale, currentOrgID))
}

const updateBotTelegramLinkLocale = `-- name: UpdateBotTelegramLinkLocale :exec
UPDATE bot_telegram_links
SET    locale = $2, updated_at = now()
WHERE  telegram_user_id = $1`

// UpdateBotTelegramLinkLocale changes the language the bot speaks to this account.
func (q *Queries) UpdateBotTelegramLinkLocale(ctx context.Context, telegramUserID int64, locale string) error {
	_, err := q.db.Exec(ctx, updateBotTelegramLinkLocale, telegramUserID, locale)
	return err
}

const updateBotTelegramLinkDefaults = `-- name: UpdateBotTelegramLinkDefaults :exec
UPDATE bot_telegram_links
SET    defaults = $2, updated_at = now()
WHERE  telegram_user_id = $1`

// UpdateBotTelegramLinkDefaults replaces the remembered wizard answers.
func (q *Queries) UpdateBotTelegramLinkDefaults(ctx context.Context, telegramUserID int64, defaults json.RawMessage) error {
	if len(defaults) == 0 {
		defaults = json.RawMessage(`{}`)
	}
	_, err := q.db.Exec(ctx, updateBotTelegramLinkDefaults, telegramUserID, defaults)
	return err
}

const setBotTelegramLinkCurrentOrg = `-- name: SetBotTelegramLinkCurrentOrg :exec
UPDATE bot_telegram_links
SET    current_org_id = $2, updated_at = now()
WHERE  telegram_user_id = $1`

// SetBotTelegramLinkCurrentOrg remembers which organization the account works in.
func (q *Queries) SetBotTelegramLinkCurrentOrg(ctx context.Context, telegramUserID int64, orgID *uuid.UUID) error {
	_, err := q.db.Exec(ctx, setBotTelegramLinkCurrentOrg, telegramUserID, orgID)
	return err
}

const revokeBotTelegramLink = `-- name: RevokeBotTelegramLink :exec
UPDATE bot_telegram_links
SET    revoked_at = now(), updated_at = now()
WHERE  telegram_user_id = $1`

// RevokeBotTelegramLink disconnects a Telegram account without deleting its memory.
func (q *Queries) RevokeBotTelegramLink(ctx context.Context, telegramUserID int64) error {
	_, err := q.db.Exec(ctx, revokeBotTelegramLink, telegramUserID)
	return err
}

// BotDraftRow mirrors one bot_drafts row: the wizard state of one Telegram
// account in one organization.
type BotDraftRow struct {
	ID             uuid.UUID       `json:"id"`
	TelegramUserID int64           `json:"telegram_user_id"`
	OrgID          uuid.UUID       `json:"org_id"`
	Mode           string          `json:"mode"`
	EventID        *uuid.UUID      `json:"event_id"`
	Step           string          `json:"step"`
	SchemaVersion  int32           `json:"schema_version"`
	State          json.RawMessage `json:"state"`
	Saved          json.RawMessage `json:"saved"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

const botDraftColumns = `id, telegram_user_id, org_id, mode, event_id, step, schema_version, state, saved,
       created_at, updated_at`

func scanBotDraftRow(row interface{ Scan(dest ...any) error }) (BotDraftRow, error) {
	var r BotDraftRow
	err := row.Scan(&r.ID, &r.TelegramUserID, &r.OrgID, &r.Mode, &r.EventID, &r.Step, &r.SchemaVersion,
		&r.State, &r.Saved, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const getBotDraft = `-- name: GetBotDraft :one
SELECT ` + botDraftColumns + `
FROM   bot_drafts
WHERE  telegram_user_id = $1
  AND  org_id = $2`

// GetBotDraft returns the account's draft in the organization, or pgx.ErrNoRows.
func (q *Queries) GetBotDraft(ctx context.Context, telegramUserID int64, orgID uuid.UUID) (BotDraftRow, error) {
	return scanBotDraftRow(q.db.QueryRow(ctx, getBotDraft, telegramUserID, orgID))
}

const upsertBotDraft = `-- name: UpsertBotDraft :one
INSERT INTO bot_drafts (telegram_user_id, org_id, mode, event_id, step, schema_version, state, saved)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (telegram_user_id, org_id) DO UPDATE
SET    mode           = EXCLUDED.mode,
       event_id       = EXCLUDED.event_id,
       step           = EXCLUDED.step,
       schema_version = EXCLUDED.schema_version,
       state          = EXCLUDED.state,
       saved          = EXCLUDED.saved,
       updated_at     = now()
RETURNING ` + botDraftColumns

// UpsertBotDraft writes the whole draft; every message the bot handles ends
// with one of these so a restart never loses the operator's answers.
func (q *Queries) UpsertBotDraft(ctx context.Context, telegramUserID int64, orgID uuid.UUID, mode string, eventID *uuid.UUID, step string, schemaVersion int32, state, saved json.RawMessage) (BotDraftRow, error) {
	if len(state) == 0 {
		state = json.RawMessage(`{}`)
	}
	if len(saved) == 0 {
		saved = json.RawMessage(`{}`)
	}
	return scanBotDraftRow(q.db.QueryRow(ctx, upsertBotDraft, telegramUserID, orgID, mode, eventID, step, schemaVersion, state, saved))
}

const deleteBotDraft = `-- name: DeleteBotDraft :exec
DELETE FROM bot_drafts
WHERE  telegram_user_id = $1
  AND  org_id = $2`

// DeleteBotDraft discards the draft (cancel, or a finished publish).
func (q *Queries) DeleteBotDraft(ctx context.Context, telegramUserID int64, orgID uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteBotDraft, telegramUserID, orgID)
	return err
}

// ─── team ─────────────────────────────────────────────────────────────────────

// BotTeamMemberRow is one member of the organization as the bot shows it.
type BotTeamMemberRow struct {
	UserID            uuid.UUID `json:"user_id"`
	Email             string    `json:"email"`
	MembershipRole    string    `json:"membership_role"`
	JoinedAt          time.Time `json:"joined_at"`
	TelegramLinked    bool      `json:"telegram_linked"`
	InvitationPending bool      `json:"invitation_pending"`
}

const listBotTeam = `-- name: ListBotTeam :many
SELECT u.id      AS user_id,
       u.email,
       m.role    AS membership_role,
       m.joined_at,
       EXISTS (SELECT 1 FROM bot_telegram_links l
               WHERE l.user_id = u.id AND l.revoked_at IS NULL)              AS telegram_linked,
       EXISTS (SELECT 1 FROM bot_invitations i
               WHERE i.user_id = u.id AND i.org_id = m.org_id
                 AND i.accepted_at IS NULL AND i.expires_at > now())         AS invitation_pending
FROM   memberships m
JOIN   users u ON u.id = m.user_id
WHERE  m.org_id = $1
  AND  m.status = 'active'
  AND  m.role IN ('org_admin', 'organizer')
ORDER  BY (m.role = 'org_admin') DESC, u.email`

// ListBotTeam lists the organization's owners and managers, owners first.
func (q *Queries) ListBotTeam(ctx context.Context, orgID uuid.UUID) ([]BotTeamMemberRow, error) {
	rows, err := q.db.Query(ctx, listBotTeam, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotTeamMemberRow
	for rows.Next() {
		var r BotTeamMemberRow
		if err := rows.Scan(&r.UserID, &r.Email, &r.MembershipRole, &r.JoinedAt, &r.TelegramLinked, &r.InvitationPending); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
