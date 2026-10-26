// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: seller_profile.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

const getOrganizationLegalForm = `-- name: GetOrganizationLegalForm :one
SELECT legal_form FROM organizations WHERE id = $1 AND deleted_at IS NULL`

// GetOrganizationLegalForm returns organizations.legal_form (nil = not given)
// of an active organization; pgx.ErrNoRows when there is none.
func (q *Queries) GetOrganizationLegalForm(ctx context.Context, orgID uuid.UUID) (*string, error) {
	var v *string
	err := q.db.QueryRow(ctx, getOrganizationLegalForm, orgID).Scan(&v)
	return v, err
}

const setOrganizationLegalForm = `-- name: SetOrganizationLegalForm :execrows
UPDATE organizations SET legal_form = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`

// SetOrganizationLegalForm stores (or, with nil, clears) the legal form and
// reports whether an active organization was updated.
func (q *Queries) SetOrganizationLegalForm(ctx context.Context, orgID uuid.UUID, legalForm *string) (bool, error) {
	tag, err := q.db.Exec(ctx, setOrganizationLegalForm, orgID, legalForm)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SellerChannelRow is a hosted-page channel with its seller-block settings.
type SellerChannelRow struct {
	ID         uuid.UUID
	Name       string
	ShowSeller bool
	// SellerHidden: an operator hid the block (settings.hosted_page.seller_hidden).
	SellerHidden bool
	// ConsentRequired: the buyer must accept the sale terms at checkout
	// (settings.hosted_page.consent_required, P2).
	ConsentRequired bool
	// PublicContact is settings.hosted_page.public_contact as raw JSON, nil
	// when the channel has none.
	PublicContact []byte
}

const listSellerChannels = `-- name: ListSellerChannels :many
SELECT id, name,
       COALESCE(settings #>> '{hosted_page,show_seller}', '') = 'true' AS show_seller,
       COALESCE(settings #>> '{hosted_page,seller_hidden}', '') = 'true' AS seller_hidden,
       settings #> '{hosted_page,public_contact}' AS public_contact,
       COALESCE(settings #>> '{hosted_page,consent_required}', '') = 'true' AS consent_required
FROM sales_channels
WHERE org_id = $1
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,enabled}' = 'true'
ORDER BY created_at, id`

// ListSellerChannels lists the organization's hosted-page channels, oldest
// first, with their seller-block settings.
func (q *Queries) ListSellerChannels(ctx context.Context, orgID uuid.UUID) ([]SellerChannelRow, error) {
	rows, err := q.db.Query(ctx, listSellerChannels, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SellerChannelRow
	for rows.Next() {
		var r SellerChannelRow
		if err := rows.Scan(&r.ID, &r.Name, &r.ShowSeller, &r.SellerHidden, &r.PublicContact, &r.ConsentRequired); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const setChannelSellerDisplay = `-- name: SetChannelSellerDisplay :execrows
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
  AND settings #>> '{hosted_page,enabled}' = 'true'`

// SetChannelSellerDisplay writes show_seller and public_contact (nil removes
// the key) of a hosted-page channel of the organization and reports whether
// such a channel was updated. Every other settings key is kept.
func (q *Queries) SetChannelSellerDisplay(ctx context.Context, channelID, orgID uuid.UUID, showSeller bool, publicContact []byte) (bool, error) {
	var contact any
	if publicContact != nil {
		contact = string(publicContact)
	}
	tag, err := q.db.Exec(ctx, setChannelSellerDisplay, channelID, orgID, showSeller, contact)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const setChannelSellerHidden = `-- name: SetChannelSellerHidden :execrows
UPDATE sales_channels
SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{hosted_page}',
        CASE WHEN $3::boolean
             THEN COALESCE(settings -> 'hosted_page', '{}'::jsonb) || jsonb_build_object('seller_hidden', true)
             ELSE COALESCE(settings -> 'hosted_page', '{}'::jsonb) - 'seller_hidden'
        END, true),
    updated_at = now()
WHERE id = $1
  AND org_id = $2
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,enabled}' = 'true'`

// SetChannelSellerHidden sets or removes settings.hosted_page.seller_hidden of
// a hosted-page channel of the organization and reports whether such a
// channel was updated. Only a platform superadmin's request reaches it.
func (q *Queries) SetChannelSellerHidden(ctx context.Context, channelID, orgID uuid.UUID, hidden bool) (bool, error) {
	tag, err := q.db.Exec(ctx, setChannelSellerHidden, channelID, orgID, hidden)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const setChannelConsentRequired = `-- name: SetChannelConsentRequired :execrows
UPDATE sales_channels
SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{hosted_page}',
        CASE WHEN $3::boolean
             THEN COALESCE(settings -> 'hosted_page', '{}'::jsonb) || jsonb_build_object('consent_required', true)
             ELSE COALESCE(settings -> 'hosted_page', '{}'::jsonb) - 'consent_required'
        END, true),
    updated_at = now()
WHERE id = $1
  AND org_id = $2
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,enabled}' = 'true'`

// SetChannelConsentRequired sets or removes
// settings.hosted_page.consent_required of a hosted-page channel of the
// organization (P2: the buyer must accept the sale terms at checkout) and
// reports whether such a channel was updated. The seller-display route calls
// it only while the seller profile is complete.
func (q *Queries) SetChannelConsentRequired(ctx context.Context, channelID, orgID uuid.UUID, required bool) (bool, error) {
	tag, err := q.db.Exec(ctx, setChannelConsentRequired, channelID, orgID, required)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ContactFormTarget is the channel a buyer's message goes through. HasEmail
// is true when the message has somewhere to go: the channel's public e-mail,
// else the organization's own contact_email, else its owner's account e-mail
// (private recipients, never shown to the buyer - owner rule 2026-10-13).
type ContactFormTarget struct {
	ChannelID uuid.UUID
	HasEmail  bool
	// OwnerReachable: the organization has an owner whose account e-mail
	// may take the messages (OwnerRecipientWhereSQL). A message from a
	// promoter's page goes to that owner only. A boolean, never the address.
	OwnerReachable bool
}

const getContactFormTarget = `-- name: GetContactFormTarget :one
SELECT sc.id,
       COALESCE(btrim(sc.settings #>> '{hosted_page,public_contact,email}'), '') <> ''
         OR COALESCE(btrim(o.contact_email), '') <> ''
         OR ` + ownerReachableSQL + ` AS has_email,
       ` + ownerReachableSQL + ` AS owner_reachable
FROM sales_channels sc
JOIN organizations o ON o.id = sc.org_id
JOIN agent_feed_tokens ft ON ft.sales_channel_id = sc.id
WHERE sc.org_id = $1
  AND sc.deleted_at IS NULL
  AND sc.settings #>> '{hosted_page,enabled}' = 'true'
  AND ft.is_active = true
  AND ($2::text = '' OR ft.token = $2::text)
ORDER BY ft.created_at DESC
LIMIT 1`

// GetContactFormTarget names the sales-page channel of orgID that a buyer's
// message goes through (the feed token's channel, or the newest eligible one
// for an empty token) and whether it has a public e-mail. pgx.ErrNoRows when
// there is none; a token of another organization finds none.
func (q *Queries) GetContactFormTarget(ctx context.Context, orgID uuid.UUID, feedToken string) (ContactFormTarget, error) {
	var t ContactFormTarget
	err := q.db.QueryRow(ctx, getContactFormTarget, orgID, feedToken).Scan(&t.ChannelID, &t.HasEmail, &t.OwnerReachable)
	return t, err
}

const getContactFormToTelegram = `-- name: GetContactFormToTelegram :one
SELECT contact_form_to_telegram FROM organizations WHERE id = $1 AND deleted_at IS NULL`

// GetContactFormToTelegram reports whether buyers' messages also go to the
// organization's sales chat (migration 0148).
func (q *Queries) GetContactFormToTelegram(ctx context.Context, orgID uuid.UUID) (bool, error) {
	var v bool
	err := q.db.QueryRow(ctx, getContactFormToTelegram, orgID).Scan(&v)
	return v, err
}

// OwnerRecipientWhereSQL is the ONE rule for "the organization's owner whose
// account e-mail may receive the contact form's messages" (membership alias
// m, user alias u, organization alias o): an active org_admin membership, an
// account neither deactivated nor anonymized, a non-blank e-mail, and never a
// person who holds any platform-wide role (a global user_roles row of a role
// other than the membership roles org_admin, organizer, agent: superadmin,
// operator, admin, support and the rest) - the operator is an org_admin of
// organizations it set up, and a buyer's message must reach the organizer,
// not the platform. Every
// query that asks for this recipient builds on it
// (TestOwnerRecipientRule_OneCopyEverywhere); the mirrors in queries/*.sql
// repeat it literally.
const OwnerRecipientWhereSQL = `m.org_id = o.id AND m.role = 'org_admin' AND m.status = 'active'
                 AND u.deactivated_at IS NULL AND u.anonymized_at IS NULL AND btrim(u.email) <> ''
                 AND NOT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                                 WHERE ur.user_id = u.id AND ` + PlatformRoleRowSQL + `)`

// ownerReachableSQL answers whether such an owner exists - a boolean, never
// the address.
const ownerReachableSQL = `EXISTS (SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
               WHERE ` + OwnerRecipientWhereSQL + `)`

// OwnerRecipientSQL selects the account e-mail of that owner (the oldest
// membership first). It is the last private recipient of the contact form
// (owner decision 2026-10-13) and is never published.
const OwnerRecipientSQL = `(SELECT u.email FROM memberships m JOIN users u ON u.id = m.user_id
  WHERE ` + OwnerRecipientWhereSQL + `
  ORDER BY m.joined_at, u.id LIMIT 1)`

const orgHasPrivateRecipient = `-- name: OrgHasPrivateRecipient :one
SELECT COALESCE(btrim(o.contact_email), '') <> ''
    OR ` + ownerReachableSQL + `
FROM organizations o WHERE o.id = $1 AND o.deleted_at IS NULL`

// OrgHasPrivateRecipient reports whether the contact form of a page without a
// public e-mail has somewhere to go: the organization's own contact_email,
// else its owner's account e-mail (OwnerRecipientSQL). It answers a boolean
// only: the hosted page's queries never read an address
// (TestGetHostedPageSellerQuery_NeverReadsOrganizationContact).
func (q *Queries) OrgHasPrivateRecipient(ctx context.Context, orgID uuid.UUID) (bool, error) {
	var v bool
	err := q.db.QueryRow(ctx, orgHasPrivateRecipient, orgID).Scan(&v)
	return v, err
}

// EventPromotedSQL is the ONE rule for "this event belongs to a promoter"
// (event alias e): a link in event_promoters to a promoter of the same
// organization that is not archived. A buyer's message about such an event,
// and every message from a promoter's own page, goes to the organization's
// owner only (owner decision 2026-10-14), never to the promoter's e-mail and
// never to the sales page's public e-mail. The page's form, the send route
// and the letters' form link all decide with it
// (TestEventPromotedRule_OneCopyEverywhere).
const EventPromotedSQL = `EXISTS (SELECT 1 FROM event_promoters epx
               JOIN org_promoters opx ON opx.id = epx.promoter_id AND opx.org_id = epx.org_id
               WHERE epx.event_id = e.id AND epx.org_id = e.org_id AND opx.archived_at IS NULL)`

const getContactEventPromoted = `-- name: GetContactEventPromoted :one
SELECT EXISTS (SELECT 1 FROM events e
               WHERE e.org_id = $1 AND lower(e.slug) = lower($2) AND e.deleted_at IS NULL
                 AND ` + EventPromotedSQL + `)`

// GetContactEventPromoted reports whether the event with this slug in orgID
// belongs to a promoter (EventPromotedSQL). An unknown slug, or one of
// another organization, answers false.
func (q *Queries) GetContactEventPromoted(ctx context.Context, orgID uuid.UUID, eventSlug string) (bool, error) {
	var v bool
	err := q.db.QueryRow(ctx, getContactEventPromoted, orgID, eventSlug).Scan(&v)
	return v, err
}

const orgOwnerReachable = `-- name: OrgOwnerReachable :one
SELECT ` + ownerReachableSQL + `
FROM organizations o WHERE o.id = $1 AND o.deleted_at IS NULL`

// OrgOwnerReachable reports whether the organization has an owner who may
// receive the contact form's messages (OwnerRecipientWhereSQL). A boolean
// only: the hosted page never learns the address.
func (q *Queries) OrgOwnerReachable(ctx context.Context, orgID uuid.UUID) (bool, error) {
	var v bool
	err := q.db.QueryRow(ctx, orgOwnerReachable, orgID).Scan(&v)
	return v, err
}

const setContactFormToTelegram = `-- name: SetContactFormToTelegram :execrows
UPDATE organizations SET contact_form_to_telegram = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL`

// SetContactFormToTelegram switches the Telegram leg of the contact form.
func (q *Queries) SetContactFormToTelegram(ctx context.Context, orgID uuid.UUID, on bool) (bool, error) {
	tag, err := q.db.Exec(ctx, setContactFormToTelegram, orgID, on)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const hideSellerDisplayForOrg = `-- name: HideSellerDisplayForOrg :many
UPDATE sales_channels
SET settings = jsonb_set(settings, '{hosted_page,show_seller}', 'false'::jsonb, true),
    updated_at = now()
WHERE org_id = $1
  AND deleted_at IS NULL
  AND settings #>> '{hosted_page,show_seller}' = 'true'
RETURNING id`

// HideSellerDisplayForOrg turns show_seller off on every channel of the
// organization that has it on and returns their ids.
func (q *Queries) HideSellerDisplayForOrg(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, hideSellerDisplayForOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
