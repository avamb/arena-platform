// public_page_seller.go builds the SELLER and PROMOTER blocks of the hosted
// sales page. A buyer is entitled to know who sells the ticket, so a channel
// can switch the blocks on with settings.hosted_page.show_seller. Off is the
// default: deploying this changes nothing a visitor sees until an operator
// turns it on, channel by channel.
//
// Every value comes from the page's OWN organization. A contact is published
// only from settings.hosted_page.public_contact of the channel; the
// organization's contact_email / contact_phone are never read, because they
// may hold a person's private login address. Since the contact form (HP
// contact form, 2026-10-13) not even the channel's public e-mail is
// published: buyers write through the form (contact_form), the address only
// receives the messages. The public phone is still printed.
package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/withdrawal"
)

// hostedPageSellerAddress is the seller's registered address. Empty parts
// are omitted.
type hostedPageSellerAddress struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

// hostedPageSellerContact is the contact the operator chose to publish for a
// channel. Unset keys are omitted.
type hostedPageSellerContact struct {
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// hostedPageSellerResponse is the legal identity of the seller of record.
type hostedPageSellerResponse struct {
	LegalName          string                   `json:"legal_name"`
	LegalForm          string                   `json:"legal_form,omitempty"`
	TaxID              string                   `json:"tax_id,omitempty"`
	TaxScheme          string                   `json:"tax_scheme,omitempty"`
	RegistrationNumber string                   `json:"registration_number,omitempty"`
	Address            *hostedPageSellerAddress `json:"address,omitempty"`
	Country            string                   `json:"country,omitempty"`
	PublicContact      *hostedPageSellerContact `json:"public_contact,omitempty"`
}

// hostedPagePromoterResponse names the promoter an event is shown under.
type hostedPagePromoterResponse struct {
	Name string `json:"name"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// buildSeller turns the stored row into the public block. It returns nil when
// the channel has not switched the block on.
func buildSeller(row gen.HostedPageSellerRow) *hostedPageSellerResponse {
	if !row.ShowSeller {
		return nil
	}
	s := &hostedPageSellerResponse{
		LegalName:          deref(row.LegalName),
		LegalForm:          deref(row.LegalForm),
		TaxID:              deref(row.TaxID),
		TaxScheme:          deref(row.TaxIDScheme),
		RegistrationNumber: deref(row.RegistrationNumber),
	}
	if s.LegalName == "" {
		s.LegalName = strings.TrimSpace(row.OrgName)
	}
	country := strings.ToUpper(deref(row.AddressCountry))
	if country == "" {
		country = strings.ToUpper(strings.TrimSpace(row.OrgCountry))
	}
	s.Country = country
	addr := hostedPageSellerAddress{
		Line1:      deref(row.AddressLine1),
		Line2:      deref(row.AddressLine2),
		City:       deref(row.AddressCity),
		PostalCode: deref(row.AddressPostalCode),
	}
	if addr.Line1 != "" || addr.Line2 != "" || addr.City != "" || addr.PostalCode != "" {
		addr.Country = country
		s.Address = &addr
	}
	s.PublicContact = publishedContact(row.PublicContact)
	return s
}

// publishedContact is the part of public_contact the page may print: the
// phone. The e-mail is never published; it only receives the contact form's
// messages.
func publishedContact(raw []byte) *hostedPageSellerContact {
	c := parsePublicContact(raw)
	if c == nil || c.Phone == "" {
		return nil
	}
	return &hostedPageSellerContact{Phone: c.Phone}
}

// hostedPageContactForm tells the page whether to offer the "Write to the
// organizer" form, with which Turnstile site key and which privacy policy the
// consent links to. It never carries the address the messages go to.
type hostedPageContactForm struct {
	Available        bool   `json:"available"`
	TurnstileSiteKey string `json:"turnstile_site_key,omitempty"`
	PrivacyURL       string `json:"privacy_url,omitempty"`
}

// contactForm decides the form from the channel row: offered when a message
// has somewhere to go - the channel's public e-mail, else the organization's
// own address, which is never answered (owner rule 2026-10-13: letters of a
// hosted page without a public e-mail link this form) - and this deployment
// can check the captcha. orgReachable is asked only when the channel has no
// public e-mail; it answers a boolean from its own query, so the page's
// seller query never touches the organization's address.
func (h *Handler) contactForm(row gen.HostedPageSellerRow, orgReachable func() bool) *hostedPageContactForm {
	if !h.contactFormReady {
		return nil
	}
	c := parsePublicContact(row.PublicContact)
	if (c == nil || c.Email == "") && (orgReachable == nil || !orgReachable()) {
		return nil
	}
	return &hostedPageContactForm{Available: true, TurnstileSiteKey: h.contactFormSiteKey, PrivacyURL: h.contactFormPrivacyURL}
}

// parsePublicContact reads settings.hosted_page.public_contact. Anything that
// is not an object of two optional strings yields nil.
func parsePublicContact(raw []byte) *hostedPageSellerContact {
	if len(raw) == 0 {
		return nil
	}
	var in struct {
		Email any `json:"email"`
		Phone any `json:"phone"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil
	}
	str := func(v any) string {
		s, _ := v.(string)
		return strings.TrimSpace(s)
	}
	c := hostedPageSellerContact{Email: str(in.Email), Phone: str(in.Phone)}
	if c.Email == "" && c.Phone == "" {
		return nil
	}
	return &c
}

