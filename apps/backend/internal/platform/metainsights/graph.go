package metainsights

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
)

// Graph endpoints counted in meta_graph_calls.
const (
	EndpointInsights  = "insights"
	EndpointCampaigns = "campaigns"
)

const (
	maxGraphBody      = 4 << 20
	maxInsightPages   = 50
	insightsPageLimit = "500"
	campaignPageLimit = "200"
	maxCampaignPages  = 20
	insightsFields    = "ad_id,ad_name,adset_id,adset_name,campaign_id,campaign_name,spend,impressions,clicks," +
		"inline_link_clicks,actions,action_values,account_currency,date_start"
)

// purchaseActions is the order in which a purchase action type is taken:
// the first one present wins, they are never summed (Meta reports the same
// purchase under several of them).
var purchaseActions = []string{"omni_purchase", "purchase", "offsite_conversion.fb_pixel_purchase"}

// ErrTooManyPages means the insights answer had more pages than one run
// reads; nothing of the run is written.
var ErrTooManyPages = errors.New("metainsights: the insights answer has too many pages")

// Client reads the Marketing API. The token travels in the Authorization
// header only; no error of Client repeats a URL, a token or Meta's message
// (Graph errors are metaconnect.GraphError, codes only).
type Client struct {
	baseURL string
	version string
	http    *http.Client
}

// NewClient builds a Client. An empty baseURL means
// metaconnect.DefaultGraphBaseURL; a nil hc gets a 30-second timeout.
func NewClient(baseURL, version string, hc *http.Client) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = metaconnect.DefaultGraphBaseURL
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = "v25.0"
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: baseURL, version: version, http: hc}
}

// CallRecorder is told about every Graph request: its endpoint and whether
// it failed (any non-2xx answer or transport failure).
type CallRecorder func(endpoint string, failed bool)

// InsightRow is one ad and day of the insights answer, money in the account
// currency's minor units.
type InsightRow struct {
	Day                string // YYYY-MM-DD, the ad account's timezone
	CampaignID         string
	CampaignName       string
	AdsetID            string
	AdsetName          string
	AdID               string
	AdName             string
	Currency           string
	SpendMinor         int64
	Impressions        int64
	Clicks             int64
	LinkClicks         int64
	Purchases          int64
	PurchaseValueMinor int64
}

// Campaign is one campaign of the ad account.
type Campaign struct {
	ID              string
	Name            string
	EffectiveStatus string
	Objective       string
}

// ActionValue is one entry of the actions or action_values list of a row.
type ActionValue struct {
	ActionType string `json:"action_type"`
	Value      string `json:"value"`
}

type insightJSON struct {
	AdID            string        `json:"ad_id"`
	AdName          string        `json:"ad_name"`
	AdsetID         string        `json:"adset_id"`
	AdsetName       string        `json:"adset_name"`
	CampaignID      string        `json:"campaign_id"`
	CampaignName    string        `json:"campaign_name"`
	Spend           string        `json:"spend"`
	Impressions     string        `json:"impressions"`
	Clicks          string        `json:"clicks"`
	InlineLinkClick string        `json:"inline_link_clicks"`
	Actions         []ActionValue `json:"actions"`
	ActionValues    []ActionValue `json:"action_values"`
	AccountCurrency string        `json:"account_currency"`
	DateStart       string        `json:"date_start"`
}

type pagingJSON struct {
	Cursors struct {
		After string `json:"after"`
	} `json:"cursors"`
	Next string `json:"next"`
}

// PickPurchase returns the purchases count and value of one row: the first
// action type of omni_purchase, purchase, offsite_conversion.fb_pixel_purchase
// present in actions, and the value of the same type in actionValues (or, if
// actions has none, the first of them present in actionValues). exp is the
// currency exponent of the value.
func PickPurchase(actions, values []ActionValue, exp int) (count, valueMinor int64) {
	chosen := ""
	for _, t := range purchaseActions {
		if v, ok := findAction(actions, t); ok {
			chosen = t
			count = parseCount(v)
			break
		}
	}
	if chosen == "" {
		for _, t := range purchaseActions {
			if _, ok := findAction(values, t); ok {
				chosen = t
				break
			}
		}
	}
	if chosen != "" {
		if v, ok := findAction(values, chosen); ok {
			if n, err := DecimalToMinor(v, exp); err == nil && n >= 0 {
				valueMinor = n
			}
		}
	}
	return count, valueMinor
}

func findAction(list []ActionValue, t string) (string, bool) {
	for _, a := range list {
		if a.ActionType == t {
			return a.Value, true
		}
	}
	return "", false
}

// toRow converts one answer row; fallbackCurrency is the stored account
// currency when the row names none. A row without an ad id or a date is
// dropped (ok false).
func toRow(j insightJSON, fallbackCurrency string) (InsightRow, bool, error) {
	if j.AdID == "" || len(j.AdID) > 64 || len(j.DateStart) != 10 {
		return InsightRow{}, false, nil
	}
	if _, err := time.Parse("2006-01-02", j.DateStart); err != nil {
		return InsightRow{}, false, nil
	}
	cur := strings.ToUpper(strings.TrimSpace(j.AccountCurrency))
	if cur == "" {
		cur = strings.ToUpper(strings.TrimSpace(fallbackCurrency))
	}
	exp := CurrencyExponent(cur)
	spend, err := DecimalToMinor(j.Spend, exp)
	if err != nil {
		return InsightRow{}, false, err
	}
	n, v := PickPurchase(j.Actions, j.ActionValues, exp)
	return InsightRow{
		Day:                j.DateStart,
		CampaignID:         clip(j.CampaignID, 64),
		CampaignName:       clip(j.CampaignName, 512),
		AdsetID:            clip(j.AdsetID, 64),
		AdsetName:          clip(j.AdsetName, 512),
		AdID:               j.AdID,
		AdName:             clip(j.AdName, 512),
		Currency:           clip(cur, 8),
		SpendMinor:         spend,
		Impressions:        parseCount(j.Impressions),
		Clicks:             parseCount(j.Clicks),
		LinkClicks:         parseCount(j.InlineLinkClick),
		Purchases:          n,
		PurchaseValueMinor: v,
	}, true, nil
}

