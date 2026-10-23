// checkout_consent_terms.go links the buyer consent (P2) to the organizer's
// rendered terms of sale (P3a, internal/platform/legaldocs).
//
// While a channel asks for the consent, the feed session's consent block
// points terms_url at the organizer's terms page,
// <API>/v1/public/pages/<org slug>/terms?event_id=..&feed_token=..&format=html
// (a direct API address until the tickets page renders the terms, P3c), and
// checkout/start stores WHICH rendering the buyer accepted: its id and the
// SHA-256 of its HTML in checkout_consents, and the document version in
// terms_version ("sale_terms:v1:es;seller:<org>"). The rendering is made on
// the pool BEFORE the hold transaction opens, from the same inputs the link
// renders (organization, the feed token's channel, the event, the buyer's
// language), so the page behind the link and the stored rendering are the
// same row while the organizer's data does not change.
//
// Nothing changes for a channel that does not ask for the consent, and
// without an API base URL the link and the version stay the platform's.
package hfeed

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/legaldocs"
)

// WithLegalTerms configures the public API base URL the consent's terms link
// is built on (APIPublicBaseURL). Empty keeps the platform terms link.
func (h *Handler) WithLegalTerms(apiBase string) *Handler {
	h.legalTermsBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	return h
}

// legalTermsURL is the link to the organizer's terms for an event sold
// through feedToken; "" when it cannot be built (no base URL, lookup failed).
func (h *Handler) legalTermsURL(ctx context.Context, eventID uuid.UUID, feedToken string) string {
	if h.legalTermsBase == "" || h.publicFeedQueries == nil {
		return ""
	}
	// A failed publication lookup only hides the link here: showing the
	// platform terms is safe for display. checkout/start decides differently.
	if published, err := h.termsPublished(ctx); err != nil || !published {
		return ""
	}
	link, err := h.publicFeedQueries.GetLegalTermsLink(ctx, eventID)
	if err != nil {
		h.logger.Warn("public_feed: terms link lookup failed",
			slog.String("event_id", eventID.String()),
			slog.String("error", err.Error()),
		)
		return ""
	}
	// The event goes by id, which every event has: a link by slug would
	// render without the event for an event that has none, and the stored
	// rendering would not be the one behind the link.
	q := url.Values{}
	q.Set("event_id", eventID.String())
	q.Set("feed_token", feedToken)
	q.Set("format", "html")
	return h.legalTermsBase + "/v1/public/pages/" + url.PathEscape(link.OrgSlug) + "/terms?" + q.Encode()
}

// termsPublished reports whether the platform has published the organizer
// terms template (its English version, the fallback of every language).
// Drafts are never public: until a version is published the consent keeps
// pointing at the platform terms (P3 execution contract).
func (h *Handler) termsPublished(ctx context.Context) (bool, error) {
	_, err := h.publicFeedQueries.GetPublishedLegalDocument(ctx, legaldocs.KindSaleTerms, legaldocs.DefaultLocale)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	h.logger.Warn("public_feed: terms publication lookup failed", slog.String("error", err.Error()))
	return false, err
}

// consentBlockFor is consentBlock with the organizer's terms link when the
// channel asks for the consent and the link can be built.
func (h *Handler) consentBlockFor(ctx context.Context, required bool, legalName string, eventID uuid.UUID, feedToken string) publicConsentBlock {
	b := h.consentBlock(required, legalName)
	if required {
		if u := h.legalTermsURL(ctx, eventID, feedToken); u != "" {
			b.TermsURL = u
		}
	}
	return b
}

// attachTermsRendering renders (or finds) the organizer's terms the buyer
// accepts and names them on the pending consent. It runs on the pool before
// any transaction opens. No published terms, no terms page for this channel
// or a seller profile that is not complete keeps the platform version; a database failure is returned, and
// checkout/start then refuses the sale (it must not store a consent that
// does not say what was accepted).
func (h *Handler) attachTermsRendering(ctx context.Context, p *pendingConsent, orgID, eventID uuid.UUID, feedToken string) error {
	if p == nil || h.legalTermsBase == "" || h.publicFeedQueries == nil {
		return nil
	}
	// A database failure is not "no published terms": storing the platform
	// version then would record a consent that may not match the page the
	// buyer saw, so the sale is refused instead.
	published, err := h.termsPublished(ctx)
	if err != nil {
		return err
	}
	if !published {
		return nil
	}
	locale := ""
	if p.locale != nil {
		locale = *p.locale
	}
	r, err := legaldocs.RenderTerms(ctx, h.publicFeedQueries, legaldocs.TermsRequest{
		OrgID:     orgID,
		FeedToken: feedToken,
		EventID:   &eventID,
		Locale:    locale,
	})
	if errors.Is(err, legaldocs.ErrNotFound) || errors.Is(err, legaldocs.ErrNoDocument) || errors.Is(err, legaldocs.ErrSellerIncomplete) {
		h.logger.Warn("public_feed_checkout: no organizer terms to name, keeping the platform terms",
			slog.String("org_id", orgID.String()),
			slog.String("error", err.Error()),
		)
		return nil
	}
	if err != nil {
		return err
	}
	p.termsVersion = r.TermsVersion()
	id, sum := r.ID, r.SHA256
	p.renderingID = &id
	p.termsSHA256 = &sum
	return nil
}
