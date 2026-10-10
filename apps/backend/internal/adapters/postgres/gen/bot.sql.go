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
	// RevokedAt is set when the owner annulled the invitation (migration
	// 0137); its code no longer redeems.
	RevokedAt *time.Time `json:"revoked_at"`
	// MembershipCreated is true when the transaction that created the
	// invitation also inserted the membership - only then may a revocation
	// remove that membership.
	MembershipCreated bool `json:"membership_created"`
	// LastSentAt is when the letter last went out (creation or resend).
	LastSentAt time.Time `json:"last_sent_at"`
}

const botInvitationColumns = `id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at`

func scanBotInvitationRow(row interface{ Scan(dest ...any) error }) (BotInvitationRow, error) {
	var r BotInvitationRow
	err := row.Scan(&r.ID, &r.OrgID, &r.UserID, &r.Email, &r.Role, &r.CodeHash, &r.InvitedBy,
		&r.ExpiresAt, &r.AcceptedAt, &r.AcceptedTelegramUserID, &r.CreatedAt,
		&r.RevokedAt, &r.MembershipCreated, &r.LastSentAt)
	return r, err
}

const insertBotInvitation = `-- name: InsertBotInvitation :one
INSERT INTO bot_invitations (org_id, user_id, email, role, code_hash, invited_by, expires_at, membership_created)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING ` + botInvitationColumns

// InsertBotInvitation stores a new one-time invitation. invitedBy is nil when
// the platform superadmin issued it outside any organization membership.
// membershipCreated says the caller's transaction inserted the membership too
// (false when the person was already a member): only then may revoking the
// invitation remove that membership.
func (q *Queries) InsertBotInvitation(ctx context.Context, orgID, userID uuid.UUID, email, role, codeHash string, invitedBy *uuid.UUID, expiresAt time.Time, membershipCreated bool) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, insertBotInvitation, orgID, userID, email, role, codeHash, invitedBy, expiresAt, membershipCreated))
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
  AND  revoked_at IS NULL
  AND  expires_at > now()
RETURNING ` + botInvitationColumns

// AcceptBotInvitation marks the invitation used by telegramUserID. It answers
// pgx.ErrNoRows when the row was already accepted, was revoked or has
// expired, so two concurrent redemptions cannot both succeed.
func (q *Queries) AcceptBotInvitation(ctx context.Context, id uuid.UUID, telegramUserID int64) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, acceptBotInvitation, id, telegramUserID))
}

const getBotInvitationInOrgForUpdate = `-- name: GetBotInvitationInOrgForUpdate :one
SELECT ` + botInvitationColumns + `
FROM   bot_invitations
WHERE  id = $1 AND org_id = $2
FOR UPDATE`

// GetBotInvitationInOrgForUpdate locks and returns an invitation of the
// organization (pgx.ErrNoRows for a missing one or one of another
// organization - the same answer, so an id cannot be probed across tenants).
func (q *Queries) GetBotInvitationInOrgForUpdate(ctx context.Context, id, orgID uuid.UUID) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, getBotInvitationInOrgForUpdate, id, orgID))
}

const getBotInvitationInOrg = `-- name: GetBotInvitationInOrg :one
SELECT ` + botInvitationColumns + `
FROM   bot_invitations
WHERE  id = $1 AND org_id = $2`

// GetBotInvitationInOrg returns an invitation of the organization.
func (q *Queries) GetBotInvitationInOrg(ctx context.Context, id, orgID uuid.UUID) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, getBotInvitationInOrg, id, orgID))
}

const revokeBotInvitation = `-- name: RevokeBotInvitation :one
UPDATE bot_invitations
SET    revoked_at = now()
WHERE  id = $1
  AND  revoked_at IS NULL
