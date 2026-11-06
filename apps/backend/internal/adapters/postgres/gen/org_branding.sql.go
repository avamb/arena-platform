// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: org_branding.sql

package gen

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

const getOrganizationBranding = `-- name: GetOrganizationBranding :one
SELECT branding FROM organizations WHERE id = $1 AND deleted_at IS NULL`

// GetOrganizationBranding returns organizations.branding (migration 0204) of
// an active organization as a flat key -> value map; an empty map means the
// platform palette. pgx.ErrNoRows when there is no such organization.
func (q *Queries) GetOrganizationBranding(ctx context.Context, orgID uuid.UUID) (map[string]string, error) {
	var raw []byte
	if err := q.db.QueryRow(ctx, getOrganizationBranding, orgID).Scan(&raw); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(raw) == 0 {
		return out, nil
	}
	// A value that is not a string (never written by the API) is skipped
	// rather than failing the whole read.
	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		return out, nil
	}
	for k, v := range loose {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}

const setOrganizationBranding = `-- name: SetOrganizationBranding :execrows
UPDATE organizations SET branding = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`

// SetOrganizationBranding replaces the whole branding object (nil or empty =
// back to the platform palette) and reports whether an active organization
// was updated.
func (q *Queries) SetOrganizationBranding(ctx context.Context, orgID uuid.UUID, branding map[string]string) (bool, error) {
	if branding == nil {
		branding = map[string]string{}
	}
	raw, err := json.Marshal(branding)
	if err != nil {
		return false, err
	}
	tag, err := q.db.Exec(ctx, setOrganizationBranding, orgID, raw)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const getOrganizationAccentForTicket = `-- name: GetOrganizationAccentForTicket :one
SELECT COALESCE(o.branding->>'primary', '')
FROM tickets t
JOIN sessions s ON s.id = t.session_id
JOIN events e ON e.id = s.event_id
JOIN organizations o ON o.id = e.org_id
WHERE t.id = $1`

// GetOrganizationAccentForTicket answers branding.primary of the
// organization that sold the ticket ("" when it has none), the accent colour
// of the ticket PDF and letters (spec 62 P-23). pgx.ErrNoRows when the
// ticket does not exist.
func (q *Queries) GetOrganizationAccentForTicket(ctx context.Context, ticketID uuid.UUID) (string, error) {
	var v string
	err := q.db.QueryRow(ctx, getOrganizationAccentForTicket, ticketID).Scan(&v)
	return v, err
}
