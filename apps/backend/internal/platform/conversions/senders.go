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

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

// Production endpoints. Tests point the senders at a stub server. The Graph
// version is not part of MetaBaseURL: it comes from META_GRAPH_VERSION
// (MetaSender.Version, DefaultMetaGraphVersion when empty).
const (
	MetaBaseURL = metaconnect.DefaultGraphBaseURL
	GA4BaseURL  = "https://www.google-analytics.com"
)

// DefaultMetaGraphVersion is the Graph version of a MetaSender without one
// (the config default of META_GRAPH_VERSION).
const DefaultMetaGraphVersion = config.DefaultMetaGraphVersion

// PartnerAgent is the platform name Meta asks a partner to send with every
// Conversions API request (spec 66 section 4.4).
const PartnerAgent = "arenasoldout"

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
	// Graph holds the codes of a Meta error answer (nil otherwise). It never
	// carries Meta's message text.
	Graph *metaconnect.GraphError
}

func (e *SendError) Error() string { return e.Msg }

// Unwrap lets metaconnect.IsTokenInvalid and IsPermissionError see the codes.
func (e *SendError) Unwrap() error {
	if e.Graph == nil {
		return nil
	}
	return e.Graph
}

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
	return send(ctx, c, u, body, false)
}

// send is do; graph reads a non-2xx answer as a Graph error and keeps only
// its codes (metaconnect.ParseGraphError): the body, Meta's message and the
// token never reach the error text.
func send(ctx context.Context, c *http.Client, u string, body []byte, graph bool) error {
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 == 2 {
		return nil
	}
	perm := resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusRequestTimeout
	se := &SendError{Msg: "http " + strconv.Itoa(resp.StatusCode), Permanent: perm}
	if graph {
		if ge := metaconnect.ParseGraphError(resp.StatusCode, raw); ge.Code != 0 {
			se.Graph = ge
			se.Msg += " graph_" + strconv.Itoa(ge.Code)
			if ge.Subcode != 0 {
				se.Msg += "/" + strconv.Itoa(ge.Subcode)
			}
		}
	}
	return se
}

// MetaSender posts Purchase to the Conversions API. BaseURL is the Graph host
// (META_GRAPH_BASE_URL, MetaBaseURL when empty) and Version the Graph version
// (META_GRAPH_VERSION, DefaultMetaGraphVersion when empty); both token
// sources, the Meta connection and the manual token, use them.
type MetaSender struct {
	BaseURL string
	Version string
	Client  *http.Client
}

// endpoint is <base>/<version>/<pixel>/events.
func (m MetaSender) endpoint(pixelID string) string {
	base := strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	if base == "" {
		base = MetaBaseURL
	}
	version := strings.Trim(strings.TrimSpace(m.Version), "/")
	if version == "" {
		version = DefaultMetaGraphVersion
	}
	return base + "/" + version + "/" + url.PathEscape(pixelID) + "/events"
}

// Send posts one Purchase. The access token travels in the JSON body, never in
// the URL, so no log line or error can carry it. A Graph error answer is
// reduced to its codes (SendError.Graph).
func (m MetaSender) Send(ctx context.Context, pixelID, token, testEventCode string, p Purchase) error {
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
	payload := map[string]any{"data": []any{ev}, "access_token": token, "partner_agent": PartnerAgent}
	if testEventCode != "" {
		payload["test_event_code"] = testEventCode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &SendError{Msg: "encode", Permanent: true}
	}
	return send(ctx, c, m.endpoint(pixelID), body, true)
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