// Insights reads GET {act}/insights at ad level, one row per ad and day, for
// since..until (YYYY-MM-DD, account timezone), following the paging cursor
// up to 50 pages (ErrTooManyPages beyond). rec is told about every request.
func (c *Client) Insights(ctx context.Context, token, actID, since, until, fallbackCurrency string, rec CallRecorder) ([]InsightRow, error) {
	tr, _ := json.Marshal(map[string]string{"since": since, "until": until})
	var out []InsightRow
	after := ""
	for page := 0; page < maxInsightPages; page++ {
		q := url.Values{
			"level":          {"ad"},
			"time_increment": {"1"},
			"time_range":     {string(tr)},
			"fields":         {insightsFields},
			"limit":          {insightsPageLimit},
		}
		if after != "" {
			q.Set("after", after)
		}
		var resp struct {
			Data   []insightJSON `json:"data"`
			Paging pagingJSON    `json:"paging"`
		}
		if err := c.get(ctx, actID+"/insights", q, token, EndpointInsights, rec, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			row, ok, err := toRow(d, fallbackCurrency)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, row)
			}
		}
		if resp.Paging.Next == "" || resp.Paging.Cursors.After == "" || resp.Paging.Cursors.After == after {
			return out, nil
		}
		after = resp.Paging.Cursors.After
	}
	return nil, ErrTooManyPages
}

// Campaigns reads GET {act}/campaigns (id, name, effective_status,
// objective), following the paging cursor up to 20 pages.
func (c *Client) Campaigns(ctx context.Context, token, actID string, rec CallRecorder) ([]Campaign, error) {
	var out []Campaign
	after := ""
	for page := 0; page < maxCampaignPages; page++ {
		q := url.Values{"fields": {"id,name,effective_status,objective"}, "limit": {campaignPageLimit}}
		if after != "" {
			q.Set("after", after)
		}
		var resp struct {
			Data []struct {
				ID              string `json:"id"`
				Name            string `json:"name"`
				EffectiveStatus string `json:"effective_status"`
				Objective       string `json:"objective"`
			} `json:"data"`
			Paging pagingJSON `json:"paging"`
		}
		if err := c.get(ctx, actID+"/campaigns", q, token, EndpointCampaigns, rec, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			if d.ID == "" || len(d.ID) > 64 {
				continue
			}
			out = append(out, Campaign{ID: d.ID, Name: clip(d.Name, 512), EffectiveStatus: clip(d.EffectiveStatus, 64), Objective: clip(d.Objective, 64)})
		}
		if resp.Paging.Next == "" || resp.Paging.Cursors.After == "" || resp.Paging.Cursors.After == after {
			break
		}
		after = resp.Paging.Cursors.After
	}
	return out, nil
}

// get sends one GET and decodes the answer into out. It never wraps a
// transport error (a *url.Error repeats the address).
func (c *Client) get(ctx context.Context, path string, query url.Values, token, endpoint string, rec CallRecorder, out any) error {
	failed := true
	defer func() {
		if rec != nil {
			rec(endpoint, failed)
		}
	}()
	u := c.baseURL + "/" + c.version + "/" + strings.TrimPrefix(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return errors.New("metainsights: cannot build the graph request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("metainsights: graph request cancelled or timed out")
		}
		return errors.New("metainsights: graph request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphBody))
	if err != nil {
		return errors.New("metainsights: cannot read the graph answer")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return metaconnect.ParseGraphError(resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("metainsights: graph answer is not valid JSON")
	}
	failed = false
	return nil
}

// IsRateLimited reports a Graph throttling answer (codes 4, 17, 32, 613 and
// 80000-80014): the run stops and the next sweep tries again.
func IsRateLimited(err error) bool {
	var ge *metaconnect.GraphError
	if !errors.As(err, &ge) {
		return false
	}
	switch ge.Code {
	case 4, 17, 32, 613:
		return true
	}
	return ge.Code >= 80000 && ge.Code <= 80014
}

// ErrorClass is what a run does with a failed Graph call.
type ErrorClass int

const (
	// ClassOther: a transport failure or another Graph error; state.last_error
	// is set and the next sweep tries again.
	ClassOther ErrorClass = iota
	// ClassTokenInvalid: code 190, the connection moves to error and its
	// token is wiped.
	ClassTokenInvalid
	// ClassPermission: codes 10, 100, 200-299; the connection stays active
	// with the code in its last_error.
	ClassPermission
	// ClassRateLimited: Meta throttles the app or the account.
	ClassRateLimited
)

// Classify sorts a Graph error into its class and the short code stored in
// last_error ("graph_190", "graph_rate_limited", "graph_unavailable"...).
func Classify(err error) (ErrorClass, string) {
	switch {
	case metaconnect.IsTokenInvalid(err):
		return ClassTokenInvalid, metaconnect.ErrorCode(err)
	case metaconnect.IsPermissionError(err):
		return ClassPermission, metaconnect.ErrorCode(err)
	case IsRateLimited(err):
		return ClassRateLimited, "graph_rate_limited"
	case errors.Is(err, ErrTooManyPages):
		return ClassOther, "insights_too_many_pages"
	case errors.Is(err, errBadDecimal):
		return ClassOther, "insights_bad_number"
	}
	return ClassOther, metaconnect.ErrorCode(err)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}
