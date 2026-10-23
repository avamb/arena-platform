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
// 2026-10-15), the site the buyer bought on and what the checkbox showed. It
// is written only when the channel asks for the consent: a tick sent to a
// channel that does not is ignored (data minimization). This code never
// writes the address or browser to a log, an audit row, a public answer or a
// Telegram message; only GET .../orders/{order_id}/consents answers them, to
// the order's organization (users, not API keys) and the platform operator.
// The router's request log records remote_addr and user_agent of EVERY
// request, this one included; the consent row is the only stored copy with
// a retention rule.
//
// A platform superadmin hiding the seller block (seller_hidden) does not turn
// the consent off: the organization is still the seller.
package hfeed

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
)

// PlatformTermsVersion names the platform terms a buyer accepts at checkout
// when no organizer terms rendering can be named (P3a: no API base URL is
// configured, or no terms version is published).
// Change it whenever the text behind PLATFORM_TERMS_URL changes.
const PlatformTermsVersion = "2026-10-15"

// ConsentKindSaleTerms is the only consent kind so far (CHECK in 0155).
const ConsentKindSaleTerms = "sale_terms"

// maxUserAgentRunes caps the stored browser string (CHECK in 0155).
const maxUserAgentRunes = 512

// maxOriginHostLen caps the stored site (CHECK in 0155).
const maxOriginHostLen = 255

// maxShownRunes caps the stored seller name (CHECK in 0155: 500).
const maxShownRunes = 500

// maxShownURLLen caps a stored link (CHECK in 0155: 2000).
const maxShownURLLen = 2000

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

// consentRecorded reports whether checkout/start stores a consent row: only
// a ticked consent for a channel that asks for it. Anything else stores
// nothing of the buyer.
func consentRecorded(required bool, c *PublicCheckoutConsent) bool {
	return required && consentAccepted(c)
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
	originHost   *string
	sellerName   *string
	termsURL     *string
	privacyURL   *string
	// renderingID / termsSHA256: the organizer terms rendering the buyer
	// accepted (P3a, attachTermsRendering); nil for the platform terms.
	renderingID *uuid.UUID
	termsSHA256 *string
}

// newPendingConsent builds the row from the request. clientIP comes from
// httputil.TrustedClientIP(r, 0): the router's trusted-proxy handling has
// already rewritten RemoteAddr, so no hop count is applied here. An address
// that does not parse is stored as NULL rather than failing the sale. shown
// is the consent block the feed answers for this channel: the seller name and
// the two links the checkbox showed.
func newPendingConsent(r *http.Request, orgID uuid.UUID, consentLocale, buyerLocale string, withdrawal bool, now time.Time, shown publicConsentBlock) *pendingConsent {
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
		originHost:   consentOriginHost(r.Header.Get("Origin"), r.Referer()),
		sellerName:   capShown(shown.SellerLegalName, maxShownRunes),
		termsURL:     capShown(shown.TermsURL, maxShownURLLen),
		privacyURL:   capShown(shown.PrivacyURL, maxShownURLLen),
	}
}

// consentOriginHost is the site the buyer bought on: scheme and host of the
// Origin header, else of the Referer; nothing else of the URL (no path, no
// query, no user info). nil when neither is an http(s) URL with a host or the
// result would exceed maxOriginHostLen.
func consentOriginHost(origin, referer string) *string {
	for _, raw := range []string{origin, referer} {
		raw = strings.TrimSpace(raw)
		if raw == "" || raw == "null" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		s := strings.ToLower(u.Scheme + "://" + u.Host)
		if len(s) > maxOriginHostLen || !utf8.ValidString(s) {
			continue
		}
		return &s
	}
	return nil
}

// capShown stores a shown value cut to max characters; nil when empty.
func capShown(raw string, max int) *string {
	s := strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(raw, ""), "\x00", ""))
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return &s
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
		OriginHost:            p.originHost,
		SellerLegalName:       p.sellerName,
		TermsURL:              p.termsURL,
		PrivacyURL:            p.privacyURL,
		LegalRenderingID:      p.renderingID,
		TermsSHA256:           p.termsSHA256,
	}
}
