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

-- name: GetContactFormTarget :one
-- GetContactFormTarget names the sales-page channel a buyer's message goes
-- through: the hosted-page channel of the organization that owns the feed
-- token ($2), or the newest eligible one when $2 is empty — the same rule as
-- GetHostedPageSeller. has_email reports whether a message has somewhere to
-- go (public e-mail, the organization's contact_email or its owner's account
-- e-mail); no address is selected here.
SELECT sc.id,
       COALESCE(btrim(sc.settings #>> '{hosted_page,public_contact,email}'), '') <> ''
         OR COALESCE(btrim(o.contact_email), '') <> ''
         OR EXISTS (SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
               WHERE m.org_id = o.id AND m.role = 'org_admin' AND m.status = 'active'
                 AND u.deactivated_at IS NULL AND u.anonymized_at IS NULL AND btrim(u.email) <> ''
                 AND NOT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                                 WHERE ur.user_id = u.id AND ur.org_id IS NULL
                                   AND r.org_id IS NULL AND r.name NOT IN ('org_admin', 'organizer', 'agent'))) AS has_email
FROM sales_channels sc
JOIN organizations o ON o.id = sc.org_id
JOIN agent_feed_tokens ft ON ft.sales_channel_id = sc.id
WHERE sc.org_id = $1
  AND sc.deleted_at IS NULL
  AND sc.settings #>> '{hosted_page,enabled}' = 'true'
  AND ft.is_active = true
  AND ($2::text = '' OR ft.token = $2::text)
ORDER BY ft.created_at DESC
LIMIT 1;

-- name: GetContactFormToTelegram :one
SELECT contact_form_to_telegram FROM organizations WHERE id = $1 AND deleted_at IS NULL;

-- name: OrgHasPrivateRecipient :one
-- A boolean only: the hosted page never reads an address. True when the
-- organization has its own contact_email or an owner with an account e-mail.
SELECT COALESCE(btrim(o.contact_email), '') <> ''
    OR EXISTS (SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
               WHERE m.org_id = o.id AND m.role = 'org_admin' AND m.status = 'active'
                 AND u.deactivated_at IS NULL AND u.anonymized_at IS NULL AND btrim(u.email) <> ''
                 AND NOT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                                 WHERE ur.user_id = u.id AND ur.org_id IS NULL
                                   AND r.org_id IS NULL AND r.name NOT IN ('org_admin', 'organizer', 'agent')))
FROM organizations o WHERE o.id = $1 AND o.deleted_at IS NULL;

-- name: SetContactFormToTelegram :execrows
UPDATE organizations SET contact_form_to_telegram = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

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
