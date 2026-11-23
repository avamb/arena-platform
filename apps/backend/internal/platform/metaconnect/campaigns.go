package metaconnect

// campaigns.go - the one ads_management use of the platform (spec 66): the
// owner lists the campaigns of the SELECTED ad account and pauses or resumes
// one. Nothing here creates a campaign or touches a budget or any other
// field: the only write is POST /{campaign_id} with status=PAUSED|ACTIVE.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// Campaign statuses the platform may set (and the only ones it compares
// against before a change).
const (
	CampaignActive = "ACTIVE"
	CampaignPaused = "PAUSED"
)

const (
	maxCampaignPages  = 5
	campaignPageLimit = "200"
	campaignFields    = "id,name,status,effective_status"
)

var (
	// ErrInvalidCampaignStatus is a status other than PAUSED or ACTIVE; no
	// request is sent.
	ErrInvalidCampaignStatus = errors.New("metaconnect: a campaign status must be PAUSED or ACTIVE")
	// ErrInvalidCampaignID is a campaign id that is not 1-32 digits; no
	// request is sent.
	ErrInvalidCampaignID = errors.New("metaconnect: a campaign id must be 1-32 digits")
	// ErrInvalidAdAccount is an ad account id that is not act_<digits> or
	// <digits>; no request is sent.
	ErrInvalidAdAccount = errors.New("metaconnect: an ad account id must be act_<digits>")
	// ErrCampaignNotConfirmed means Meta answered the status change without
	// success=true.
	ErrCampaignNotConfirmed = errors.New("metaconnect: Meta did not confirm the campaign status change")
)

// Campaign is one campaign as Meta reports it. AccountID is the ad account
// id without the act_ prefix (only Campaign reads it).
type Campaign struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	EffectiveStatus string `json:"effective_status"`
	AccountID       string `json:"-"`
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ValidCampaignID reports whether id is 1 to 32 ASCII digits, so it can go
// into a Graph path as is (no "/", "?", "..").
func ValidCampaignID(id string) bool {
	return len(id) >= 1 && len(id) <= 32 && allDigits(id)
}

// ValidCampaignStatus reports whether status is one the platform may set.
func ValidCampaignStatus(status string) bool {
	return status == CampaignActive || status == CampaignPaused
}

// AdAccountDigits returns the digits of an ad account id given as
// act_<digits> or <digits>, and false for anything else. org_meta_assets
// stores the id as Meta's edges answer it (act_<digits>).
func AdAccountDigits(id string) (string, bool) {
	d := strings.TrimPrefix(strings.TrimSpace(id), "act_")
	if len(d) > 32 || !allDigits(d) {
		return "", false
	}
	return d, true
}

// NormalizeAdAccountID returns "act_<digits>" for an ad account id given
// with or without the prefix.
func NormalizeAdAccountID(id string) (string, bool) {
	d, ok := AdAccountDigits(id)
	if !ok {
		return "", false
	}
	return "act_" + d, true
}

// SameAdAccount reports whether a campaign's account_id names the selected
// ad account (both compared without the act_ prefix). An empty or invalid
// side never matches.
func SameAdAccount(accountID, selected string) bool {
	a, ok1 := AdAccountDigits(accountID)
	b, ok2 := AdAccountDigits(selected)
	return ok1 && ok2 && a == b
}

// IsRateLimited reports a Graph throttling error: 4, 17, 32, 613 or
// 80000-80014.
func IsRateLimited(err error) bool {
	var ge *GraphError
	if !errors.As(err, &ge) {
		return false
	}
	switch ge.Code {
	case 4, 17, 32, 613:
		return true
	}
	return ge.Code >= 80000 && ge.Code <= 80014
}

type campaignJSON struct {
	ID              json.RawMessage `json:"id"`
	Name            string          `json:"name"`
	Status          string          `json:"status"`
	EffectiveStatus string          `json:"effective_status"`
	AccountID       json.RawMessage `json:"account_id"`
}

func (j campaignJSON) campaign() Campaign {
	return Campaign{
		ID:              jsonID(j.ID),
		Name:            clip(j.Name, 400),
		Status:          clip(j.Status, 32),
		EffectiveStatus: clip(j.EffectiveStatus, 64),
		AccountID:       strings.TrimPrefix(jsonID(j.AccountID), "act_"),
	}
}

// MarkTokenInvalidIfSealed is MarkTokenInvalid for the token that failed:
// the row changes only while it still holds sealed. A token saved by a
// reconnect between the read and Meta's 190 stays, and the call reports
// false (no notice).
func MarkTokenInvalidIfSealed(ctx context.Context, q Querier, orgID uuid.UUID, sealed, reason string) (bool, error) {
	if sealed == "" {
		return false, nil
	}
	tag, err := q.Exec(ctx, `
		UPDATE org_meta_connections
		   SET status = 'error', access_token_enc = NULL, last_error = $2, checked_at = now(), updated_at = now()
		 WHERE org_id = $1 AND status = 'active' AND access_token_enc = $3`, orgID, clip(reason, 200), sealed)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Campaigns lists the campaigns of adAccountID (act_<digits> or <digits>),
// following paging.cursors.after for at most 5 pages of 200.
func (c *Client) Campaigns(ctx context.Context, token, adAccountID string) ([]Campaign, error) {
	act, ok := NormalizeAdAccountID(adAccountID)
	if !ok {
		return nil, ErrInvalidAdAccount
	}
	out := []Campaign{}
	after := ""
	for page := 0; page < maxCampaignPages; page++ {
		q := url.Values{"fields": {campaignFields}, "limit": {campaignPageLimit}}
		if after != "" {
			q.Set("after", after)
		}
		var resp struct {
			Data   []campaignJSON `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := c.do(ctx, http.MethodGet, act+"/campaigns", q, nil, token, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			cp := d.campaign()
			if !ValidCampaignID(cp.ID) {
				continue
			}
			out = append(out, cp)
		}
		if resp.Paging.Next == "" || resp.Paging.Cursors.After == "" || resp.Paging.Cursors.After == after {
			break
		}
		after = resp.Paging.Cursors.After
	}
	return out, nil
}

// Campaign reads one campaign with its account_id.
func (c *Client) Campaign(ctx context.Context, token, campaignID string) (Campaign, error) {
	if !ValidCampaignID(campaignID) {
		return Campaign{}, ErrInvalidCampaignID
	}
	var resp campaignJSON
	q := url.Values{"fields": {campaignFields + ",account_id"}}
	if err := c.do(ctx, http.MethodGet, campaignID, q, nil, token, &resp); err != nil {
		return Campaign{}, err
	}
	return resp.campaign(), nil
}

// SetCampaignStatus sets the status of one campaign to PAUSED or ACTIVE
// (POST /{id}, form status=...). Any other status, or an invalid id, is
// refused before a request is made. Nothing else is sent.
func (c *Client) SetCampaignStatus(ctx context.Context, token, campaignID, status string) error {
	if !ValidCampaignStatus(status) {
		return ErrInvalidCampaignStatus
	}
	if !ValidCampaignID(campaignID) {
		return ErrInvalidCampaignID
	}
	var resp struct {
		Success *bool `json:"success"`
	}
	if err := c.do(ctx, http.MethodPost, campaignID, nil, url.Values{"status": {status}}, token, &resp); err != nil {
		return err
	}
	if resp.Success == nil || !*resp.Success {
		return ErrCampaignNotConfirmed
	}
	return nil
}
