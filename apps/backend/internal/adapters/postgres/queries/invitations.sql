-- Staff invitations with seats on the plan (spec 46, task INV-01).
-- The hidden channel "Invitations" (sales_channels.kind = 'internal') that
-- every staff invitation order is written on. At most one per organization
-- (partial unique index sales_channels_one_internal_per_org_uq, migration
-- 0175); created lazily by the first invitation.

-- name: InsertInternalChannel :one
-- Race-safe create: a concurrent first invitation loses on the partial
-- unique index and gets no row back; the caller then reads the winner with
-- GetInternalChannelID. No provider account, no gateway credential, no
-- hosted page, fee 0; provider keeps the column default.
INSERT INTO sales_channels (org_id, name, kind, fee_percent, settings)
VALUES ($1, $2, 'internal', 0.00, '{}'::jsonb)
ON CONFLICT (org_id) WHERE kind = 'internal' DO NOTHING
RETURNING id;

-- name: GetInternalChannelID :one
SELECT id
FROM   sales_channels
WHERE  org_id = $1
  AND  kind = 'internal';
