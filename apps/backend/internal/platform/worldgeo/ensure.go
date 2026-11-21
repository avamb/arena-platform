package worldgeo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
)

// ErrUnknownCountry means the code is neither in the arena reference nor in
// the world table.
var ErrUnknownCountry = errors.New("worldgeo: unknown country")

// CountryNamespace is the i18n_text namespace of country names (key = iso2).
const CountryNamespace = "geo.countries"

// Reference is a row of the arena country reference.
type Reference struct {
	ID       uuid.UUID
	Iso2     string
	Iso3     string
	Slug     string
	Currency string
	Region   string
	// NameEn and NameRu come from the world table; they are empty for a
	// reference country the world table does not know.
	NameEn string
	NameRu string
}

// Name is the reference country's name in the locale, the code when the
// world table has no name for it.
func (r Reference) Name(locale string) string {
	c := Country{NameEn: r.NameEn, NameRu: r.NameRu}
	if n := c.Name(locale); n != "" {
		return n
	}
	return r.Iso2
}

const selectReference = `SELECT id, iso2, iso3, slug, currency, region FROM countries WHERE iso2 = $1`

func scanReference(row pgx.Row) (Reference, error) {
	var r Reference
	err := row.Scan(&r.ID, &r.Iso2, &r.Iso3, &r.Slug, &r.Currency, &r.Region)
	r.Currency = strings.TrimSpace(r.Currency)
	return r, err
}

// Ensure returns the reference row of the country with the alpha-2 code,
// creating it from the world table on tx when the reference does not have it
// (iso3, slug, currency, region, and the English and Russian names in
// i18n_text). created is true only when this call inserted the row; the
// caller then queues the "new country" notification on the same tx. A code
// the reference does not have and the world table does not know is
// ErrUnknownCountry. Two transactions racing for the same code both succeed:
// the loser waits on the winner's row and reads it back with created=false.
func Ensure(ctx context.Context, tx pgx.Tx, iso2 string) (Reference, bool, error) {
	iso2 = strings.ToUpper(strings.TrimSpace(iso2))
	world, inWorld := Lookup(iso2)

	ref, err := scanReference(tx.QueryRow(ctx, selectReference, iso2))
	switch {
	case err == nil:
		ref.NameEn, ref.NameRu = world.NameEn, world.NameRu
		return ref, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Reference{}, false, fmt.Errorf("worldgeo: read country %s: %w", iso2, err)
	}
	if !inWorld {
		return Reference{}, false, ErrUnknownCountry
	}

	slug, err := freeSlug(ctx, tx, world)
	if err != nil {
		return Reference{}, false, err
	}
	ref, err = scanReference(tx.QueryRow(ctx, `
INSERT INTO countries (iso2, iso3, slug, currency, region)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (iso2) DO NOTHING
RETURNING id, iso2, iso3, slug, currency, region`, world.Iso2, world.Iso3, slug, world.Currency, world.Region))
	if errors.Is(err, pgx.ErrNoRows) {
		// Another transaction created it first.
		ref, err = scanReference(tx.QueryRow(ctx, selectReference, iso2))
		if err != nil {
			return Reference{}, false, fmt.Errorf("worldgeo: read country %s: %w", iso2, err)
		}
		ref.NameEn, ref.NameRu = world.NameEn, world.NameRu
		return ref, false, nil
	}
	if err != nil {
		return Reference{}, false, fmt.Errorf("worldgeo: insert country %s: %w", iso2, err)
	}
	if err := UpsertI18nName(ctx, tx, CountryNamespace, world.Iso2, world.NameEn, world.NameRu); err != nil {
		return Reference{}, false, fmt.Errorf("worldgeo: country names %s: %w", iso2, err)
	}
	ref.NameEn, ref.NameRu = world.NameEn, world.NameRu
	return ref, true, nil
}

// freeSlug is the slug of the English name, with the code appended when an
// other country already holds it (countries_slug_uniq is global).
func freeSlug(ctx context.Context, tx pgx.Tx, c Country) (string, error) {
	base := geoslug.Slugify(c.NameEn)
	if base == "" {
		base = strings.ToLower(c.Iso2)
	}
	for _, candidate := range []string{base, base + "-" + strings.ToLower(c.Iso2), base + "-" + strings.ToLower(c.Iso3)} {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM countries WHERE slug = $1)`, candidate).Scan(&taken); err != nil {
			return "", fmt.Errorf("worldgeo: check slug: %w", err)
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("worldgeo: no free slug for %s", c.Iso2)
}

// upsertI18nSQL writes one localized name, replacing a stored one.
const upsertI18nSQL = `
INSERT INTO i18n_text (namespace, key, locale, value)
VALUES ($1, $2, $3, $4)
ON CONFLICT (namespace, key, locale)
DO UPDATE SET value = EXCLUDED.value, updated_at = now()`

// UpsertI18nName inserts or updates the English and Russian i18n_text rows of
// namespace + key on tx. An empty name leaves its row untouched. The admin geo
// routes (hgeo) and Ensure share it.
func UpsertI18nName(ctx context.Context, tx pgx.Tx, namespace, key, nameEn, nameRu string) error {
	if nameEn != "" {
		if _, err := tx.Exec(ctx, upsertI18nSQL, namespace, key, "en", nameEn); err != nil {
			return err
		}
	}
	if nameRu != "" {
		if _, err := tx.Exec(ctx, upsertI18nSQL, namespace, key, "ru", nameRu); err != nil {
			return err
		}
	}
	return nil
}
