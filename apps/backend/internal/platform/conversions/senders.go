package conversions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Production endpoints. Tests point the senders at a stub server.
const (
	MetaBaseURL = "https://graph.facebook.com/v21.0"
	GA4BaseURL  = "https://www.google-analytics.com"
)

// Purchase is what a sender needs; Email and Phone are filled only when the
// buyer's tracking consent is true.
type Purchase struct {
	OrderID     string // event_id for Meta, dedup with a browser event
	OrderNumber string // transaction_id for GA4 (orders.system_id)
	Value       float64
	Currency    string
	EventTime   time.Time
	IsTest      bool

	Context BuyerContext
	Email   string
	Phone   string
}

// SendError says whether a retry can help.
type SendError struct {
	Msg       string
	Permanent bool
}

func (e *SendError) Error() string { return e.Msg }

// IsPermanent reports a failure no retry fixes (a 4xx other than 408/429).
func IsPermanent(err error) bool {
	var se *SendError
	return errors.As(err, &se) && se.Permanent
}

func httpClient() *http.Client { return &http.Client{Timeout: 15 * time.Second} }

// do posts body and classifies the answer. The URL may carry a secret (GA4
// api_secret), so a transport error is reduced to its cause and the response
// body is never included in the message.
func do(ctx context.Context, c *http.Client, u string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return &SendError{Msg: "build request", Permanent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &SendError{Msg: "transport: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 == 2 {
		return nil
	}
	perm := resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusRequestTimeout
	return &SendError{Msg: "http " + strconv.Itoa(resp.StatusCode), Permanent: perm}
}

// MetaSender posts Purchase to the Conversions API.
type MetaSender struct {
	BaseURL string
	Client  *http.Client
}

// Send posts one Purchase. The access token travels in the JSON body, never in
// the URL, so no log line or error can carry it.
func (m MetaSender) Send(ctx context.Context, pixelID, token, testEventCode string, p Purchase) error {
	base := m.BaseURL
	if base == "" {
		base = MetaBaseURL
	}
	c := m.Client
	if c == nil {
		c = httpClient()
	}
	user := map[string]any{}
	if p.Email != "" {
		user["em"] = []string{sha256Hex(strings.ToLower(strings.TrimSpace(p.Email)))}
	}
	if d := digits(p.Phone); d != "" {
		user["ph"] = []string{sha256Hex(d)}
	}
	if p.Context.ClientIP != "" {
		user["client_ip_address"] = p.Context.ClientIP
	}
	if p.Context.UserAgent != "" {
		user["client_user_agent"] = p.Context.UserAgent
	}
	if p.Context.FBP != "" {
		user["fbp"] = p.Context.FBP
	}
	fbc := p.Context.FBC
	if fbc == "" && p.Context.FBCLID != "" {
		fbc = "fb.1." + strconv.FormatInt(p.EventTime.UnixMilli(), 10) + "." + p.Context.FBCLID
	}
	if fbc != "" {
		user["fbc"] = fbc
	}
	ev := map[string]any{
		"event_name":    "Purchase",
		"event_time":    p.EventTime.Unix(),
		"event_id":      p.OrderID,
		"action_source": "website",
		"user_data":     user,
		"custom_data": map[string]any{
			"currency": strings.ToUpper(p.Currency),
			"value":    p.Value,
			"order_id": p.OrderNumber,
		},
	}
	if p.Context.PageURL != "" {
		ev["event_source_url"] = p.Context.PageURL
	}
	payload := map[string]any{"data": []any{ev}, "access_token": token}
	if testEventCode != "" {
		payload["test_event_code"] = testEventCode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &SendError{Msg: "encode", Permanent: true}
	}
	return do(ctx, c, strings.TrimRight(base, "/")+"/"+url.PathEscape(pixelID)+"/events", body)
}

// GA4Sender posts purchase to the Measurement Protocol.
type GA4Sender struct {
	BaseURL string
	Client  *http.Client
}

// Send posts one purchase. A test order goes to the validation endpoint
// (/debug/mp/collect), which checks the payload and records nothing.
func (g GA4Sender) Send(ctx context.Context, measurementID, apiSecret string, p Purchase) error {
	base := g.BaseURL
	if base == "" {
		base = GA4BaseURL
	}
	c := g.Client
	if c == nil {
		c = httpClient()
	}
	cid := p.Context.GAClientID
	if cid == "" {
		cid = FallbackClientID(p.OrderNumber, p.EventTime)
	}
	body, err := json.Marshal(map[string]any{
		"client_id": cid,
		"events": []any{map[string]any{
			"name": "purchase",
			"params": map[string]any{
				"transaction_id": p.OrderNumber,
				"value":          p.Value,
				"currency":       strings.ToUpper(p.Currency),
			},
		}},
	})
	if err != nil {
		return &SendError{Msg: "encode", Permanent: true}
	}
	path := "/mp/collect"
	if p.IsTest {
		path = "/debug/mp/collect"
	}
	q := url.Values{"measurement_id": {measurementID}, "api_secret": {apiSecret}}
	return do(ctx, c, strings.TrimRight(base, "/")+path+"?"+q.Encode(), body)
}

var metaEventNames = map[string]string{
	KindPageView:         "PageView",
	KindViewContent:      "ViewContent",
	KindInitiateCheckout: "InitiateCheckout",
	KindLead:             "Lead",
	KindSubscribe:        "CompleteRegistration",
}

var ga4EventNames = map[string]string{
	KindPageView:         "page_view",
	KindViewContent:      "view_item",
	KindInitiateCheckout: "begin_checkout",
	KindLead:             "generate_lead",
	KindSubscribe:        "sign_up",
}

func eventTime(ev SiteEvent) time.Time {
	if ev.Time.IsZero() {
		return time.Now().UTC()
	}
	return ev.Time
}

// SendEvent posts one site event to the Conversions API. A non-empty
// testEventCode makes it a test event.
func (m MetaSender) SendEvent(ctx context.Context, pixelID, token, testEventCode string, ev SiteEvent) error {
	name, ok := metaEventNames[ev.Kind]
	if !ok {
		return &SendError{Msg: "unknown_kind", Permanent: true}
	}
	base := m.BaseURL
	if base == "" {
		base = MetaBaseURL
	}
	c := m.Client
	if c == nil {
		c = httpClient()
	}
	t := eventTime(ev)
	user := map[string]any{}
	if ev.Email != "" {
		user["em"] = []string{sha256Hex(strings.ToLower(strings.TrimSpace(ev.Email)))}
	}
	if d := digits(ev.Phone); d != "" {
		user["ph"] = []string{sha256Hex(d)}
	}
	if ev.Context.ClientIP != "" {
		user["client_ip_address"] = ev.Context.ClientIP
	}
	if ev.Context.UserAgent != "" {
		user["client_user_agent"] = ev.Context.UserAgent
	}
	if ev.Context.FBP != "" {
		user["fbp"] = ev.Context.FBP
	}
	fbc := ev.Context.FBC
	if fbc == "" && ev.Context.FBCLID != "" {
		fbc = "fb.1." + strconv.FormatInt(t.UnixMilli(), 10) + "." + ev.Context.FBCLID
	}
	if fbc != "" {
		user["fbc"] = fbc
	}
	if ev.VisitorID != "" {
		user["external_id"] = []string{sha256Hex(ev.VisitorID)}
	}
	custom := map[string]any{}
	switch ev.Kind {
	case KindViewContent:
		if ev.ContentID != "" {
			custom["content_ids"] = []string{ev.ContentID}
		}
		custom["content_type"] = "product"
	case KindInitiateCheckout:
		if ev.Seller != "" {
			custom["seller"] = ev.Seller
		}
		if ev.City != "" {
			custom["city"] = ev.City
		}
		if ev.CheckoutType != "" {
			custom["checkout_type"] = ev.CheckoutType
		}
		if ev.ContentID != "" {
			custom["content_ids"] = []string{ev.ContentID}
		}
	}
	if ev.Value != 0 && ev.Currency != "" {
		custom["value"] = ev.Value
		custom["currency"] = strings.ToUpper(ev.Currency)
	}
	e := map[string]any{
		"event_name":    name,
		"event_time":    t.Unix(),
		"event_id":      ev.EventID,
		"action_source": "website",
		"user_data":     user,
	}
	if len(custom) > 0 {
		e["custom_data"] = custom
	}
	if ev.Context.PageURL != "" {
		e["event_source_url"] = ev.Context.PageURL
	}
	payload := map[string]any{"data": []any{e}, "access_token": token}
	if testEventCode != "" {
		payload["test_event_code"] = testEventCode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &SendError{Msg: "encode", Permanent: true}
	}
	return do(ctx, c, strings.TrimRight(base, "/")+"/"+url.PathEscape(pixelID)+"/events", body)
}

// SendEvent posts one site event to the Measurement Protocol. The GA client id
// is required: without it the event is a permanent failure ("no_client_id").
func (g GA4Sender) SendEvent(ctx context.Context, measurementID, apiSecret string, ev SiteEvent) error {
	name, ok := ga4EventNames[ev.Kind]
	if !ok {
		return &SendError{Msg: "unknown_kind", Permanent: true}
	}
	if ev.Context.GAClientID == "" {
		return &SendError{Msg: "no_client_id", Permanent: true}
	}
	base := g.BaseURL
	if base == "" {
		base = GA4BaseURL
	}
	c := g.Client
	if c == nil {
		c = httpClient()
	}
	params := map[string]any{"engagement_time_msec": 1}
	if n, err := strconv.ParseInt(ev.GASessionID, 10, 64); err == nil {
		params["session_id"] = n
	}
	if ev.Context.PageURL != "" && ev.Kind == KindPageView {
		params["page_location"] = ev.Context.PageURL
	}
	switch ev.Kind {
	case KindViewContent, KindInitiateCheckout:
		if ev.ContentID != "" || ev.ContentName != "" {
			params["items"] = []any{map[string]any{"item_id": ev.ContentID, "item_name": ev.ContentName}}
		}
	}
	if ev.Kind == KindInitiateCheckout {
		if ev.Seller != "" {
			params["affiliation"] = ev.Seller
		}
		if ev.CheckoutType != "" {
			params["checkout_type"] = ev.CheckoutType
		}
	}
	if ev.Value != 0 && ev.Currency != "" {
		params["value"] = ev.Value
		params["currency"] = strings.ToUpper(ev.Currency)
	}
	body, err := json.Marshal(map[string]any{
		"client_id": ev.Context.GAClientID,
		"events":    []any{map[string]any{"name": name, "params": params}},
	})
	if err != nil {
		return &SendError{Msg: "encode", Permanent: true}
	}
	q := url.Values{"measurement_id": {measurementID}, "api_secret": {apiSecret}}
	return do(ctx, c, strings.TrimRight(base, "/")+"/mp/collect?"+q.Encode(), body)
}

// FallbackClientID is the GA client id of a purchase whose browser sent none:
// stable per order, shaped like GA's own (<random>.<timestamp>).
func FallbackClientID(orderNumber string, t time.Time) string {
	h := sha256.Sum256([]byte("arena-ga4:" + orderNumber))
	n := uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
	return fmt.Sprintf("%d.%d", n, t.Unix())
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
