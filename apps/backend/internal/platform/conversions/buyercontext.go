// Package conversions sends the server-side half of a purchase to the ad
// platforms: Meta Conversions API and GA4 Measurement Protocol (spec 62 section
// 8.3, items П-8 and П-9).
//
// Three pieces:
//
//   - BuyerContext, what the site sends in checkout/start (fbp, fbc, fbclid,
//     gclid, GA4 client id) plus the client IP and User-Agent the server
//     itself saw. It is stored beside the checkout session, bounded and never
//     logged. The tracking consent the same object may carry is NOT stored
//     here: ConsentFromBuyerContext hands it to package trackingconsent,
//     which keeps it on checkout_sessions (migration 0205, P-20) - the one
//     stored truth that PATCH .../consent also updates.
//   - Dispatcher, a leg of arena-worker's outbox fan-out. On v1.order.paid it
//     only QUEUES a conversion_deliveries row per configured provider and
//     always returns nil, so an ad platform can never delay or re-trigger the
//     webhooks.
//   - Sweep, the self-scheduling worker job conversions.sweep that sends the
//     due rows with a growing pause and records last_error.
//
// Consent is read at SEND time through ONE function, PGStore.ConsentForOrder,
// which reads trackingconsent.ForOrder (checkout_sessions via
// orders.checkout_session_id).
package conversions

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// Field limits. The values come from the open internet, so each is bounded.
const (
	maxToken       = 256  // fbp, fbc, fbclid, gclid, ga_client_id
	maxVersion     = 64   // tracking_consent_text_version
	maxUserAgent   = 512  // the browser User-Agent
	maxPageURL     = 2048 // the page the checkout started on
	maxContextBody = 8 << 10
)

// BuyerContext is the stored form (snake_case JSON). Empty fields are omitted.
type BuyerContext struct {
	FBCLID     string `json:"fbclid,omitempty"`
	GCLID      string `json:"gclid,omitempty"`
	FBP        string `json:"fbp,omitempty"`
	FBC        string `json:"fbc,omitempty"`
	GAClientID string `json:"ga_client_id,omitempty"`
	PageURL    string `json:"page_url,omitempty"`
	// ClientIP and UserAgent are what THIS server saw on checkout/start.
	ClientIP  string `json:"client_ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// CleanBuyerContext decodes the client's buyer_context object and adds the IP
// and User-Agent the server saw. It returns nil when the request carried no
// object at all. A malformed or oversized object is nil too, never an error: a
// purchase must not fail over a tracking label.
func CleanBuyerContext(raw json.RawMessage, clientIP, userAgent string) []byte {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" || len(raw) > maxContextBody {
		return nil
	}
	var in struct {
		FBCLID     string `json:"fbclid"`
		GCLID      string `json:"gclid"`
		FBP        string `json:"fbp"`
		FBC        string `json:"fbc"`
		GAClientID string `json:"ga_client_id"`
		PageURL    string `json:"page_url"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil
	}
	out := BuyerContext{
		FBCLID:     clean(in.FBCLID, maxToken),
		GCLID:      clean(in.GCLID, maxToken),
		FBP:        clean(in.FBP, maxToken),
		FBC:        clean(in.FBC, maxToken),
		GAClientID: clean(in.GAClientID, maxToken),
		PageURL:    clean(in.PageURL, maxPageURL),
		ClientIP:   clean(clientIP, 64),
		UserAgent:  clean(userAgent, maxUserAgent),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// UnstatedTextVersion is the banner-text version stored for a refusal that
// came without one: a "no" needs no text to be honoured.
const UnstatedTextVersion = "unstated"

// ConsentFromBuyerContext reads buyer_context.tracking_consent and
// tracking_consent_text_version as a trackingconsent.Input, so checkout/start
// stores them on checkout_sessions like the top-level tracking_consent object.
// nil when the object carries no boolean tracking_consent, or when it says yes
// without a usable text version (a yes counts only with the text it answered;
// nothing stored reads as no). A no without a version is kept as a no.
func ConsentFromBuyerContext(raw json.RawMessage) *trackingconsent.Input {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" || len(raw) > maxContextBody {
		return nil
	}
	var in struct {
		TrackingConsent            *bool  `json:"tracking_consent"`
		TrackingConsentTextVersion string `json:"tracking_consent_text_version"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.TrackingConsent == nil {
		return nil
	}
	version := clean(in.TrackingConsentTextVersion, maxVersion)
	if version == "" {
		if *in.TrackingConsent {
			return nil
		}
		version = UnstatedTextVersion
	}
	granted := *in.TrackingConsent
	return &trackingconsent.Input{Granted: &granted, Version: version}
}

// clean trims, rejects control characters and drops a value over max runes.
// It is dropped, not truncated: a truncated click id would attribute wrongly.
func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > max {
		return ""
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}
