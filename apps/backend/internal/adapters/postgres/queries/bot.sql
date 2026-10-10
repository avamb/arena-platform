-- bot.sql — the Telegram event-center bot's own tables (migration 0115):
-- Telegram account links, one-time invitation codes and wizard drafts.
-- Wrappers are hand-maintained in gen/bot.sql.go.

-- name: InsertBotInvitation :one
INSERT INTO bot_invitations (org_id, user_id, email, role, code_hash, invited_by, expires_at, membership_created)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at;

-- name: GetBotInvitationByCodeHash :one
SELECT id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
       accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at
FROM   bot_invitations
WHERE  code_hash = $1;

-- name: AcceptBotInvitation :one
UPDATE bot_invitations
SET    accepted_at               = now(),
       accepted_telegram_user_id = $2
WHERE  id = $1
  AND  accepted_at IS NULL
  AND  revoked_at IS NULL
  AND  expires_at > now()
RETURNING id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at;

-- name: GetBotInvitationInOrgForUpdate :one
-- Lock an invitation of the organization (EC-16). A missing row and a row of
-- another organization answer alike.
SELECT id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
       accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at
FROM   bot_invitations
WHERE  id = $1 AND org_id = $2
FOR UPDATE;

-- name: GetBotInvitationInOrg :one
SELECT id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
       accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at
FROM   bot_invitations
WHERE  id = $1 AND org_id = $2;

-- name: RevokeBotInvitation :one
UPDATE bot_invitations
SET    revoked_at = now()
WHERE  id = $1
  AND  revoked_at IS NULL
RETURNING id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at;

-- name: CountOtherLiveBotInvitations :one
-- The OTHER invitations that still keep the person in the organization:
-- accepted ones, and unaccepted ones that neither expired nor were revoked.
SELECT count(*)
FROM   bot_invitations
WHERE  user_id = $1
  AND  org_id  = $2
  AND  id <> $3
  AND  revoked_at IS NULL
  AND (accepted_at IS NOT NULL OR expires_at > now());

-- name: BotInvitationCreatedMembership :one
-- Whether ANY invitation of the person to the organization (revoked or not)
-- inserted the membership: a second invitation to someone the first one
-- brought in finds the membership already there and records created=false.
SELECT EXISTS (
    SELECT 1 FROM bot_invitations
    WHERE  user_id = $1 AND org_id = $2 AND membership_created
);

-- name: BotUserWorksInOrg :one
-- Whether the person has already started working in the organization through
-- the bot by another route than accepting THIS invitation.
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
       );

-- name: CountActiveOrgAdmins :one
SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'org_admin' AND status = 'active';

-- name: ResendBotInvitation :one
-- A fresh code and expiry for a not-yet-accepted, not-revoked invitation, only
-- when the last letter went out at least $5 seconds ago (one statement, so two
-- concurrent resends cannot both pass).
UPDATE bot_invitations
SET    code_hash    = $3,
       expires_at   = $4,
       last_sent_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  accepted_at IS NULL
  AND  revoked_at IS NULL
  AND  last_sent_at <= now() - make_interval(secs => $5::int)
RETURNING id, org_id, user_id, email, role, code_hash, invited_by, expires_at,
          accepted_at, accepted_telegram_user_id, created_at, revoked_at, membership_created, last_sent_at;

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
       -- Any answer makes the draft live again, so it earns a fresh
       -- 24-hour reminder once it goes idle anew (migration 0130).
       reminded_at    = NULL,
       updated_at     = now()
RETURNING id, telegram_user_id, org_id, mode, event_id, step, schema_version, state, saved,
          created_at, updated_at;

-- name: DeleteBotDraft :exec
DELETE FROM bot_drafts
WHERE  telegram_user_id = $1
  AND  org_id = $2;

-- name: ListBotDraftsForReminder :many
-- Drafts idle since before $1 whose owner has not been reminded yet, oldest
-- first, with the language to remind them in (migration 0130).
SELECT d.id, d.telegram_user_id, d.org_id, d.mode, d.event_id, d.state, l.locale, d.updated_at
FROM   bot_drafts d
JOIN   bot_telegram_links l ON l.telegram_user_id = d.telegram_user_id
WHERE  d.updated_at < $1
  AND  d.reminded_at IS NULL
ORDER  BY d.updated_at
LIMIT  $2;

-- name: MarkBotDraftReminded :exec
UPDATE bot_drafts
SET    reminded_at = now()
WHERE  id = $1;

-- name: DeleteBotDraftsIdleBefore :many
-- Drafts abandoned since before $1, deleted; the rows come back so their
-- owners can be told the draft is gone.
DELETE FROM bot_drafts d
USING  bot_telegram_links l
WHERE  l.telegram_user_id = d.telegram_user_id
  AND  d.updated_at < $1
RETURNING d.id, d.telegram_user_id, d.org_id, d.mode, d.event_id, d.state, l.locale, d.updated_at;

-- name: UpsertBotDialog :one
-- One live dialog per Telegram account and kind; every save slides the expiry.
INSERT INTO bot_dialogs (telegram_user_id, org_id, kind, step, state, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (telegram_user_id, kind) DO UPDATE
SET    org_id     = EXCLUDED.org_id,
       step       = EXCLUDED.step,
       state      = EXCLUDED.state,
       expires_at = EXCLUDED.expires_at,
       updated_at = now()
RETURNING id, telegram_user_id, org_id, kind, step, state, expires_at, created_at, updated_at;

-- name: GetBotDialog :one
-- The dialog whatever its expiry: the caller reports an expired one once.
SELECT id, telegram_user_id, org_id, kind, step, state, expires_at, created_at, updated_at
FROM   bot_dialogs
WHERE  telegram_user_id = $1
  AND  kind = $2;

-- name: DeleteBotDialog :exec
DELETE FROM bot_dialogs
WHERE  telegram_user_id = $1
  AND  kind = $2;

-- name: DeleteExpiredBotDialogs :execrows
DELETE FROM bot_dialogs
WHERE  expires_at < $1;

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
