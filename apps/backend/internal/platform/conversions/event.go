package conversions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat/money"
)

// Kinds of an event without an order (conversion_deliveries.kind, migration
// 0219). The purchase kind stays the order's.
const (
	KindPageView         = "page_view"
	KindViewContent      = "view_content"
	KindInitiateCheckout = "initiate_checkout"
	KindLead             = "lead"
	KindSubscribe        = "subscribe"
	// KindAddToCart is the widget's priced cart (checkout/quote, migration
	// 0225); queued only with the buyer's explicit tracking consent.
	KindAddToCart = "add_to_cart"
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
	// Items are the sessions of a widget cart (add_to_cart, initiate_checkout),
	// in the Purchase item shape; empty for a static site's events.
	Items []PurchaseItem `json:"items,omitempty"`
	// SeatsConsidered is how many distinct seats the buyer toggled in this
	// cart (add_to_cart only); 0 is left out.
	SeatsConsidered int `json:"seats_considered,omitempty"`
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
		SELECT NULL, o.org_id, p.provider, $2, $3, $4::jsonb
		  FROM (SELECT $1::uuid AS org_id) o
		  LEFT JOIN org_conversion_settings st ON st.org_id = o.org_id
		 CROSS JOIN LATERAL (VALUES
		        -- Meta is ready with a manual pixel and token, or (when the
		        -- connection is on) with an active Meta connection that has a
		        -- token and a selected dataset, exactly as Enqueue decides.
		        ('meta'::text, (COALESCE(st.meta_pixel_id, '') <> '' AND COALESCE(st.meta_capi_token, '') <> '')
		                       OR ($6::boolean AND EXISTS (
		                             SELECT 1
		                               FROM org_meta_connections c
		                               JOIN org_meta_assets a ON a.org_id = c.org_id AND a.kind = 'dataset' AND a.selected
		                              WHERE c.org_id = o.org_id AND c.status = 'active' AND c.access_token_enc IS NOT NULL))),
		        ('ga4'::text,  COALESCE(st.ga4_measurement_id, '') <> '' AND COALESCE(st.ga4_api_secret, '') <> ''
		                       AND $5::boolean)
		       ) AS p(provider, ready)
		 WHERE p.ready
		ON CONFLICT (org_id, provider, event_id) WHERE event_id IS NOT NULL DO NOTHING`,
		orgID, ev.Kind, ev.EventID, raw, ev.Context.GAClientID != "", s.connectionOn())
	return err
}

// CartEvent builds the add_to_cart or initiate_checkout event of a widget
// cart. session names what, where and when (its Quantity and Price are set
// here, an empty SessionID leaves the event without items); quantity is the
// cart's ticket count, maxUnit its highest unit price and total what the buyer
// pays, both in minor units of currency. The money is converted once, here.
func CartEvent(kind, eventID string, session PurchaseItem, quantity int, maxUnit, total int64,
	currency string, bc BuyerContext, seatsConsidered int) SiteEvent {
	scale := money.ScaleFor(currency)
	ev := SiteEvent{
		Kind: kind, EventID: eventID, Context: bc,
		Value: money.MajorScale(total, scale), Currency: currency,
	}
	if session.SessionID != "" && quantity > 0 {
		session.Quantity = quantity
		session.Price = money.MajorScale(maxUnit, scale)
		ev.Items = []PurchaseItem{session}
	}
	if seatsConsidered > 0 {
		ev.SeatsConsidered = seatsConsidered
	}
	return ev
}

// SessionItem reads what, where and when of one session for a cart event
// (no quantity or price). Best effort: false when the session is unknown or
// the read fails, and the event then goes out without items.
func (s *PGStore) SessionItem(ctx context.Context, sessionID uuid.UUID) (PurchaseItem, bool) {
	var it PurchaseItem
	var evID *string
	err := s.pool.QueryRow(ctx, `
		SELECT s.id::text, e.id::text, COALESCE(e.name, ''), s.start_at,
		       COALESCE(v.name, ''), COALESCE(t_en.value, ci.slug, ''), COALESCE(co.iso2::text, '')
		  FROM sessions s
		  LEFT JOIN events e ON e.id = s.event_id
		  LEFT JOIN venues v ON v.id = s.venue_id
		  LEFT JOIN cities ci ON ci.id = v.city_id
		  LEFT JOIN countries co ON co.id = ci.country_id
		  LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities'
		         AND t_en.key = ci.slug AND t_en.locale = 'en'
		 WHERE s.id = $1`, sessionID).
		Scan(&it.SessionID, &evID, &it.EventName, &it.StartAt, &it.Venue, &it.City, &it.Country)
	if err != nil {
		return PurchaseItem{}, false
	}
	if evID != nil {
		it.EventID = *evID
	}
	it.StartAt = it.StartAt.UTC()
	return it, true
}
