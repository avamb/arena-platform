-- session_external_refs.sql — sqlc query definitions for the arena-native
-- event bundle's externalRef → session mapping (migration 0099,
-- feature #523, event-bundle spec §3.2 / §6).
--
-- The import path resolves a bundle in this order: externalRef first
-- (repeat/edit of a known session), then an explicitly supplied
-- actionEventId. GetExternalRefBySession exists so step 2 can detect that
-- the session already carries a DIFFERENT ref and answer 409
-- import.external_ref_conflict instead of silently rebinding it.
-- InsertSessionExternalRef runs inside the same transaction as the session
-- write (spec §3.2 step 7).

-- name: GetSessionByExternalRef :one
-- Resolves an externalRef inside one organization to its session, and
-- returns that session's event so the bundle can reuse both without a
-- second query. Returns pgx.ErrNoRows when the ref is unknown to the org
-- (a ref belonging to another organization is invisible here — the PK is
-- org-scoped, so cross-tenant lookups simply miss).
SELECT ser.session_id, s.event_id
FROM   session_external_refs ser
JOIN   sessions s ON s.id = ser.session_id
WHERE  ser.org_id = $1
  AND  ser.external_ref = $2;

-- name: GetExternalRefBySession :one
-- Returns the external reference already bound to a session, if any.
-- Returns pgx.ErrNoRows when the session has no ref yet (the normal case
-- for sessions created through the admin UI or the legacy Bil24 import).
SELECT external_ref
FROM   session_external_refs
WHERE  session_id = $1;

-- name: InsertSessionExternalRef :exec
-- Binds an external reference to a session. Violates the composite primary
-- key (23505) when the org already used the ref for another session, and
-- session_external_refs_session_uq when the session already has a ref —
-- both surface to the caller as import.external_ref_conflict.
INSERT INTO session_external_refs (org_id, external_ref, session_id)
VALUES ($1, $2, $3);