// sellerBlocks resolves the seller block of orgID through the channel that
// owns feedToken (empty = the organization's newest eligible channel) and,
// when eventID is set, the promoter the event is linked to. Both are nil when
// the channel keeps the block off. A lookup failure is logged and answers
// nil: the block is informational and must not turn a resolved page into a
// 500.
func (h *Handler) sellerBlocks(ctx context.Context, orgID uuid.UUID, feedToken string, eventID *uuid.UUID) (*hostedPageSellerResponse, *hostedPagePromoterResponse, *hostedPageContactForm) {
	row, err := h.publicFeedQueries.GetHostedPageSeller(ctx, orgID, feedToken)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public_page: seller lookup failed",
				slog.String("org_id", orgID.String()),
				slog.String("error", err.Error()),
			)
		}
		return nil, nil, nil
	}
	form := h.contactForm(row, func() bool {
		ok, err := h.publicFeedQueries.OrgHasContactEmail(ctx, orgID)
		if err != nil {
			h.logger.Warn("public_page: contact form recipient lookup failed",
				slog.String("org_id", orgID.String()),
				slog.String("error", err.Error()),
			)
			return false
		}
		return ok
	})
	seller := buildSeller(row)
	if seller == nil {
		return nil, nil, form
	}
	if eventID == nil {
		return seller, nil, form
	}
	name, err := h.publicFeedQueries.GetHostedPageEventPromoterName(ctx, *eventID, orgID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public_page: promoter lookup failed",
				slog.String("event_id", eventID.String()),
				slog.String("error", err.Error()),
			)
		}
		return seller, nil, form
	}
	if name = strings.TrimSpace(name); name == "" {
		return seller, nil, form
	}
	return seller, &hostedPagePromoterResponse{Name: name}, form
}

// showWithdrawalNotice reports whether the widget must tell the buyer that
// tickets for an event on a specific date carry no right of withdrawal: the
// seller organization is in the EU/EEA. A failed lookup answers false and is
// logged; an unknown country is false by the rule itself.
func (h *Handler) showWithdrawalNotice(ctx context.Context, orgID uuid.UUID) bool {
	c, err := h.publicFeedQueries.GetSellerCountries(ctx, orgID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public_feed: seller country lookup failed",
				slog.String("org_id", orgID.String()),
				slog.String("error", err.Error()),
			)
		}
		return false
	}
	return withdrawal.ForSeller(c.LegalAddressCountry, c.Country)
}
