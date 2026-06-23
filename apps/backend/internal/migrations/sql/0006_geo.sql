-- +goose Up
-- =====================================================================
-- arena_new — Country & city reference data (Wave 3, feature #123)
--
-- Creates the geo reference tables seeded with ISO country data (focus
-- on IL=Israel and EE=Estonia) and an initial city list. Localized
-- country/city names are stored in i18n_text under the namespaces
-- 'geo.countries' and 'geo.cities'.
--
-- Tables created:
--   * countries  — ISO 3166-1 country reference (iso2 PK slug)
--   * cities     — City reference rows linked to countries via FK
--
-- Seeds:
--   * 10 countries (ISO standard codes, prioritizing IL and EE)
--   * 7 cities for IL and EE
--   * i18n_text rows for en/ru localized names
-- =====================================================================

-- ---------------------------------------------------------------------
-- countries
-- ---------------------------------------------------------------------
CREATE TABLE countries (
    id         uuid     PRIMARY KEY DEFAULT uuidv7(),
    iso2       char(2)  NOT NULL,
    iso3       char(3)  NOT NULL,
    slug       text     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT countries_iso2_uniq UNIQUE (iso2),
    CONSTRAINT countries_slug_uniq UNIQUE (slug),
    CONSTRAINT countries_iso2_upper CHECK (iso2 = upper(iso2)),
    CONSTRAINT countries_iso3_upper CHECK (iso3 = upper(iso3))
);

COMMENT ON TABLE countries IS
    'ISO 3166-1 country reference rows. Wave 3 — Catalog (feature #123).';

COMMENT ON COLUMN countries.iso2 IS
    'ISO 3166-1 alpha-2 code, e.g. "IL", "EE". Unique, uppercase.';

COMMENT ON COLUMN countries.iso3 IS
    'ISO 3166-1 alpha-3 code, e.g. "ISR", "EST". Uppercase.';

COMMENT ON COLUMN countries.slug IS
    'URL-safe unique slug, e.g. "israel", "estonia". Lowercase, hyphenated.';

-- ---------------------------------------------------------------------
-- cities
-- ---------------------------------------------------------------------
CREATE TABLE cities (
    id         uuid     PRIMARY KEY DEFAULT uuidv7(),
    country_id uuid     NOT NULL REFERENCES countries(id) ON DELETE RESTRICT,
    slug       text     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT cities_slug_uniq UNIQUE (slug)
);

COMMENT ON TABLE cities IS
    'City reference rows linked to countries. Wave 3 — Catalog (feature #123).';

COMMENT ON COLUMN cities.slug IS
    'URL-safe unique slug, e.g. "tel-aviv", "tallinn". Lowercase, hyphenated.';

CREATE INDEX cities_country_id_idx ON cities(country_id);

-- ---------------------------------------------------------------------
-- Country seeds (IL + EE focus, plus common countries for completeness)
-- ---------------------------------------------------------------------
INSERT INTO countries (iso2, iso3, slug) VALUES
    ('IL', 'ISR', 'israel'),
    ('EE', 'EST', 'estonia'),
    ('US', 'USA', 'united-states'),
    ('DE', 'DEU', 'germany'),
    ('GB', 'GBR', 'united-kingdom'),
    ('FR', 'FRA', 'france'),
    ('LV', 'LVA', 'latvia'),
    ('LT', 'LTU', 'lithuania'),
    ('FI', 'FIN', 'finland'),
    ('SE', 'SWE', 'sweden')
ON CONFLICT (iso2) DO NOTHING;

-- ---------------------------------------------------------------------
-- City seeds for IL and EE
-- ---------------------------------------------------------------------
INSERT INTO cities (country_id, slug)
SELECT c.id, city.slug
FROM countries c
CROSS JOIN (VALUES
    ('IL', 'tel-aviv'),
    ('IL', 'jerusalem'),
    ('IL', 'haifa'),
    ('EE', 'tallinn'),
    ('EE', 'tartu'),
    ('EE', 'parnu'),
    ('EE', 'narva')
) AS city(iso2, slug)
WHERE c.iso2 = city.iso2
ON CONFLICT (slug) DO NOTHING;

-- ---------------------------------------------------------------------
-- i18n_text: localized country names
-- ---------------------------------------------------------------------
INSERT INTO i18n_text (namespace, key, locale, value) VALUES
    -- Israel
    ('geo.countries', 'IL', 'en', 'Israel'),
    ('geo.countries', 'IL', 'ru', 'Израиль'),
    -- Estonia
    ('geo.countries', 'EE', 'en', 'Estonia'),
    ('geo.countries', 'EE', 'ru', 'Эстония'),
    -- United States
    ('geo.countries', 'US', 'en', 'United States'),
    ('geo.countries', 'US', 'ru', 'Соединённые Штаты'),
    -- Germany
    ('geo.countries', 'DE', 'en', 'Germany'),
    ('geo.countries', 'DE', 'ru', 'Германия'),
    -- United Kingdom
    ('geo.countries', 'GB', 'en', 'United Kingdom'),
    ('geo.countries', 'GB', 'ru', 'Великобритания'),
    -- France
    ('geo.countries', 'FR', 'en', 'France'),
    ('geo.countries', 'FR', 'ru', 'Франция'),
    -- Latvia
    ('geo.countries', 'LV', 'en', 'Latvia'),
    ('geo.countries', 'LV', 'ru', 'Латвия'),
    -- Lithuania
    ('geo.countries', 'LT', 'en', 'Lithuania'),
    ('geo.countries', 'LT', 'ru', 'Литва'),
    -- Finland
    ('geo.countries', 'FI', 'en', 'Finland'),
    ('geo.countries', 'FI', 'ru', 'Финляндия'),
    -- Sweden
    ('geo.countries', 'SE', 'en', 'Sweden'),
    ('geo.countries', 'SE', 'ru', 'Швеция')
ON CONFLICT (namespace, key, locale) DO NOTHING;

-- ---------------------------------------------------------------------
-- i18n_text: localized city names (IL)
-- ---------------------------------------------------------------------
INSERT INTO i18n_text (namespace, key, locale, value) VALUES
    ('geo.cities', 'tel-aviv',   'en', 'Tel Aviv'),
    ('geo.cities', 'tel-aviv',   'ru', 'Тель-Авив'),
    ('geo.cities', 'jerusalem',  'en', 'Jerusalem'),
    ('geo.cities', 'jerusalem',  'ru', 'Иерусалим'),
    ('geo.cities', 'haifa',      'en', 'Haifa'),
    ('geo.cities', 'haifa',      'ru', 'Хайфа'),
    -- EE
    ('geo.cities', 'tallinn',    'en', 'Tallinn'),
    ('geo.cities', 'tallinn',    'ru', 'Таллин'),
    ('geo.cities', 'tartu',      'en', 'Tartu'),
    ('geo.cities', 'tartu',      'ru', 'Тарту'),
    ('geo.cities', 'parnu',      'en', 'Pärnu'),
    ('geo.cities', 'parnu',      'ru', 'Пярну'),
    ('geo.cities', 'narva',      'en', 'Narva'),
    ('geo.cities', 'narva',      'ru', 'Нарва')
ON CONFLICT (namespace, key, locale) DO NOTHING;

-- +goose Down
-- =====================================================================
-- Remove geo data in reverse-dependency order.
-- =====================================================================

DELETE FROM i18n_text
WHERE namespace IN ('geo.countries', 'geo.cities');

DROP TABLE IF EXISTS cities;
DROP TABLE IF EXISTS countries;
