package shop

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// CourierSource is everything the courier text of an order is built from. The
// text itself is rendered by salesnotify.FormatShopCourierText, the builder
// the Telegram announcement shares; this package cannot import it (salesnotify
// imports shop), so the HTTP layer joins the two.
type CourierSource struct {
	Order gen.ShopOrderRow
	Items []gen.ShopOrderItemRow
	// Locale is organizations.default_locale ("" = English).
	Locale string
	// ShopCountry is shop_settings.country_iso2, upper case, "" when unset.
	ShopCountry string
}

// CourierSourceFor loads the courier text's inputs for one order of the
// organization. An order of another organization is ErrOrderNotFound.
func (s *Service) CourierSourceFor(ctx context.Context, orgID, id uuid.UUID) (CourierSource, error) {
	if err := s.ready(); err != nil {
		return CourierSource{}, err
	}
	o, err := s.q.GetShopOrder(ctx, id, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CourierSource{}, ErrOrderNotFound
	}
	if err != nil {
		return CourierSource{}, err
	}
	items, err := s.q.ListShopOrderItems(ctx, []uuid.UUID{o.ID})
	if err != nil {
		return CourierSource{}, err
	}
	src := CourierSource{Order: o, Items: items}
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(default_locale, '') FROM organizations WHERE id = $1`, orgID).Scan(&src.Locale); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CourierSource{}, err
	}
	// A shop without settings has no home country: the text then names every
	// country, which is the safe side.
	st, err := s.q.GetShopSettings(ctx, orgID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CourierSource{}, err
	}
	if err == nil && st.CountryISO2 != nil {
		src.ShopCountry = strings.ToUpper(strings.TrimSpace(*st.CountryISO2))
	}
	return src, nil
}
