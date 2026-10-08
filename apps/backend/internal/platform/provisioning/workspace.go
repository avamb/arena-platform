// Package provisioning creates the rows a new organizer needs to start
// selling: the organization, its legal fields, a direct-merchant sales channel
// with a public sales page, the owner account and — when the applicant came
// from Telegram — the bot link.
//
// It runs on a caller-supplied transaction so an approval either leaves a
// complete workspace or nothing (08_architecture/34_onboarding_applications_ru.md §6).
// It never turns on merchant_of_record, never sets kyb_status to anything but
// the default and never stores payment-provider keys: the owner enters those
// after signing in.
package provisioning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// OwnerRole is the membership role of the person who runs the organization.
const OwnerRole = "org_admin"

// passwordSetupTTL is how long the owner's set-your-password link lives.
const passwordSetupTTL = 7 * 24 * time.Hour

// passwordSetupEmailMaxAttempts matches the self-service password-reset job.
const passwordSetupEmailMaxAttempts = 5

// ErrDuplicate means an organization with that name (or slug) already exists.
var ErrDuplicate = errors.New("provisioning: an organization with that name already exists")

// maxSlugAttempts bounds the search for a free slug.
const maxSlugAttempts = 50

// WorkspaceInput is everything CreateWorkspace needs.
type WorkspaceInput struct {
	OrgName string
	// LegalName and the fields below are copied onto the organization; empty
	// strings are stored as NULL.
	LegalName          string
	Country            string
	Locale             string
	TaxID              string
	TaxIDScheme        string // the form's scheme: vat, ico, ein, other
	RegistrationNumber string
	AddressLine1       string
	AddressPostalCode  string
	AddressCity        string
	AddressCountry     string
	Website            string
	ContactEmail       string
	ContactPhone       string

	OwnerEmail     string
	OwnerFirstName string
	OwnerLastName  string

	// PaymentProvider is the form's choice; only stripe and flitt are
	// provider values, anything else starts on stripe and the owner changes it.
	PaymentProvider string
	// FeePercent is the channel's service charge, "0.00" when empty.
	FeePercent string

	// TelegramUserID, when set, links the owner's Telegram account to the bot.
	TelegramUserID   *int64
	TelegramUsername *string

	Now time.Time
}

// Workspace is what was created.
type Workspace struct {
	OrgID        uuid.UUID
	Slug         string
	ChannelID    uuid.UUID
	OwnerID      uuid.UUID
	OwnerCreated bool
}

// CreateWorkspace creates the workspace on tx. The caller commits.
func CreateWorkspace(ctx context.Context, tx pgx.Tx, in WorkspaceInput) (Workspace, error) {
	var ws Workspace
	q := gen.New(tx)
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	if strings.TrimSpace(in.OrgName) == "" {
		return ws, errors.New("provisioning: organization name is required")
	}
	email, err := users.NormalizeEmail(in.OwnerEmail)
	if err != nil {
		return ws, fmt.Errorf("provisioning: owner e-mail: %w", err)
	}
	locale := in.Locale
	if locale == "" {
		locale = "en"
	}

	slug, err := freeSlug(ctx, q, in.OrgName)
	if err != nil {
		return ws, err
	}
	org, err := q.InsertOrganization(ctx, in.OrgName, slug, in.Country, locale, 1200)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ws, ErrDuplicate
		}
		return ws, fmt.Errorf("provisioning: insert organization: %w", err)
	}
	ws.OrgID, ws.Slug = org.ID, org.Slug

	if _, err := tx.Exec(ctx, `
UPDATE organizations SET
    legal_name = $2, tax_id = $3, tax_id_scheme = $4, registration_number = $5,
    legal_address_line1 = $6, legal_address_postal_code = $7, legal_address_city = $8,
    legal_address_country = $9, contact_email = $10, contact_phone = $11, website_url = $12,
    updated_at = now()
WHERE id = $1`,
		org.ID, nullable(in.LegalName), nullable(in.TaxID), nullable(OrgTaxScheme(in.TaxIDScheme, in.Country)),
		nullable(in.RegistrationNumber), nullable(in.AddressLine1), nullable(in.AddressPostalCode),
		nullable(in.AddressCity), nullable(in.AddressCountry), nullable(in.ContactEmail),
		nullable(in.ContactPhone), nullable(in.Website),
	); err != nil {
		return ws, fmt.Errorf("provisioning: set legal fields: %w", err)
	}

	provider := "stripe"
	if in.PaymentProvider == "flitt" {
		provider = "flitt"
	}
	fee := in.FeePercent
	if fee == "" {
		fee = "0.00"
	}
	channel, err := q.InsertSalesChannel(ctx, org.ID, "Website", "direct_merchant", provider, nil, fee, nil, []byte(`{}`))
	if err != nil {
		return ws, fmt.Errorf("provisioning: insert sales channel: %w", err)
	}
	ws.ChannelID = channel.ID
	if _, err := q.EnableChannelHostedPage(ctx, channel.ID, org.ID); err != nil {
		return ws, fmt.Errorf("provisioning: enable sales page: %w", err)
	}

	if err := ensureOwner(ctx, tx, q, &ws, email, in, locale, org.Name); err != nil {
		return ws, err
	}
	if in.TelegramUserID != nil {
		orgID := org.ID
		if _, err := q.UpsertBotTelegramLink(ctx, *in.TelegramUserID, ws.OwnerID, in.TelegramUsername, botLocale(locale), &orgID); err != nil {
			return ws, fmt.Errorf("provisioning: link bot: %w", err)
		}
	}
	return ws, nil
}

