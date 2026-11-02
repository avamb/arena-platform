package shop

// referral.go — the storefront's side of "Refer a friend" (spec 52 section
// 3.5): does the shop run a programme, and what is the personal code of a paid
// order. The rules (who may get a code, how a discount is worked out) live in
// package referral; this file only asks them for the public routes.

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
)

// ErrReferralNotAvailable: the order has no personal code to show (it is not
// paid, the shop has no live programme, or the token names nothing).
var ErrReferralNotAvailable = errors.New("shop: referral not available")

// ReferralShare is what the "Invite a friend" block shows: the code, the two
// percentages of the programme it was issued under, and the facts the share
// link is built from.
type ReferralShare struct {
	Code              string
	FriendDiscountPct int
	CashbackPct       int
	OrgSlug           string
	ShopDomain        string
}

// ReferralActive says whether the organization's shop has a live programme now.
// A failed read counts as "no": the catalog never fails over a promotion.
func (s *Service) ReferralActive(ctx context.Context, orgID uuid.UUID) bool {
	if s.ready() != nil {
		return false
	}
	_, err := s.q.GetActiveReferralProgramForShop(ctx, orgID, time.Now())
	return err == nil
}

// ReferralForOrder issues (once) and returns the personal code of the paid shop
// order with this token. It needs the shop's programme to be live: a paused or
// ended programme shows no block. ErrReferralNotAvailable covers every "not
// for this order" answer so the route can say one 404.
func (s *Service) ReferralForOrder(ctx context.Context, token string) (ReferralShare, error) {
	if err := s.ready(); err != nil {
		return ReferralShare{}, err
	}
	o, err := s.q.GetReferralShopOrderFactsByToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReferralShare{}, ErrReferralNotAvailable
	}
	if err != nil {
		return ReferralShare{}, err
	}
	if _, err := s.q.GetActiveReferralProgramForShop(ctx, o.OrgID, time.Now()); errors.Is(err, pgx.ErrNoRows) {
		return ReferralShare{}, ErrReferralNotAvailable
	} else if err != nil {
		return ReferralShare{}, err
	}
	code, _, err := referral.IssueShopCode(ctx, s.db, o.ID)
	switch {
	case errors.Is(err, referral.ErrNotEligible), errors.Is(err, referral.ErrNoProgram), errors.Is(err, referral.ErrOrderNotFound):
		return ReferralShare{}, ErrReferralNotAvailable
	case err != nil:
		return ReferralShare{}, err
	}
	facts, err := s.q.GetReferralShopShareFacts(ctx, o.OrgID)
	if err != nil {
		return ReferralShare{}, err
	}
	return ReferralShare{
		Code: code.Code, FriendDiscountPct: code.FriendDiscountPct, CashbackPct: code.CashbackPct,
		OrgSlug: facts.OrgSlug, ShopDomain: facts.ShopDomain,
	}, nil
}
