package legaldocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrNotFound: the terms page does not exist (unknown slug, no channel, an
// event that is not this organization's or not published, a feed token of
// another organization). Callers answer it like the hosted page's 404.
var ErrNotFound = errors.New("legaldocs: terms not found")

// ErrNoDocument: no version of the terms is published in the language or in
// English.
var ErrNoDocument = errors.New("legaldocs: no published terms")

// Store is what rendering the terms reads and writes. *gen.Queries
// satisfies it, on the pool or on a transaction.
type Store interface {
	GetPublishedLegalDocument(ctx context.Context, kind, locale string) (gen.LegalDocumentRow, error)
	GetLegalTermsSeller(ctx context.Context, orgID uuid.UUID, feedToken string) (gen.LegalTermsSellerRow, error)
	GetLegalTermsEventBySlug(ctx context.Context, orgID uuid.UUID, eventSlug string, promoterID *uuid.UUID) (gen.LegalTermsEventRow, error)
	GetLegalTermsEventByID(ctx context.Context, orgID, eventID uuid.UUID) (gen.LegalTermsEventRow, error)
	GetLegalTermsPublishedEventByID(ctx context.Context, orgID, eventID uuid.UUID, promoterID *uuid.UUID) (gen.LegalTermsEventRow, error)
	GetLegalTermsPromoter(ctx context.Context, orgID, promoterID uuid.UUID) (gen.LegalTermsPromoterRow, error)
	GetLegalRenderingByInputs(ctx context.Context, documentID uuid.UUID, inputsHash string) (gen.LegalRenderingRow, error)
	InsertLegalRendering(ctx context.Context, p gen.InsertLegalRenderingParams) (gen.LegalRenderingRow, error)
}

var _ Store = (*gen.Queries)(nil)

// TermsRequest names the terms to render.
type TermsRequest struct {
	OrgID uuid.UUID
	// FeedToken picks the channel (any active channel of OrgID); empty =
	// the organization's newest hosted-page channel.
	FeedToken string
	// EventID (checkout), PublicEventID or EventSlug (public page) names
	// the event, whose refund policy and promoter the terms state. At most
	// one is used, in that order. The public ones must name a published
	// event (of the promoter, when PromoterID is set).
	EventID       *uuid.UUID
	PublicEventID *uuid.UUID
	EventSlug     string
	// PromoterID: the page slug named a promoter. An event must then be
	// linked to it; without an event its data is the promoter's.
	PromoterID *uuid.UUID
	// Locale is the buyer's language; anything outside Locales is English.
	Locale string
}

// Rendering is the stored terms HTML for one request.
type Rendering struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	DocumentID      uuid.UUID
	DocumentVersion int
	Locale          string
	RefundMode      string
	SellerLegalName string
	HTML            string
	SHA256          string
	RenderedAt      time.Time
}

// TermsVersion is the value checkout_consents.terms_version records for a
// consent to this rendering: kind, version and language of the document and
// the seller organization ("sale_terms:v1:es;seller:<uuid>").
func (r Rendering) TermsVersion() string {
	return fmt.Sprintf("%s:v%d:%s;seller:%s", KindSaleTerms, r.DocumentVersion, r.Locale, r.OrgID)
}

// NormalizeLocale folds a language tag to one of Locales: case and region
// subtag dropped ("cs-CZ" -> "cs"), anything else English.
func NormalizeLocale(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	if KnownLocale(l) {
		return l
	}
	return DefaultLocale
}

