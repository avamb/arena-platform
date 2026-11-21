-- org_branding.sql — an organization's theme tokens (migration 0203,
-- spec 62 §12.1). Hand-maintained wrappers live in gen/org_branding.sql.go.

-- name: GetOrganizationBranding :one
-- GetOrganizationBranding returns organizations.branding ('{}' = platform
-- palette) of an active organization.
SELECT branding FROM organizations WHERE id = $1 AND deleted_at IS NULL;

-- name: SetOrganizationBranding :execrows
-- SetOrganizationBranding replaces the whole branding object.
UPDATE organizations SET branding = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: GetOrganizationAccentForTicket :one
-- GetOrganizationAccentForTicket answers branding.primary of the
-- organization that sold a ticket ('' when it has none) — the accent colour
-- of the ticket PDF and letters (spec 62 P-23).
SELECT COALESCE(o.branding->>'primary', '')
FROM tickets t
JOIN sessions s ON s.id = t.session_id
JOIN events e ON e.id = s.event_id
JOIN organizations o ON o.id = e.org_id
WHERE t.id = $1;
