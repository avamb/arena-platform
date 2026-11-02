package eventbot

// arena_client_referral.go — the calls behind the "Refer a friend" screens
// (spec 52 section 5.2). The rules live in arena-api (the referral package and
// hcheckout): the percentages' ranges, the terms, whether the event's channels
// can return a part of a payment, the cashback itself. The bot only asks and
// shows the answers. The rewards carry no buyer contact data by design.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The programme states as the API spells them.
const (
	referralStatusActive = "active"
	referralStatusPaused = "paused"
	referralStatusEnded  = "ended"
)

// Error codes of the referral routes that have their own words.
const (
	referralCodeNotFound     = "referral.program_not_found"
	referralCodeTerms        = "referral.terms_not_accepted"
	referralCodeProvider     = "referral.provider_unsupported"
	referralCodeInvalidValue = "referral.invalid_value"
	referralCodeEnded        = "referral.program_ended"
)

// referralRewardsRequestLimit is how many rewards one page of the list shows.
const referralRewardsRequestLimit = 5

// ReferralSettings is the body of a programme: PUT replaces all of it. A nil
// MaxFriends is "no limit"; the cap is always sent (100 = no cap).
type ReferralSettings struct {
	FriendDiscountPct int
	CashbackPct       int
	CashbackCapPct    int
	MaxFriends        *int
	AcceptTerms       bool
}

func referralPath(orgID, eventID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/events/" + eventID.String()
}

// ReferralProgram reads the event's programme with the capability of its
// channels. An event with no programme is a 404 referral.program_not_found whose
// APIError carries the capability.
func (c *ArenaClient) ReferralProgram(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (openapi.ReferralProgramResponse, error) {
	var out openapi.ReferralProgramResponse
	err := c.do(ctx, http.MethodGet, referralPath(orgID, eventID)+"/referral-program", jwt, nil, &out)
	return out, err
}

// SetReferralProgram starts the programme or replaces the settings of the live
// one. AcceptTerms is sent only when true; the server demands it for a start.
func (c *ArenaClient) SetReferralProgram(ctx context.Context, jwt string, orgID, eventID uuid.UUID, in ReferralSettings) (openapi.ReferralProgramResponse, error) {
	body := map[string]any{
		"friend_discount_pct": in.FriendDiscountPct,
		"cashback_pct":        in.CashbackPct,
		"cashback_cap_pct":    in.CashbackCapPct,
		"max_friends":         in.MaxFriends,
	}
	if in.AcceptTerms {
		body["accept_terms"] = true
	}
	var out openapi.ReferralProgramResponse
	err := c.do(ctx, http.MethodPut, referralPath(orgID, eventID)+"/referral-program", jwt, body, &out)
	return out, err
}

// ReferralAction pauses, resumes or ends the programme (action is "pause",
// "resume" or "end").
func (c *ArenaClient) ReferralAction(ctx context.Context, jwt string, orgID, eventID uuid.UUID, action string) (openapi.ReferralProgramResponse, error) {
	var out openapi.ReferralProgramResponse
	err := c.do(ctx, http.MethodPost, referralPath(orgID, eventID)+"/referral-program/"+action, jwt, nil, &out)
	return out, err
}

// ReferralSummary reads the totals of the event's programmes, per currency.
func (c *ArenaClient) ReferralSummary(ctx context.Context, jwt string, orgID, eventID uuid.UUID) (openapi.ReferralSummary, error) {
	var out openapi.ReferralSummary
	err := c.do(ctx, http.MethodGet, referralPath(orgID, eventID)+"/referral-program/summary", jwt, nil, &out)
	return out, err
}

// ReferralRewards reads one page of rewards, newest first. state narrows to one
// reward state ("" = all); cursor is the previous page's next_cursor.
func (c *ArenaClient) ReferralRewards(ctx context.Context, jwt string, orgID, eventID uuid.UUID, state, cursor string) (openapi.ReferralRewardList, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(referralRewardsRequestLimit))
	if state != "" {
		q.Set("state", state)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out openapi.ReferralRewardList
	err := c.do(ctx, http.MethodGet, referralPath(orgID, eventID)+"/referral-rewards?"+q.Encode(), jwt, nil, &out)
	return out, err
}
