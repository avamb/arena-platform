-- seller_profile.sql — the owner's "Organization data" (HP-4a): the legal
-- form (migration 0146) and the per-channel seller-block settings of the
-- hosted sales page (settings.hosted_page.show_seller / public_contact).
-- Hand-maintained wrappers live in gen/seller_profile.sql.go.

-- name: GetOrganizationLegalForm :one
-- GetOrganizationLegalForm returns organizations.legal_form (NULL = not given)
-- of an active organization.
SELECT legal_form FROM organizations WHERE id = $1 AND deleted_at IS NULL;

-- name: SetOrganizationLegalForm :execrows
-- SetOrganizationLegalForm stores or clears (NULL) the legal form.
UPDATE organizations SET legal_form = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: ListSellerChannels :many
-- ListSellerChannels lists the organization's channels that run a hosted sales
-- page (settings.hosted_page.enabled), oldest first, with the two settings of
-- the seller block. A WordPress site's channel never has the flag, so it is
-- never listed.
SELECT id, name,
       COALESCE(settings #>> '{hosted_page,show_seller}', '') = 'true' AS show_seller,
       settings #> '{hosted_page,public_contact}' AS public_contact
FROM sales_channels
WHERE org_id = $1
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,enabled}' = 'true'
ORDER BY created_at, id;

-- name: SetChannelSellerDisplay :execrows
-- SetChannelSellerDisplay writes settings.hosted_page.show_seller ($3) and
-- settings.hosted_page.public_contact ($4; NULL removes the key) of a
-- hosted-page channel of the organization. Every other key of settings is
-- left as it is.
UPDATE sales_channels
SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{hosted_page}',
        CASE WHEN $4::jsonb IS NULL
             THEN (COALESCE(settings -> 'hosted_page', '{}'::jsonb) - 'public_contact')
                  || jsonb_build_object('show_seller', $3::boolean)
             ELSE COALESCE(settings -> 'hosted_page', '{}'::jsonb)
                  || jsonb_build_object('show_seller', $3::boolean, 'public_contact', $4::jsonb)
        END, true),
    updated_at = now()
WHERE id = $1
  AND org_id = $2
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,enabled}' = 'true';

-- name: HideSellerDisplayForOrg :many
-- HideSellerDisplayForOrg turns settings.hosted_page.show_seller off on every
-- channel of the organization that shows the seller block, and returns their
-- ids. Called in the transaction that empties a required legal field, so a
-- page never shows an incomplete seller block.
UPDATE sales_channels
SET settings = jsonb_set(settings, '{hosted_page,show_seller}', 'false'::jsonb, true),
    updated_at = now()
WHERE org_id = $1
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,show_seller}' = 'true'
RETURNING id;
