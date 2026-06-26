-- operator_networks.sql — sqlc source queries for the Operator Network model
-- (feature #205).
--
-- Schema source: internal/migrations/sql/0043_operator_networks.sql (feature #204).
-- Design context: 09_autoforge/admin_ui/operator_network_design_note.md (#202).
--
-- Three tables are covered:
--   operator_networks       — the network entity (CRUD + list).
--   network_users           — users assigned as network_operator on a network.
--   network_organizations   — organizations attached to a network as either
--                              `organizer` or `agent` (assignment_kind).
--
-- The generated wrappers live in ../gen/operator_networks.sql.go. Regenerate
-- with: make sqlc-generate.

-- ─── operator_networks: CRUD ─────────────────────────────────────────────────

-- name: InsertOperatorNetwork :one
-- Creates a new operator_network. Status defaults to 'active'.
-- Callers must handle 23505 on the partial UNIQUE index over (slug) WHERE
-- archived_at IS NULL when the slug is already used by an active network.
INSERT INTO operator_networks (name, slug)
VALUES ($1, $2)
RETURNING id, name, slug, status, archived_at, created_at, updated_at;

-- name: GetOperatorNetworkByID :one
-- Returns a network by id regardless of archive state. Callers that want
-- only live networks should also check archived_at IS NULL.
SELECT id, name, slug, status, archived_at, created_at, updated_at
FROM   operator_networks
WHERE  id = $1;

-- name: GetOperatorNetworkBySlug :one
-- Returns the active (non-archived) network for a slug. Archived networks
-- are excluded so slugs can be reused.
SELECT id, name, slug, status, archived_at, created_at, updated_at
FROM   operator_networks
WHERE  slug = $1
  AND  archived_at IS NULL;

-- name: ListOperatorNetworks :many
-- Lists all non-archived networks, newest first.
SELECT id, name, slug, status, archived_at, created_at, updated_at
FROM   operator_networks
WHERE  archived_at IS NULL
ORDER  BY created_at DESC, id DESC;

-- name: UpdateOperatorNetwork :one
-- Updates the editable fields. Empty strings leave fields unchanged.
-- Callers must handle 23505 on the partial UNIQUE slug index.
UPDATE operator_networks
SET    name       = COALESCE(NULLIF($2, ''), name),
       slug       = COALESCE(NULLIF($3, ''), slug),
       updated_at = now()
WHERE  id = $1
  AND  archived_at IS NULL
RETURNING id, name, slug, status, archived_at, created_at, updated_at;

-- name: SetOperatorNetworkStatus :one
-- Sets status to one of ('active','suspended'). Use ArchiveOperatorNetwork to
-- transition to 'archived' (which also sets archived_at).
UPDATE operator_networks
SET    status     = $2,
       updated_at = now()
WHERE  id         = $1
  AND  archived_at IS NULL
  AND  $2 IN ('active', 'suspended')
RETURNING id, name, slug, status, archived_at, created_at, updated_at;

-- name: ArchiveOperatorNetwork :one
-- Soft-archives a network (status='archived', archived_at=now()). Idempotent:
-- calling on an already-archived network returns no rows.
UPDATE operator_networks
SET    status      = 'archived',
       archived_at = now(),
       updated_at  = now()
WHERE  id          = $1
  AND  archived_at IS NULL
RETURNING id, name, slug, status, archived_at, created_at, updated_at;

-- ─── network_users: CRUD + listing ───────────────────────────────────────────

-- name: InsertNetworkUser :one
-- Assigns a user as network_operator on a network. Callers must handle
-- 23505 on the (network_id, user_id, role) unique constraint.
INSERT INTO network_users (network_id, user_id)
VALUES ($1, $2)
RETURNING id, network_id, user_id, role, status, created_at, updated_at;

-- name: GetNetworkUser :one
-- Returns the assignment row for (network_id, user_id) regardless of status.
SELECT id, network_id, user_id, role, status, created_at, updated_at
FROM   network_users
WHERE  network_id = $1
  AND  user_id    = $2;

-- name: SetNetworkUserStatus :one
-- Updates the lifecycle status of a network_users row.
UPDATE network_users
SET    status     = $3,
       updated_at = now()
WHERE  network_id = $1
  AND  user_id    = $2
  AND  $3 IN ('active', 'suspended', 'revoked')
RETURNING id, network_id, user_id, role, status, created_at, updated_at;

-- name: DeleteNetworkUser :exec
-- Hard-deletes a network_users row (network_id, user_id). Prefer
-- SetNetworkUserStatus('revoked') for auditable changes.
DELETE FROM network_users
WHERE  network_id = $1
  AND  user_id    = $2;

-- name: ListNetworkUsersByNetwork :many
-- Returns active users assigned to a network, oldest first.
SELECT id, network_id, user_id, role, status, created_at, updated_at
FROM   network_users
WHERE  network_id = $1
  AND  status     = 'active'
ORDER  BY created_at ASC, id ASC;

-- name: ListNetworksByUser :many
-- Returns all non-archived operator_networks where the user holds an active
-- network_users row. Used by the middleware to enumerate networks visible to
-- the calling user.
SELECT n.id, n.name, n.slug, n.status, n.archived_at, n.created_at, n.updated_at
FROM   operator_networks n
JOIN   network_users     nu ON nu.network_id = n.id
WHERE  nu.user_id     = $1
  AND  nu.status      = 'active'
  AND  n.archived_at  IS NULL
ORDER  BY n.created_at DESC, n.id DESC;

-- ─── network_organizations: CRUD + listing ───────────────────────────────────

-- name: InsertNetworkOrganization :one
-- Attaches an organization to a network as either 'organizer' or 'agent'.
-- Callers must handle 23505 on the
-- (network_id, organization_id, assignment_kind) unique constraint.
INSERT INTO network_organizations (network_id, organization_id, assignment_kind)
VALUES ($1, $2, $3)
RETURNING id, network_id, organization_id, assignment_kind, status,
          attached_at, created_at, updated_at;

-- name: GetNetworkOrganization :one
-- Returns the attachment row for (network_id, organization_id, assignment_kind).
SELECT id, network_id, organization_id, assignment_kind, status,
       attached_at, created_at, updated_at
FROM   network_organizations
WHERE  network_id      = $1
  AND  organization_id = $2
  AND  assignment_kind = $3;

-- name: SetNetworkOrganizationStatus :one
-- Updates the lifecycle status of a network_organizations row.
UPDATE network_organizations
SET    status     = $4,
       updated_at = now()
WHERE  network_id      = $1
  AND  organization_id = $2
  AND  assignment_kind = $3
  AND  $4 IN ('active', 'suspended', 'revoked')
RETURNING id, network_id, organization_id, assignment_kind, status,
          attached_at, created_at, updated_at;

-- name: DeleteNetworkOrganization :exec
-- Hard-deletes an attachment row. Prefer SetNetworkOrganizationStatus
-- ('revoked') for auditable changes.
DELETE FROM network_organizations
WHERE  network_id      = $1
  AND  organization_id = $2
  AND  assignment_kind = $3;

-- name: ListNetworkOrganizationsByNetwork :many
-- Returns all active organization attachments for a network. Pass NULL for
-- assignmentKind to return both organizers and agents; otherwise filter to
-- one kind.
SELECT id, network_id, organization_id, assignment_kind, status,
       attached_at, created_at, updated_at
FROM   network_organizations
WHERE  network_id = $1
  AND  status     = 'active'
  AND  ($2::text IS NULL OR assignment_kind = $2)
ORDER  BY attached_at ASC, id ASC;

-- name: ListOrganizersByNetwork :many
-- Convenience: active organizer attachments for a network.
SELECT id, network_id, organization_id, assignment_kind, status,
       attached_at, created_at, updated_at
FROM   network_organizations
WHERE  network_id      = $1
  AND  assignment_kind = 'organizer'
  AND  status          = 'active'
ORDER  BY attached_at ASC, id ASC;

-- name: ListAgentsByNetwork :many
-- Convenience: active agent attachments for a network.
SELECT id, network_id, organization_id, assignment_kind, status,
       attached_at, created_at, updated_at
FROM   network_organizations
WHERE  network_id      = $1
  AND  assignment_kind = 'agent'
  AND  status          = 'active'
ORDER  BY attached_at ASC, id ASC;

-- name: ListNetworksByOrganization :many
-- Returns all non-archived networks an organization is attached to. Pass NULL
-- for assignmentKind to return attachments of any kind; otherwise filter to
-- 'organizer' or 'agent'. Used by reporting and by the middleware when
-- resolving network scope for an org-scoped request.
SELECT n.id, n.name, n.slug, n.status, n.archived_at, n.created_at, n.updated_at,
       no.assignment_kind
FROM   operator_networks         n
JOIN   network_organizations     no ON no.network_id = n.id
WHERE  no.organization_id = $1
  AND  no.status          = 'active'
  AND  n.archived_at      IS NULL
  AND  ($2::text IS NULL OR no.assignment_kind = $2)
ORDER  BY n.created_at DESC, n.id DESC;
