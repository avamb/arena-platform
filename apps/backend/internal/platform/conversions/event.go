package conversions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Kinds of an event without an order (conversion_deliveries.kind, migration
// 0218). The purchase kind stays the order's.
const (
	KindPageView         = "page_view"
	KindViewContent      = "view_content"
	KindInitiateCheckout = "initiate_checkout"
	KindLead             = "lead"
	KindSubscribe        = "subscribe"
)

// SiteEvent is one event of a static site (spec 63 section 19), stored in
// conversion_deliveries.payload until the sweep sends it. The consent decision
// was taken before it was queued (model "on until the visitor refuses"): a
// refusal queues nothing. Personal values are cleared when the row is final.
type SiteEvent struct {
	Kind    string `json:"kind"`     // one of the Kind* constants
	EventID string `json:"event_id"` // browser id, the dedup key of Meta and GA4
	// Time is when the visitor acted; zero means "now" at queue time.
	Time time.Time `json:"time"`
	// Context: fbp, fbc, fbclid, gclid, ga_client_id, page_url, client_ip,
	// user_agent (same bounded form as a checkout's buyer context).
	Context BuyerContext `json:"context"`
	// VisitorID is the site's first-party visitor id; Meta gets its SHA-256 as external_id.
	VisitorID string `json:"visitor_id,omitempty"`
	// GASessionID is the number of the GA4 session (cookie _ga_<id>), optional.
	GASessionID string `json:"ga_session_id,omitempty"`
	// Email and Phone are set only for a lead and only when the visitor typed them.
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
	// ContentID is the platform event id (Meta content_ids, GA4 item_id), ContentName its title.
	ContentID   string `json:"content_id,omitempty"`
	ContentName string `json:"content_name,omitempty"`
	// Seller is the box office a click leads to (GA4 affiliation, Meta custom_data.seller).
	Seller string `json:"seller,omitempty"`
	City   string `json:"city,omitempty"`
	// Value is the "from" price in major units, Currency its ISO code; both optional.
	Value    float64 `json:"value,omitempty"`
	Currency string  `json:"currency,omitempty"`
	// CheckoutType is "external" (a partner box office) or "own" (the site's modal).
	CheckoutType string `json:"checkout_type,omitempty"`
}

// EnqueueEvent queues one delivery per ready provider of the organization
// (Meta needs pixel id and token, GA4 needs measurement id and secret; GA4 also
// needs a client id, a GA4 row without one is not queued). Idempotent on
// (org, provider, event id): the same browser event sent twice queues once.
func (s *PGStore) EnqueueEvent(ctx context.Context, orgID uuid.UUID, ev SiteEvent) error {
	if ev.EventID == "" || ev.Kind == "" || ev.Kind == "purchase" {
		return fmt.Errorf("conversions: event needs a kind and an event id")
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO conversion_deliveries (order_id, org_id, provider, kind, event_id, payload)
		SELECT NULL, st.org_id, p.provider, $2, $3, $4::jsonb
		  FROM org_conversion_settings st
		 CROSS JOIN LATERAL (VALUES
		        ('meta'::text, COALESCE(st.meta_pixel_id, '') <> '' AND COALESCE(st.meta_capi_token, '') <> ''),
		        ('ga4'::text,  COALESCE(st.ga4_measurement_id, '') <> '' AND COALESCE(st.ga4_api_secret, '') <> ''
		                       AND $5::boolean)
		       ) AS p(provider, ready)
		 WHERE st.org_id = $1 AND p.ready
		ON CONFLICT (org_id, provider, event_id) WHERE event_id IS NOT NULL DO NOTHING`,
		orgID, ev.Kind, ev.EventID, raw, ev.Context.GAClientID != "")
	return err
}
