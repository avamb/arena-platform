//go:build integration

package httpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

// TestMarketingReward_ReferralCodeIsNeverTheTicksReward guards the seam of
// spec 51-1 and spec 52: a personal "Refer a friend" code is limited to one
// use per buyer like a reward code, so without the check every subscriber
// would get the friend's discount and pay its owner a cashback.
func TestMarketingReward_ReferralCodeIsNeverTheTicksReward(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "mkrf")
	defer f.cleanup()
	defer func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM referral_programs WHERE org_id = $1`, f.orgID); err != nil {
			t.Errorf("cleanup referral_programs: %v", err)
		}
	}()
	defer growthCleanup(t, f)()

	var programID, referralID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO referral_programs (org_id, scope, event_id, friend_discount_pct, cashback_pct, terms_accepted_at)
		VALUES ($1, 'event', $2, 10, 10, now()) RETURNING id`, f.orgID, f.eventID).Scan(&programID); err != nil {
		t.Fatalf("referral programme: %v", err)
	}
	code := "ANNA-" + strings.ToUpper(f.orgID.String()[:4])
	if err := pool.QueryRow(ctx, `
		INSERT INTO promo_codes (org_id, code, discount_type, discount_value, max_uses_per_customer, status,
		                         origin, referral_program_id)
		VALUES ($1, $2, 'percent', 10, 1, 'active', 'referral', $3) RETURNING id`,
		f.orgID, code, programID).Scan(&referralID); err != nil {
		t.Fatalf("referral code: %v", err)
	}

	_, err := marketing.SaveSettings(ctx, pool, f.orgID, marketing.Settings{OptInEnabled: true, ConsentPromoCodeID: &referralID}, nil)
	if !errors.Is(err, marketing.ErrCodeNotFound) {
		t.Fatalf("SaveSettings with a referral code = %v, want ErrCodeNotFound", err)
	}

	// A setting written before the check (or by hand) is no reward either.
	if _, err := pool.Exec(ctx, `
		INSERT INTO org_growth_settings (org_id, cart_reminder_enabled, marketing_optin_enabled, consent_promo_code_id)
		VALUES ($1, false, true, $2)`, f.orgID, referralID); err != nil {
		t.Fatal(err)
	}
	offer, err := marketing.LoadOffer(ctx, pool, f.orgID)
	if err != nil {
		t.Fatalf("LoadOffer: %v", err)
	}
	if !offer.Enabled || offer.Code != nil {
		t.Fatalf("offer = enabled %v, code %v; want the tick without a reward code", offer.Enabled, offer.Code)
	}
}