RETURNING ` + botInvitationColumns

// RevokeBotInvitation annuls the invitation (pgx.ErrNoRows when it already
// was). The row stays; its code no longer redeems.
func (q *Queries) RevokeBotInvitation(ctx context.Context, id uuid.UUID) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, revokeBotInvitation, id))
}

const countOtherLiveBotInvitations = `-- name: CountOtherLiveBotInvitations :one
SELECT count(*)
FROM   bot_invitations
WHERE  user_id = $1
  AND  org_id  = $2
  AND  id <> $3
  AND  revoked_at IS NULL
  AND (accepted_at IS NOT NULL OR expires_at > now())`

// CountOtherLiveBotInvitations counts the OTHER invitations that still keep
// the person in the organization: accepted ones, and ones not yet accepted
// that neither expired nor were revoked.
func (q *Queries) CountOtherLiveBotInvitations(ctx context.Context, userID, orgID, exceptID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countOtherLiveBotInvitations, userID, orgID, exceptID).Scan(&n)
	return n, err
}

const botInvitationCreatedMembership = `-- name: BotInvitationCreatedMembership :one
SELECT EXISTS (
    SELECT 1 FROM bot_invitations
    WHERE  user_id = $1 AND org_id = $2 AND membership_created
)`

// BotInvitationCreatedMembership reports whether any invitation of the person
// to the organization, revoked or not, inserted the membership.
func (q *Queries) BotInvitationCreatedMembership(ctx context.Context, userID, orgID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, botInvitationCreatedMembership, userID, orgID).Scan(&ok)
	return ok, err
}

const botUserWorksInOrg = `-- name: BotUserWorksInOrg :one
SELECT EXISTS (
           SELECT 1 FROM bot_telegram_links l
           WHERE  l.user_id = $1 AND l.revoked_at IS NULL AND l.current_org_id = $2
       )
    OR EXISTS (
           SELECT 1 FROM bot_drafts d
           JOIN   bot_telegram_links l ON l.telegram_user_id = d.telegram_user_id
           WHERE  l.user_id = $1 AND d.org_id = $2
       )
    OR EXISTS (
           SELECT 1 FROM bot_dialogs g
           JOIN   bot_telegram_links l ON l.telegram_user_id = g.telegram_user_id
           WHERE  l.user_id = $1 AND g.org_id = $2
       )`

// BotUserWorksInOrg reports whether the person has already started working in
// the organization through the bot by another route than accepting THIS
// invitation: a live Telegram link whose current organization it is, a wizard
// draft or an open dialog there. (A person who was a member of another
// organization has a Telegram link from it and may open this one at once,
// because the membership exists from the moment of the invitation.)
func (q *Queries) BotUserWorksInOrg(ctx context.Context, userID, orgID uuid.UUID) (bool, error) {
	var b bool
	err := q.db.QueryRow(ctx, botUserWorksInOrg, userID, orgID).Scan(&b)
	return b, err
}

const countActiveOrgAdmins = `-- name: CountActiveOrgAdmins :one
SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'org_admin' AND status = 'active'`

// CountActiveOrgAdmins counts the organization's active owners.
func (q *Queries) CountActiveOrgAdmins(ctx context.Context, orgID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, countActiveOrgAdmins, orgID).Scan(&n)
	return n, err
}

const resendBotInvitation = `-- name: ResendBotInvitation :one
UPDATE bot_invitations
SET    code_hash    = $3,
       expires_at   = $4,
       last_sent_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  accepted_at IS NULL
  AND  revoked_at IS NULL
  AND  last_sent_at <= now() - make_interval(secs => $5::int)
RETURNING ` + botInvitationColumns

// ResendBotInvitation gives a not-yet-accepted, not-revoked invitation a
// fresh code and expiry and stamps last_sent_at, in ONE statement that also
// enforces the minimum interval since the last letter - two concurrent
// resends cannot both pass. pgx.ErrNoRows when the row does not qualify (the
// caller re-reads it to say why).
func (q *Queries) ResendBotInvitation(ctx context.Context, id, orgID uuid.UUID, codeHash string, expiresAt time.Time, minIntervalSeconds int32) (BotInvitationRow, error) {
	return scanBotInvitationRow(q.db.QueryRow(ctx, resendBotInvitation, id, orgID, codeHash, expiresAt, minIntervalSeconds))
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
       reminded_at    = NULL,
       updated_at     = now()
RETURNING ` + botDraftColumns

// UpsertBotDraft writes the whole draft; every message the bot handles ends
// with one of these so a restart never loses the operator's answers. It also
// clears reminded_at: an answer makes the draft live again, so it earns a
// fresh idle reminder once it goes quiet anew.
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

// ─── idle drafts (migration 0130) ─────────────────────────────────────────────

