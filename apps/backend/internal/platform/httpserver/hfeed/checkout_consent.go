// checkout_consent.go is the buyer consent at checkout (task P2 of
// 08_architecture/40, plan 8.9): a sales channel may ask the buyer to tick
// "I accept the sale terms of the organizer <legal name> and the platform
// policy" before paying.
//
// The switch is settings.hosted_page.consent_required of the channel, written
// only by the seller-display route and only while the seller profile is
// complete. The checkout asks for the consent while the switch is on AND the
// profile is still complete (consentFromFlags): an organizer whose legal data
// was emptied afterwards sells without the seller block and without the
// question, exactly as the plan says.
//
// The record (checkout_consents, migration 0155) is dispute evidence. It
// stores the buyer's real IP address and browser string (owner decision
// 2026-10-15). Neither is ever written to a log, an audit row, a public answer
// or a Telegram message; only GET .../orders/{order_id}/consents answers them,
// to the order's organization and the platform operator.
package hfeed

import (
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

// PlatformTermsVersion names the platform terms a buyer accepts at checkout
// until the organizer's rendered terms exist (task P3, legal_renderings).
// Change it whenever the text behind PLATFORM_TERMS_URL changes.
const PlatformTermsVersion = "2026-10-15"

// ConsentKindSaleTerms is the only consent kind so far (CHECK in 0155).
const ConsentKindSaleTerms = "sale_terms"

// maxUserAgentRunes caps the stored browser string (CHECK in 0155).
const maxUserAgentRunes = 512

// PublicCheckoutConsent is the optional consent member of the checkout/start
// body: the buyer ticked the box (accepted) in the widget's language (locale).
type PublicCheckoutConsent struct {
	Accepted bool   `json:"accepted"`
	Locale   string `json:"locale,omitempty"`
}

// publicConsentBlock is the consent member of the public feed session: whether
// the widget must show the checkbox, the seller's legal name for its text and
// the two pages it links to.
type publicConsentBlock struct {
	Required        bool   `json:"required"`
	SellerLegalName string `json:"seller_legal_name,omitempty"`
	TermsURL        string `json:"terms_url,omitempty"`
	PrivacyURL      string `json:"privacy_url,omitempty"`
}

// WithCheckoutConsent configures the pages the consent checkbox links to: the
// platform terms (PLATFORM_TERMS_URL, else <site>/legal/terms) and the privacy
// policy (the contact form's rule).
func (h *Handler) WithCheckoutConsent(termsURL, privacyURL string) *Handler {
	h.consentTermsURL = strings.TrimSpace(termsURL)
	h.consentPrivacyURL = strings.TrimSpace(privacyURL)
	return h
}

// consentFromFlags decides the consent from the buyer-flags row of the feed
// token's channel: required while the channel's switch is on and the seller
// profile is complete (sellerinfo.Missing is empty - the same rule the seller
// block and the seller-display route apply). legalName is what the checkbox
// names; it falls back to the organization's name.
func consentFromFlags(f gen.FeedTokenBuyerFlagsRow) (required bool, legalName string) {
	if !f.ConsentRequired {
		return false, ""
	}
	p := sellerinfo.Profile{
		LegalName:   deref(f.LegalName),
		TaxID:       deref(f.TaxID),
		AddressLine: deref(f.AddressLine1),
		City:        deref(f.AddressCity),
		PostalCode:  deref(f.AddressPostalCode),
	}
	if c := parsePublicContact(f.PublicContact); c != nil {
		p.PublicEmail = c.Email
	}
	if len(sellerinfo.Missing(p)) > 0 {
		return false, ""
	}
	legalName = p.LegalName
	if legalName == "" {
		legalName = strings.TrimSpace(f.OrgName)
	}
	return true, legalName
}

// consentBlock is the feed session's consent member for one channel decision.
func (h *Handler) consentBlock(required bool, legalName string) publicConsentBlock {
	b := publicConsentBlock{Required: required, TermsURL: h.consentTermsURL, PrivacyURL: h.consentPrivacyURL}
	if required {
		b.SellerLegalName = legalName
	}
	return b
}

// consentTermsVersion is what the buyer accepted: the platform terms version
// and the seller organization (P3 will name a legal_renderings row instead).
func consentTermsVersion(orgID uuid.UUID) string {
	return "platform-terms:" + PlatformTermsVersion + ";seller:" + orgID.String()
}

// consentAccepted reports whether the body carries a ticked consent.
func consentAccepted(c *PublicCheckoutConsent) bool {
	return c != nil && c.Accepted
}

// consentRefused is the pre-flight rule of checkout/start: a channel that
// asks for the consent refuses a body without a ticked one, before any hold.
func consentRefused(required bool, c *PublicCheckoutConsent) bool {
	return required && !consentAccepted(c)
}

// pendingConsent is the consent row checkout/start writes in the checkout
// transaction, gathered before any transaction opens.
type pendingConsent struct {
	termsVersion string
	locale       *string
	acceptedAt   time.Time
	clientIP     *string
	userAgent    *string
	withdrawal   bool
}

// newPendingConsent builds the row from the request. clientIP comes from
// httputil.TrustedClientIP(r, 0): the router's trusted-proxy handling has
// already rewritten RemoteAddr, so no hop count is applied here. An address
// that does not parse is stored as NULL rather than failing the sale.
func newPendingConsent(r *http.Request, orgID uuid.UUID, consentLocale, buyerLocale string, withdrawal bool, now time.Time) *pendingConsent {
	locale := normalizeBuyerLocale(consentLocale)
	if locale == "" {
		locale = buyerLocale
	}
	return &pendingConsent{
		termsVersion: consentTermsVersion(orgID),
		locale:       localePtr(locale),
		acceptedAt:   now.UTC(),
		clientIP:     consentClientIP(httputil.TrustedClientIP(r, 0)),
		userAgent:    capUserAgent(r.UserAgent()),
		withdrawal:   withdrawal,
	}
}

// consentClientIP parses the textual address; nil when it is not one.
func consentClientIP(raw string) *string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	s := addr.Unmap().WithZone("").String()
	return &s
}

// capUserAgent keeps at most maxUserAgentRunes characters of a valid UTF-8
// browser string without NUL bytes (Postgres text refuses them); nil when
// nothing is left.
func capUserAgent(raw string) *string {
	s := strings.ToValidUTF8(raw, "")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) > maxUserAgentRunes {
		s = string([]rune(s)[:maxUserAgentRunes])
	}
	return &s
}

// params is the insert for the checkout session cs of orgID.
func (p *pendingConsent) params(orgID, checkoutSessionID uuid.UUID) gen.InsertCheckoutConsentParams {
	return gen.InsertCheckoutConsentParams{
		OrgID:                 orgID,
		CheckoutSessionID:     checkoutSessionID,
		Kind:                  ConsentKindSaleTerms,
		TermsVersion:          p.termsVersion,
		Locale:                p.locale,
		AcceptedAt:            p.acceptedAt,
		ClientIP:              p.clientIP,
		UserAgent:             p.userAgent,
		WithdrawalNoticeShown: p.withdrawal,
	}
}