func optString(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// RenderTerms renders the sale terms of an organization for one channel,
// event and language, and stores the rendering once: the same document and
// data always answer the same row (UNIQUE (document_id, inputs_hash)).
// It never updates anything.
func RenderTerms(ctx context.Context, s Store, req TermsRequest) (Rendering, error) {
	seller, err := s.GetLegalTermsSeller(ctx, req.OrgID, strings.TrimSpace(req.FeedToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rendering{}, ErrNotFound
	}
	if err != nil {
		return Rendering{}, fmt.Errorf("legaldocs: seller: %w", err)
	}

	var event *gen.LegalTermsEventRow
	switch {
	case req.EventID != nil:
		e, err := s.GetLegalTermsEventByID(ctx, req.OrgID, *req.EventID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Rendering{}, ErrNotFound
		}
		if err != nil {
			return Rendering{}, fmt.Errorf("legaldocs: event: %w", err)
		}
		event = &e
	case req.PublicEventID != nil:
		e, err := s.GetLegalTermsPublishedEventByID(ctx, req.OrgID, *req.PublicEventID, req.PromoterID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Rendering{}, ErrNotFound
		}
		if err != nil {
			return Rendering{}, fmt.Errorf("legaldocs: event: %w", err)
		}
		event = &e
	case strings.TrimSpace(req.EventSlug) != "":
		e, err := s.GetLegalTermsEventBySlug(ctx, req.OrgID, strings.TrimSpace(req.EventSlug), req.PromoterID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Rendering{}, ErrNotFound
		}
		if err != nil {
			return Rendering{}, fmt.Errorf("legaldocs: event: %w", err)
		}
		event = &e
	}

	var promoter *Promoter
	var eventMode *string
	if event != nil {
		eventMode = event.RefundPolicyMode
		if event.PromoterName != nil && strings.TrimSpace(*event.PromoterName) != "" {
			promoter = &Promoter{
				Name: optString(event.PromoterName), LegalID: optString(event.PromoterLegalID),
				Address: optString(event.PromoterAddress), Website: optString(event.PromoterWebsite),
			}
		}
	} else if req.PromoterID != nil {
		p, err := s.GetLegalTermsPromoter(ctx, req.OrgID, *req.PromoterID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Rendering{}, ErrNotFound
		}
		if err != nil {
			return Rendering{}, fmt.Errorf("legaldocs: promoter: %w", err)
		}
		promoter = &Promoter{Name: p.Name, LegalID: optString(p.LegalID), Address: optString(p.Address), Website: optString(p.Website)}
	}

	locale := NormalizeLocale(req.Locale)
	doc, err := s.GetPublishedLegalDocument(ctx, KindSaleTerms, locale)
	if errors.Is(err, pgx.ErrNoRows) && locale != DefaultLocale {
		doc, err = s.GetPublishedLegalDocument(ctx, KindSaleTerms, DefaultLocale)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Rendering{}, ErrNoDocument
	}
	if err != nil {
		return Rendering{}, fmt.Errorf("legaldocs: document: %w", err)
	}

	data := Data{
		Locale:          doc.Locale,
		DocumentVersion: int(doc.Version),
		Seller:          sellerFromRow(seller),
		Promoter:        promoter,
		RefundMode:      ResolveRefundMode(eventMode, seller.RefundPolicyMode),
	}
	values := BuildValues(data)
	channelID := seller.ChannelID
	rctx := Context{OrgID: seller.OrgID, ChannelID: &channelID}
	if event != nil {
		id := event.EventID
		rctx.EventID = &id
	}
	inputs, hash, err := Inputs(rctx, values)
	if err != nil {
		return Rendering{}, fmt.Errorf("legaldocs: inputs: %w", err)
	}

	row, err := s.GetLegalRenderingByInputs(ctx, doc.ID, hash)
	if err == nil {
		return renderingFromRow(row, values), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Rendering{}, fmt.Errorf("legaldocs: rendering lookup: %w", err)
	}

	tpl, err := Parse(doc.BodyTemplate)
	if err != nil {
		// Publishing parses the template, so this means the allowlist
		// shrank under a published version: a server error, never a page
		// with half the data.
		return Rendering{}, fmt.Errorf("legaldocs: published document %s: %w", doc.ID, err)
	}
	body := Render(tpl, values, doc.Locale)
	row, err = s.InsertLegalRendering(ctx, gen.InsertLegalRenderingParams{
		OrgID:           seller.OrgID,
		ChannelID:       rctx.ChannelID,
		EventID:         rctx.EventID,
		DocumentID:      doc.ID,
		DocumentVersion: doc.Version,
		Locale:          doc.Locale,
		RefundMode:      data.RefundMode,
		Inputs:          inputs,
		InputsHash:      hash,
		BodyHTML:        body,
		BodySHA256:      SHA256Hex([]byte(body)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Another request stored the same rendering meanwhile.
		row, err = s.GetLegalRenderingByInputs(ctx, doc.ID, hash)
	}
	if err != nil {
		return Rendering{}, fmt.Errorf("legaldocs: store rendering: %w", err)
	}
	return renderingFromRow(row, values), nil
}

func sellerFromRow(r gen.LegalTermsSellerRow) Seller {
	country := strings.ToUpper(optString(r.AddressCountry))
	if country == "" {
		country = strings.ToUpper(strings.TrimSpace(r.OrgCountry))
	}
	var phone string
	if len(r.PublicContact) > 0 {
		var c struct {
			Phone any `json:"phone"`
		}
		if json.Unmarshal(r.PublicContact, &c) == nil {
			phone, _ = c.Phone.(string)
		}
	}
	if r.PaymentMode == "merchant_of_record" {
		// The platform sells as merchant of record (plan 2026-10-13
		// section 4): the terms name it, not the organizer. Its legal
		// entity data is not in the database yet.
		return Seller{LegalName: PlatformSellerName, IsPlatform: true}
	}
	name := optString(r.LegalName)
	if name == "" {
		name = strings.TrimSpace(r.OrgName)
	}
	return Seller{
		LegalName:          name,
		LegalForm:          optString(r.LegalForm),
		RegistrationNumber: optString(r.RegistrationNumber),
		TaxID:              optString(r.TaxID),
		TaxScheme:          optString(r.TaxIDScheme),
		AddressLine1:       optString(r.AddressLine1),
		AddressLine2:       optString(r.AddressLine2),
		PostalCode:         optString(r.AddressPostalCode),
		City:               optString(r.AddressCity),
		Country:            country,
		Phone:              strings.TrimSpace(phone),
	}
}

func renderingFromRow(r gen.LegalRenderingRow, v Values) Rendering {
	return Rendering{
		ID:              r.ID,
		OrgID:           r.OrgID,
		DocumentID:      r.DocumentID,
		DocumentVersion: int(r.DocumentVersion),
		Locale:          r.Locale,
		RefundMode:      r.RefundMode,
		SellerLegalName: v.text("seller.legal_name"),
		HTML:            r.BodyHTML,
		SHA256:          r.BodySHA256,
		RenderedAt:      r.RenderedAt,
	}
}
