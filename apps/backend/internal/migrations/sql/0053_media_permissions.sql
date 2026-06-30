-- +goose Up
-- =====================================================================
-- arena_new — Media RBAC permissions (Wave G — feature #286, G-2)
--
-- Seeds three permissions that gate the /v1/media endpoints introduced in
-- feature #286:
--   * media.write  — POST /v1/media (multipart upload)
--   * media.read   — GET  /v1/media/{id} (metadata + signed URL)
--   * media.delete — DELETE /v1/media/{id} (soft-delete)
--
-- Grant all three to the platform `admin` role and to the tenant
-- `org_admin` role. Other roles can be granted media.* permissions by
-- follow-up migrations as new media-aware surfaces appear.
-- =====================================================================

INSERT INTO permissions (name, description) VALUES
    ('media.write',  'Upload a binary media object (org logos, event posters, artist photos)'),
    ('media.read',   'Read media metadata and obtain a signed download URL'),
    ('media.delete', 'Soft-delete a media object so the GC worker reclaims its bytes')
ON CONFLICT DO NOTHING;

-- Grant all media permissions to the platform admin role.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('media.write', 'media.read', 'media.delete')
ON CONFLICT DO NOTHING;

-- Grant all media permissions to org_admin (tenant admin manages own media).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('media.write', 'media.read', 'media.delete')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name IN ('media.write', 'media.read', 'media.delete')
);
DELETE FROM permissions
WHERE name IN ('media.write', 'media.read', 'media.delete');
