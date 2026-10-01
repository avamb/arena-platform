-- bot.sql — the Telegram event-center bot's own tables (migration 0115):
-- Telegram account links, one-time invitation codes and wizard drafts.
-- Wrappers are hand-maintained in gen/bot.sql.go.

-- name: InsertBotInvitation :one
INSERT INTO bot_invitations (org_id, user_id, email, role, code_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at;

-- name: GetBotInvitationByCodeHash :one
SELECT id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
       accepted_at, accepted_telegram_user_id, created_at
FROM   bot_invitations
WHERE  code_hash = $1;

-- name: AcceptBotInvitation :one
UPDATE bot_invitations
SET    accepted_at               = now(),
       accepted_telegram_user_id = $2
WHERE  id = $1
  AND  accepted_at IS NULL
  AND  expires_at > now()
RETURNING id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at;

-- name: GetBotTelegramLink :one
SELECT telegram_user_id, user_id, telegram_username, locale, defaults, current_org_id,
       created_at, updated_at, revoked_at
FROM   bot_telegram_links
WHERE  telegram_user_id = $1;

-- name: UpsertBotTelegramLink :one
INSERT INTO bot_telegram_links (telegram_user_id, user_id, telegram_username, locale, current_org_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (telegram_user_id) DO UPDATE
SET    user_id           = EXCLUDED.user_id,
       telegram_username = EXCLUDED.telegram_username,
       locale            = EXCLUDED.locale,
       current_org_id    = EXCLUDED.current_org_id,
       revoked_at        = NULL,
       updated_at        = now()
RETURNING telegram_user_id, user_id, telegram_username, locale, defaults, current_org_id,
          created_at, updated_at, revoked_at;

-- name: UpdateBotTelegramLinkLocale :exec
UPDATE bot_telegram_links
SET    locale = $2, updated_at = now()
WHERE  telegram_user_id = $1;

-- name: UpdateBotTelegramLinkDefaults :exec
UPDATE bot_telegram_links
SET    defaults = $2, updated_at = now()
WHERE  telegram_user_id = $1;

-- name: SetBotTelegramLinkCurrentOrg :exec
UPDATE bot_telegram_links
SET    current_org_id = $2, updated_at = now()
WHERE  telegram_user_id = $1;

-- name: RevokeBotTelegramLink :exec
UPDATE bot_telegram_links
SET    revoked_at = now(), updated_at = now()
WHERE  telegram_user_id = $1;

-- name: GetBotDraft :one
SELECT id, telegram_user_id, org_id, mode, event_id, step, schema_version, state, saved,
       created_at, updated_at
FROM   bot_drafts
WHERE  telegram_user_id = $1
  AND  org_id = $2;

-- name: UpsertBotDraft :one
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
RETURNING id, telegram_user_id, org_id, mode, event_id, step, schema_version, state, saved,
          created_at, updated_at;

-- name: DeleteBotDraft :exec
DELETE FROM bot_drafts
WHERE  telegram_user_id = $1
  AND  org_id = $2;

-- name: ListBotTeam :many
-- The organization's team as the bot shows it: owners first, then managers,
-- each with whether a Telegram account is linked and whether an invitation
-- is still waiting to be opened.
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
ORDER  BY (m.role = 'org_admin') DESC, u.email;

-- name: GetBotInvitationInviterLink :one
-- The Telegram account of whoever issued the invitation that this Telegram
-- account just accepted, so the bot can tell them the person has joined. No
-- row when the invitation was issued without a bot-linked user (the
-- superadmin's API call, a person who never opened the bot).
SELECT l.telegram_user_id, l.locale
FROM   bot_invitations i
JOIN   bot_telegram_links l ON l.user_id = i.invited_by AND l.revoked_at IS NULL
WHERE  i.user_id = $1
  AND  i.org_id = $2
  AND  i.accepted_telegram_user_id = $3
ORDER  BY i.accepted_at DESC
LIMIT  1;