// BotDraftReminderRow is what the idle-draft sweep needs to tell a draft's
// owner about it: the draft's identity and content plus the owner's language.
type BotDraftReminderRow struct {
	ID             uuid.UUID       `json:"id"`
	TelegramUserID int64           `json:"telegram_user_id"`
	OrgID          uuid.UUID       `json:"org_id"`
	Mode           string          `json:"mode"`
	EventID        *uuid.UUID      `json:"event_id"`
	State          json.RawMessage `json:"state"`
	Locale         string          `json:"locale"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

const botDraftReminderColumns = `d.id, d.telegram_user_id, d.org_id, d.mode, d.event_id, d.state, l.locale, d.updated_at`

func scanBotDraftReminderRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}) ([]BotDraftReminderRow, error) {
	defer rows.Close()
	var out []BotDraftReminderRow
	for rows.Next() {
		var r BotDraftReminderRow
		if err := rows.Scan(&r.ID, &r.TelegramUserID, &r.OrgID, &r.Mode, &r.EventID, &r.State, &r.Locale, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listBotDraftsForReminder = `-- name: ListBotDraftsForReminder :many
SELECT ` + botDraftReminderColumns + `
FROM   bot_drafts d
JOIN   bot_telegram_links l ON l.telegram_user_id = d.telegram_user_id
WHERE  d.updated_at < $1
  AND  d.reminded_at IS NULL
ORDER  BY d.updated_at
LIMIT  $2`

// ListBotDraftsForReminder lists up to limit drafts untouched since before
// idleBefore whose owner has not been reminded yet, oldest first.
func (q *Queries) ListBotDraftsForReminder(ctx context.Context, idleBefore time.Time, limit int32) ([]BotDraftReminderRow, error) {
	rows, err := q.db.Query(ctx, listBotDraftsForReminder, idleBefore, limit)
	if err != nil {
		return nil, err
	}
	return scanBotDraftReminderRows(rows)
}

const markBotDraftReminded = `-- name: MarkBotDraftReminded :exec
UPDATE bot_drafts
SET    reminded_at = now()
WHERE  id = $1`

// MarkBotDraftReminded records that the idle reminder for a draft was sent.
// It leaves updated_at alone, so the reminder does not postpone the draft's
// own expiry.
func (q *Queries) MarkBotDraftReminded(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, markBotDraftReminded, id)
	return err
}

const deleteBotDraftsIdleBefore = `-- name: DeleteBotDraftsIdleBefore :many
DELETE FROM bot_drafts d
USING  bot_telegram_links l
WHERE  l.telegram_user_id = d.telegram_user_id
  AND  d.updated_at < $1
RETURNING ` + botDraftReminderColumns

// DeleteBotDraftsIdleBefore deletes every draft untouched since before
// idleBefore and returns them, so their owners can be told the draft is gone.
func (q *Queries) DeleteBotDraftsIdleBefore(ctx context.Context, idleBefore time.Time) ([]BotDraftReminderRow, error) {
	rows, err := q.db.Query(ctx, deleteBotDraftsIdleBefore, idleBefore)
	if err != nil {
		return nil, err
	}
	return scanBotDraftReminderRows(rows)
}

// ─── dialogs (migration 0130) ─────────────────────────────────────────────────

// BotDialogRow mirrors one bot_dialogs row: a short multi-step dialog of one
// Telegram account (the team invite, a session move, ...). Step and State
// belong to the dialog's own code; the table only keeps them.
type BotDialogRow struct {
	ID             uuid.UUID       `json:"id"`
	TelegramUserID int64           `json:"telegram_user_id"`
	OrgID          *uuid.UUID      `json:"org_id"`
	Kind           string          `json:"kind"`
	Step           string          `json:"step"`
	State          json.RawMessage `json:"state"`
	ExpiresAt      time.Time       `json:"expires_at"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

const botDialogColumns = `id, telegram_user_id, org_id, kind, step, state, expires_at, created_at, updated_at`

func scanBotDialogRow(row interface{ Scan(dest ...any) error }) (BotDialogRow, error) {
	var r BotDialogRow
	err := row.Scan(&r.ID, &r.TelegramUserID, &r.OrgID, &r.Kind, &r.Step, &r.State, &r.ExpiresAt, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const upsertBotDialog = `-- name: UpsertBotDialog :one
INSERT INTO bot_dialogs (telegram_user_id, org_id, kind, step, state, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (telegram_user_id, kind) DO UPDATE
SET    org_id     = EXCLUDED.org_id,
       step       = EXCLUDED.step,
       state      = EXCLUDED.state,
       expires_at = EXCLUDED.expires_at,
       updated_at = now()
RETURNING ` + botDialogColumns

// UpsertBotDialog writes the account's dialog of this kind, replacing any
// earlier one: there is one live dialog per account and kind.
func (q *Queries) UpsertBotDialog(ctx context.Context, telegramUserID int64, orgID *uuid.UUID, kind, step string, state json.RawMessage, expiresAt time.Time) (BotDialogRow, error) {
	if len(state) == 0 {
		state = json.RawMessage(`{}`)
	}
	return scanBotDialogRow(q.db.QueryRow(ctx, upsertBotDialog, telegramUserID, orgID, kind, step, state, expiresAt))
}

const getBotDialog = `-- name: GetBotDialog :one
SELECT ` + botDialogColumns + `
FROM   bot_dialogs
WHERE  telegram_user_id = $1
  AND  kind = $2`

// GetBotDialog returns the account's dialog of this kind EVEN WHEN it has
// expired — the caller tells the person so once and then deletes it — or
// pgx.ErrNoRows.
func (q *Queries) GetBotDialog(ctx context.Context, telegramUserID int64, kind string) (BotDialogRow, error) {
	return scanBotDialogRow(q.db.QueryRow(ctx, getBotDialog, telegramUserID, kind))
}

const deleteBotDialog = `-- name: DeleteBotDialog :exec
DELETE FROM bot_dialogs
WHERE  telegram_user_id = $1
  AND  kind = $2`

// DeleteBotDialog ends the account's dialog of this kind; a missing one is
// not an error.
func (q *Queries) DeleteBotDialog(ctx context.Context, telegramUserID int64, kind string) error {
	_, err := q.db.Exec(ctx, deleteBotDialog, telegramUserID, kind)
	return err
}

const deleteExpiredBotDialogs = `-- name: DeleteExpiredBotDialogs :execrows
DELETE FROM bot_dialogs
WHERE  expires_at < $1`

// DeleteExpiredBotDialogs removes every dialog whose expiry is earlier than
// the given instant and reports how many went.
func (q *Queries) DeleteExpiredBotDialogs(ctx context.Context, before time.Time) (int64, error) {
	tag, err := q.db.Exec(ctx, deleteExpiredBotDialogs, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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
	// The person's most relevant invitation of this organization that was not
	// revoked (an accepted one, else the latest): its id, when it was
	// accepted, when it expires and when its letter last went out. All nil
	// when they have none (a member added another way).
	InvitationID         *uuid.UUID `json:"invitation_id"`
	InvitationAcceptedAt *time.Time `json:"invitation_accepted_at"`
	InvitationExpiresAt  *time.Time `json:"invitation_expires_at"`
	InvitationLastSentAt *time.Time `json:"invitation_last_sent_at"`
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
                 AND i.revoked_at IS NULL
                 AND i.accepted_at IS NULL AND i.expires_at > now())         AS invitation_pending,
       inv.id           AS invitation_id,
       inv.accepted_at  AS invitation_accepted_at,
       inv.expires_at   AS invitation_expires_at,
       inv.last_sent_at AS invitation_last_sent_at
FROM   memberships m
JOIN   users u ON u.id = m.user_id
LEFT JOIN LATERAL (
           SELECT i.id, i.accepted_at, i.expires_at, i.last_sent_at
           FROM   bot_invitations i
           WHERE  i.user_id = u.id AND i.org_id = m.org_id AND i.revoked_at IS NULL
           ORDER  BY (i.accepted_at IS NOT NULL) DESC, i.created_at DESC
           LIMIT  1
       ) inv ON true
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
		if err := rows.Scan(&r.UserID, &r.Email, &r.MembershipRole, &r.JoinedAt, &r.TelegramLinked, &r.InvitationPending,
			&r.InvitationID, &r.InvitationAcceptedAt, &r.InvitationExpiresAt, &r.InvitationLastSentAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getBotInvitationInviterLink = `-- name: GetBotInvitationInviterLink :one
SELECT l.telegram_user_id, l.locale
FROM   bot_invitations i
JOIN   bot_telegram_links l ON l.user_id = i.invited_by AND l.revoked_at IS NULL
WHERE  i.user_id = $1
  AND  i.org_id = $2
  AND  i.accepted_telegram_user_id = $3
ORDER  BY i.accepted_at DESC
LIMIT  1`

// BotInviterLink is the Telegram account (and language) of whoever issued an
// accepted invitation.
type BotInviterLink struct {
	TelegramUserID int64  `json:"telegram_user_id"`
	Locale         string `json:"locale"`
}

// GetBotInvitationInviterLink finds the issuer's Telegram account for the
// invitation userID accepted in orgID from acceptedBy. pgx.ErrNoRows when the
// issuer has no active bot link.
func (q *Queries) GetBotInvitationInviterLink(ctx context.Context, userID, orgID uuid.UUID, acceptedBy int64) (BotInviterLink, error) {
	var r BotInviterLink
	err := q.db.QueryRow(ctx, getBotInvitationInviterLink, userID, orgID, acceptedBy).Scan(&r.TelegramUserID, &r.Locale)
	return r, err
}
