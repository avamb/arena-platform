// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: session_external_refs.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// GetSessionByExternalRef
// ─────────────────────────────────────────────────────────────────────────────

const getSessionByExternalRef = `-- name: GetSessionByExternalRef :one
SELECT ser.session_id, s.event_id
FROM   session_external_refs ser
JOIN   sessions s ON s.id = ser.session_id
WHERE  ser.org_id = $1
  AND  ser.external_ref = $2`

// GetSessionByExternalRef resolves an externalRef inside one organization to
// the session it is bound to, plus that session's event. Returns
// pgx.ErrNoRows when the organization has no such ref — including when the
// ref exists but belongs to a different organization, since the lookup is
// scoped by the org-composite primary key (no cross-tenant leak).
func (q *Queries) GetSessionByExternalRef(ctx context.Context, orgID uuid.UUID, externalRef string) (sessionID, eventID uuid.UUID, err error) {
	row := q.db.QueryRow(ctx, getSessionByExternalRef, orgID, externalRef)
	err = row.Scan(&sessionID, &eventID)
	return sessionID, eventID, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetExternalRefBySession
// ─────────────────────────────────────────────────────────────────────────────

const getExternalRefBySession = `-- name: GetExternalRefBySession :one
SELECT external_ref
FROM   session_external_refs
WHERE  session_id = $1`

// GetExternalRefBySession returns the external reference already bound to a
// session. Returns pgx.ErrNoRows when the session carries no ref, which is
// the normal state for sessions created through the admin UI or the legacy
// Bil24 import route.
func (q *Queries) GetExternalRefBySession(ctx context.Context, sessionID uuid.UUID) (string, error) {
	row := q.db.QueryRow(ctx, getExternalRefBySession, sessionID)
	var externalRef string
	err := row.Scan(&externalRef)
	return externalRef, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertSessionExternalRef
// ─────────────────────────────────────────────────────────────────────────────

const insertSessionExternalRef = `-- name: InsertSessionExternalRef :exec
INSERT INTO session_external_refs (org_id, external_ref, session_id)
VALUES ($1, $2, $3)`

// InsertSessionExternalRef binds an external reference to a session. Call it
// inside the same transaction that creates the session (spec §3.2 step 7).
// Returns a 23505 unique violation when the org already bound the ref to a
// different session (composite primary key) or when the session already has
// a ref (session_external_refs_session_uq); both map to the caller-visible
// import.external_ref_conflict (409).
func (q *Queries) InsertSessionExternalRef(ctx context.Context, orgID uuid.UUID, externalRef string, sessionID uuid.UUID) error {
	_, err := q.db.Exec(ctx, insertSessionExternalRef, orgID, externalRef, sessionID)
	return err
}
