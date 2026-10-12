// public_page_seller.go builds the SELLER and PROMOTER blocks of the hosted
// sales page. A buyer is entitled to know who sells the ticket, so a channel
// can switch the blocks on with settings.hosted_page.show_seller. Off is the
// default: deploying this changes nothing a visitor sees until an operator
// turns it on, channel by channel.
//
// Every value comes from the page's OWN organization. A contact is published
// only from settings.hosted_page.public_contact of the channel; the
// organization's contact_email / contact_phone are never read, because they
// may hold a person's private login address.
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
	s.PublicContact = parsePublicContact(row.PublicContact)
	return s
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
func (h *Handler) sellerBlocks(ctx context.Context, orgID uuid.UUID, feedToken string, eventID *uuid.UUID) (*hostedPageSellerResponse, *hostedPagePromoterResponse) {
	row, err := h.publicFeedQueries.GetHostedPageSeller(ctx, orgID, feedToken)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public_page: seller lookup failed",
				slog.String("org_id", orgID.String()),
				slog.String("error", err.Error()),
			)
		}
		return nil, nil
	}
	seller := buildSeller(row)
	if seller == nil {
		return nil, nil
	}
	if eventID == nil {
		return seller, nil
	}
	name, err := h.publicFeedQueries.GetHostedPageEventPromoterName(ctx, *eventID, orgID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("public_page: promoter lookup failed",
				slog.String("event_id", eventID.String()),
				slog.String("error", err.Error()),
			)
		}
		return seller, nil
	}
	if name = strings.TrimSpace(name); name == "" {
		return seller, nil
	}
	return seller, &hostedPagePromoterResponse{Name: name}
}
