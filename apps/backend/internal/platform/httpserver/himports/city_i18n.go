// city_i18n.go gives an imported city a human display name (feature #537,
// spec 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.3).
//
// arena's `cities` table has no name column at all: the display name lives in
// i18n_text under the namespace `geo.cities`, keyed by the city slug, and every
// reader resolves it with the same fallback chain
//
//	COALESCE(t_loc.value, t_en.value, ci.slug)
//
// (gen/venues.sql.go ListActionVenuesByOrg, gen/geo.sql.go ListCities,
// orderexport/query.go). The import used to create a city with a slug only, so
// the fallback fired and the site saw `praha` where the operator had typed
// `Praha` — the lowercase city names observed on the live stand (spec §1 item 4).
//
// Writing the translation at creation time closes that: the name the payload
// carries is preserved verbatim (case included) for the organization's own
// locale and for English, and an existing translation is never touched.
package himports

import (
	"context"
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// cityI18nNamespace is the i18n_text namespace every city-name reader joins on.
// It is a compile-time constant of the schema (migration 0006), not a setting.
const cityI18nNamespace = "geo.cities"

// defaultCityLocale is the locale every city name is written under in addition
// to the organization's own, because it is the second link of the readers'
// fallback chain: a city named only in `cs` would still read as the bare slug
// for anyone browsing in another locale.
const defaultCityLocale = "en"

// ensureCityNameTranslations records the payload's city name as the display
// name of citySlug, for the organization's default locale and for English.
//
// It is additive only — `InsertI18nTextIfAbsent` keeps whatever translation is
// already there — so a repeated import writes nothing the second time, and an
// operator who corrects a name through the admin geo endpoints keeps the
// correction even if the site re-posts the old spelling.
//
// A database error is returned rather than downgraded to a warning: the insert
// runs inside the import transaction, where a refused statement poisons the
// whole transaction and would surface as an unexplained failure at COMMIT.
func (h *Handler) ensureCityNameTranslations(ctx context.Context, q *gen.Queries, plan importPlan, citySlug, rawName string) error {
	name := normalizeDisplayName(rawName)
	if citySlug == "" || name == "" {
		return nil
	}

	locales := []string{defaultCityLocale}
	org, err := q.GetOrganizationByID(ctx, plan.OrgID)
	if err != nil {
		return fmt.Errorf("read organization locale for city name: %w", err)
	}
	if loc := normalizeLocale(org.DefaultLocale); loc != "" && loc != defaultCityLocale {
		locales = append(locales, loc)
	}

	for _, locale := range locales {
		if err := q.InsertI18nTextIfAbsent(ctx, cityI18nNamespace, citySlug, locale, name); err != nil {
			return fmt.Errorf("write %s city name for locale %s: %w", citySlug, locale, err)
		}
	}
	return nil
}

// normalizeDisplayName trims the value and collapses every run of whitespace
// (including tabs and newlines a copy-paste may carry) into a single space.
// Case is deliberately preserved: the whole point of the translation is to show
// the name the way the operator wrote it.
func normalizeDisplayName(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}

// normalizeLocale reduces organizations.default_locale to the form i18n_text
// rows are keyed by: lower-case, whitespace-free. An empty or blank value means
// "no organization locale", and only the English row is written.
func normalizeLocale(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
