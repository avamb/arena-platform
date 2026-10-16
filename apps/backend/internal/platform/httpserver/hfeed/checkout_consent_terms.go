// checkout_consent_terms.go links the buyer consent (P2) to the organizer's
// rendered terms of sale (P3a, internal/platform/legaldocs).
//
// While a channel asks for the consent, the feed session's consent block
// points terms_url at the organizer's terms page,
// <API>/v1/public/pages/<org slug>/terms?event_slug=..&feed_token=..&format=html
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
	link, err := h.publicFeedQueries.GetLegalTermsLink(ctx, eventID)
	if err != nil {
		h.logger.Warn("public_feed: terms link lookup failed",
			slog.String("event_id", eventID.String()),
			slog.String("error", err.Error()),
		)
		return ""
	}
	q := url.Values{}
	if link.EventSlug != nil && strings.TrimSpace(*link.EventSlug) != "" {
		q.Set("event_slug", strings.TrimSpace(*link.EventSlug))
	}
	q.Set("feed_token", feedToken)
	q.Set("format", "html")
	return h.legalTermsBase + "/v1/public/pages/" + url.PathEscape(link.OrgSlug) + "/terms?" + q.Encode()
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
// any transaction opens. No published terms (or no terms page for this
// channel) keeps the platform version; a database failure is returned, and
// checkout/start then refuses the sale (it must not store a consent that
// does not say what was accepted).
func (h *Handler) attachTermsRendering(ctx context.Context, p *pendingConsent, orgID, eventID uuid.UUID, feedToken string) error {
	if p == nil || h.legalTermsBase == "" || h.publicFeedQueries == nil {
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
	if errors.Is(err, legaldocs.ErrNotFound) || errors.Is(err, legaldocs.ErrNoDocument) {
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