// ensureOwner finds or creates the owner, gives them the owner membership and,
// for a brand-new account, queues the set-your-password e-mail.
func ensureOwner(ctx context.Context, tx pgx.Tx, q *gen.Queries, ws *Workspace, email string, in WorkspaceInput, locale, orgName string) error {
	existing, err := q.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		if existing.DeactivatedAt != nil {
			return errors.New("provisioning: the owner's account is deactivated")
		}
		ws.OwnerID = existing.ID
	case errors.Is(err, pgx.ErrNoRows):
		secret, tokenErr := users.GenerateVerificationToken()
		if tokenErr != nil {
			return fmt.Errorf("provisioning: account secret: %w", tokenErr)
		}
		hash, hashErr := users.HashPassword(secret)
		if hashErr != nil {
			return fmt.Errorf("provisioning: hash account secret: %w", hashErr)
		}
		created, insErr := q.InsertUser(ctx, email, hash, botLocale(locale))
		if insErr != nil {
			return fmt.Errorf("provisioning: insert owner: %w", insErr)
		}
		ws.OwnerID, ws.OwnerCreated = created.ID, true
		first, last := nullable(in.OwnerFirstName), nullable(in.OwnerLastName)
		if first != nil || last != nil {
			if err := q.SetUserName(ctx, created.ID, first, last); err != nil {
				return fmt.Errorf("provisioning: set owner name: %w", err)
			}
		}
		setupToken, tokErr := users.GenerateVerificationToken()
		if tokErr != nil {
			return fmt.Errorf("provisioning: setup token: %w", tokErr)
		}
		expires := in.Now.Add(passwordSetupTTL)
		if err := q.InsertPasswordResetToken(ctx, users.TokenHash(setupToken), created.ID, expires); err != nil {
			return fmt.Errorf("provisioning: store setup token: %w", err)
		}
		if _, err := worker.EnqueueInTx(ctx, tx, authemail.JobTypePasswordResetEmail, authemail.PasswordResetEmailPayload{
			UserID:    created.ID.String(),
			Email:     email,
			Token:     setupToken,
			ExpiresAt: expires,
			Purpose:   authemail.PurposeOrgInvitation,
			OrgName:   orgName,
		}, passwordSetupEmailMaxAttempts); err != nil {
			return fmt.Errorf("provisioning: queue setup e-mail: %w", err)
		}
	default:
		return fmt.Errorf("provisioning: look up owner: %w", err)
	}
	if _, err := q.InsertMembership(ctx, ws.OwnerID, ws.OrgID, OwnerRole); err != nil {
		return fmt.Errorf("provisioning: grant owner membership: %w", err)
	}
	return nil
}

// freeSlug derives a slug from the name and appends -2, -3, … until it is free
// in the namespace organizations and promoters share.
func freeSlug(ctx context.Context, q *gen.Queries, name string) (string, error) {
	base := geoslug.Slugify(name)
	if base == "" {
		base = "organizer"
	}
	for i := 1; i <= maxSlugAttempts; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		taken, err := q.PromoterSlugTaken(ctx, slug)
		if err != nil {
			return "", fmt.Errorf("provisioning: check slug: %w", err)
		}
		if !taken {
			return slug, nil
		}
	}
	return "", errors.New("provisioning: no free slug")
}

// OrgTaxScheme maps the form's tax-number type onto the values the
// organizations table accepts (eu_vat, gb_vat, il_vat, us_ein, other).
func OrgTaxScheme(formScheme, country string) string {
	switch formScheme {
	case "vat":
		switch strings.ToUpper(country) {
		case "GB":
			return "gb_vat"
		case "IL":
			return "il_vat"
		}
		return "eu_vat"
	case "ein":
		return "us_ein"
	case "":
		return ""
	}
	return "other"
}

func botLocale(l string) string {
	switch l {
	case "ru", "es":
		return l
	}
	return "en"
}

func nullable(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
