package shop

// storefront.go — who opens the storefront and when (spec
// 08_architecture/61 section 3.4, owner decisions V-1 and V-11).
//
// shop_settings.enabled is "the storefront is open and takes orders". Any
// caller the organization routes let through with shop.manage (owner,
// manager, platform superadmin) may open or close it while the shop module is
// connected; the module gate lives in the HTTP layer. Opening is refused with
// 422 shop.cannot_open and details.blockers unless the settings were saved,
// at least one product is active and the shop's payment channel has a usable,
// verified provider config. Closing is always allowed. The storefront host
// (shop_domain) stays a platform superadmin's decision.
//
// The payment verdict is gathered by the caller on the pool BEFORE the
// transaction (it reads the provider configs through the checkout's own
// pre-flight) and passed in; the rest is read on the transaction.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
)

// What keeps a storefront closed (details.blockers of shop.cannot_open and
// open_blockers of the settings), in this order.
const (
	// BlockerSettingsNotSaved: the shop settings were never saved.
	BlockerSettingsNotSaved = "settings_not_saved"
	// BlockerNoActiveProduct: no product is on sale (status active).
	BlockerNoActiveProduct = "no_active_product"
	// BlockerPaymentNotConfigured: the shop's sales channel has no active,
	// verified payment provider config the checkout could take money with.
	BlockerPaymentNotConfigured = "payment_not_configured"
)

// blockers lists what keeps the storefront closed.
func blockers(saved, hasActive, paymentReady bool) []string {
	out := []string{}
	if !saved {
		out = append(out, BlockerSettingsNotSaved)
	}
	if !hasActive {
		out = append(out, BlockerNoActiveProduct)
	}
	if !paymentReady {
		out = append(out, BlockerPaymentNotConfigured)
	}
	return out
}

// cannotOpen is the 422 refusal of an opening with what is missing.
func cannotOpen(list []string) *Error {
	e := invalid("shop.cannot_open",
		"the storefront cannot be opened yet: save the shop settings, put at least one product on sale and connect a verified payment provider to the shop's sales channel")
	e.Details = map[string]any{"blockers": list}
	return e
}

// OpenBlockers is what keeps the storefront of st closed today, read on the
// pool (the settings page shows it). paymentReady is the caller's payment
// pre-flight verdict for the shop's channel. An empty list means it can be
// opened.
func (s *Service) OpenBlockers(ctx context.Context, st SettingsView, paymentReady bool) ([]string, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	active, err := s.q.ShopHasActiveProduct(ctx, st.OrgID)
	if err != nil {
		return nil, err
	}
	return blockers(st.Configured, active, paymentReady), nil
}

// checkOpenTx refuses an opening of a saved shop that has nothing to sell or
// no way to take the money, on the caller's transaction.
func checkOpenTx(ctx context.Context, q *gen.Queries, orgID uuid.UUID, saved, paymentReady bool) error {
	active, err := q.ShopHasActiveProduct(ctx, orgID)
	if err != nil {
		return err
	}
	if list := blockers(saved, active, paymentReady); len(list) > 0 {
		return cannotOpen(list)
	}
	return nil
}

// SetStorefront opens (true) or closes (false) the storefront without
// touching the rest of the settings. Opening needs a saved settings row, an
// active product and paymentReady (422 shop.cannot_open with every blocker
// otherwise); closing always succeeds, also when there is nothing to close.
func (s *Service) SetStorefront(ctx context.Context, orgID uuid.UUID, enabled, paymentReady bool) (SettingsView, error) {
	err := s.inTx(ctx, func(_ pgx.Tx, q *gen.Queries) error {
		cur, err := q.GetShopSettingsForUpdate(ctx, orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			if !enabled {
				return nil
			}
			return checkOpenTx(ctx, q, orgID, false, paymentReady)
		}
		if err != nil {
			return err
		}
		if cur.Enabled == enabled {
			return nil
		}
		if enabled {
			if err := checkOpenTx(ctx, q, orgID, true, paymentReady); err != nil {
				return err
			}
		}
		_, err = q.SetShopSettingsEnabled(ctx, orgID, enabled)
		return err
	})
	if err != nil {
		return SettingsView{}, err
	}
	return s.GetSettings(ctx, orgID)
}

// ShopMode is the organization's shop mode today (orgmodules.ShopMode on the
// pool).
func (s *Service) ShopMode(ctx context.Context, orgID uuid.UUID) (orgmodules.Mode, error) {
	if err := s.ready(); err != nil {
		return orgmodules.ModeNone, err
	}
	return orgmodules.ShopMode(ctx, s.q, orgID, time.Now())
}

// moduleClosed reports whether the shop module is NOT connected on the UTC
// day of now: the storefront of such an organization is closed whatever
// enabled says (spec 61 section 4.2).
func moduleClosed(ctx context.Context, q *gen.Queries, orgID uuid.UUID, now time.Time) (bool, error) {
	active, err := orgmodules.Active(ctx, q, orgID, orgmodules.ModuleShop, now)
	if err != nil {
		return false, err
	}
	return !active, nil
}

// errEnabledNotAllowed refuses a change of enabled by a caller the HTTP layer
// did not allow to make it.
var errEnabledNotAllowed = newErr(http.StatusForbidden, "shop.enabled_not_allowed",
	"opening or closing the storefront needs the shop.manage permission in this organization")
