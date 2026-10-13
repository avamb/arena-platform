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
	// PublicContact is settings.hosted_page.public_contact as raw JSON, nil
	// when the channel has none.
	PublicContact []byte
}

const listSellerChannels = `-- name: ListSellerChannels :many
SELECT id, name,
       COALESCE(settings #>> '{hosted_page,show_seller}', '') = 'true' AS show_seller,
       settings #> '{hosted_page,public_contact}' AS public_contact
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
		if err := rows.Scan(&r.ID, &r.Name, &r.ShowSeller, &r.PublicContact); err != nil {
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

// ContactFormTarget is the channel a buyer's message goes through.
type ContactFormTarget struct {
	ChannelID uuid.UUID
	HasEmail  bool
}

const getContactFormTarget = `-- name: GetContactFormTarget :one
SELECT sc.id,
       COALESCE(sc.settings #>> '{hosted_page,public_contact,email}', '') <> '' AS has_email
FROM sales_channels sc
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
	err := q.db.QueryRow(ctx, getContactFormTarget, orgID, feedToken).Scan(&t.ChannelID, &t.HasEmail)
	return t, err
}

const getContactFormToTelegram = `-- name: GetContactFormToTelegram :one
SELECT contact_form_to_telegram FROM organizations WHERE id = $1 AND deleted_at IS NULL`

// GetContactFormToTelegram reports whether buyers' messages also go to the
// organization's sales chat (migration 0147).
func (q *Queries) GetContactFormToTelegram(ctx context.Context, orgID uuid.UUID) (bool, error) {
	var v bool
	err := q.db.QueryRow(ctx, getContactFormToTelegram, orgID).Scan(&v)
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
