/**
 * The venue directory: what the stepwise venue chooser reads (spec 64
 * section 2.1.1). ONE module holds every call, so a different final shape of
 * the server routes costs one adapter here.
 *
 * Two sources behind one interface:
 *  - `serverSource`: `GET /v1/geo/world-countries` and
 *    `GET /v1/organizations/{org_id}/venue-directory` (both in openapi.yaml,
 *    typed by the generated client). The server names the country
 *    `country_iso2` and the coordinates `geo_lat`/`geo_lng`; this adapter maps
 *    them to the view model below, so no screen knows the wire names.
 *  - `fallbackSource`: the same view model built in the browser from the
 *    older calls (venues, countries, cities). No regions
 *    (`worldCountries` is empty, so every reference country is offered at the
 *    quick pick), no `quick_pick`, and `has_plan` is `null` (unknown). It only
 *    serves a server that does not have the routes yet (an old stand image).
 * `autoSource` tries the server and switches to the fallback for the rest of
 * the tab when the route is missing (404).
 */
import { ApiError, call } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import { listCountries, listVenues, searchCities } from "@/lib/api/endpoints";
import type { GeoCitiesResponse, GeoWorldCountriesResponse, VenueDirectoryResponse, VenueItem } from "@/lib/api/types";

/** The seven regions of the world, in the order the chooser shows them (spec 64 section 2.1.1). */
export const REGIONS = ["europe", "middle_east", "asia", "africa", "north_america", "latin_america", "oceania"] as const;
export type Region = (typeof REGIONS)[number];

/** A country of the built-in world list (ISO 3166). `in_reference`: the platform has a row for it. */
export interface WorldCountry {
  iso2: string;
  iso3?: string;
  /** The name in the language asked for. */
  name: string;
  /** The English name, always: the search matches it too. */
  name_en: string;
  currency?: string;
  region: string;
  in_reference: boolean;
  /** At least one organization is registered in the country (server only). */
  has_organizers?: boolean;
}

export interface DirectoryCountry {
  iso2: string;
  name: string;
  /** The English name when the source knows it (the fallback does, the server leaves it to the world list). */
  name_en?: string;
  venues: number;
  /** Venues of the organization itself (server only). */
  own_venues?: number;
}

export interface DirectoryCity {
  id: string;
  /** Empty = "no city" (a venue whose city is not set, the fallback only). */
  name: string;
  country: string;
  venues: number;
}

export interface DirectoryVenue {
  id: string;
  name: string;
  /** The venue belongs to the organization. It only labels the row. */
  own: boolean;
  country: string;
  city_id: string | null;
  city_name: string;
  address_line1: string;
  timezone: string;
  latitude: number | null;
  longitude: number | null;
  capacity_default: number | null;
  /** "verified" for a shared venue the platform checked. */
  review_status: string;
  /** An active plan this organization may use (its own, or a verified template it may copy). `null` = the source does not know (the fallback). */
  has_plan: boolean | null;
  /** The organization has its OWN active seating plan at the venue (server only). */
  has_own_plan?: boolean;
}

export interface Directory {
  /** Counters over the whole scope. The server lists countries with at least one venue, the fallback every reference country. */
  countries: DirectoryCountry[];
  /** Filled when `country` is sent. */
  cities: DirectoryCity[];
  /** Filled when any of `country`, `cityId`, `q` is sent. At most about 100 rows, no paging. */
  venues: DirectoryVenue[];
  /** ISO codes of the quick row, in the server's order. Absent = the source has none (the fallback). */
  quick_pick?: string[];
  /** Count of the filtered venues before the limit (server only). */
  total?: number;
}

export interface DirectoryQuery {
  country?: string;
  cityId?: string;
  q?: string;
}

export interface VenueDirectorySource {
  readonly kind: "server" | "fallback";
  worldCountries(locale: string): Promise<WorldCountry[]>;
  directory(orgId: string, query: DirectoryQuery, locale: string): Promise<Directory>;
}

/** The pseudo country of venues that have no country (the fallback only). */
export const NO_COUNTRY = "--";
/** The id prefix of the pseudo city "no city" of a country (the fallback only). */
export const NO_CITY_PREFIX = "nocity:";

export const isNoCity = (cityId: string): boolean => cityId.startsWith(NO_CITY_PREFIX);

/** True for the answer of a server that does not have the route: the web then falls back. */
export function isRouteMissing(err: unknown): boolean {
  return err instanceof ApiError && ((err.status === 404 && (err.code === "http.not_found" || err.code === "http.404")) || err.status === 405);
}

// ─── the server ──────────────────────────────────────────────────────────────

export const serverSource: VenueDirectorySource = {
  kind: "server",
  async worldCountries(locale) {
    const res = await call<GeoWorldCountriesResponse>({ route: API.geoWorldCountries, query: { locale } });
    return (res.countries ?? []).map((c) => ({
      iso2: c.iso2,
      iso3: c.iso3,
      name: c.name,
      name_en: c.name_en,
      currency: c.currency,
      region: c.region,
      in_reference: c.in_reference,
      has_organizers: c.has_organizers,
    }));
  },
  async directory(orgId, query, locale) {
    const res = await call<VenueDirectoryResponse>({
      route: API.venueDirectory,
      params: { org_id: orgId },
      query: { country: query.country, city_id: query.cityId, q: query.q, locale },
    });
    return {
      countries: (res.countries ?? []).map((c) => ({ iso2: c.iso2, name: c.name, venues: c.venues, own_venues: c.own_venues })),
      cities: (res.cities ?? []).map((c) => ({ id: c.id, name: c.name, country: c.country_iso2, venues: c.venues })),
      venues: (res.venues ?? []).map(
        (v): DirectoryVenue => ({
          id: v.id,
          name: v.name,
          own: v.own,
          country: (v.country_iso2 ?? "").toUpperCase(),
          city_id: v.city_id,
          city_name: v.city_name ?? "",
          address_line1: (v.address_line1 ?? v.address ?? "").trim(),
          timezone: v.timezone ?? "",
          latitude: v.geo_lat,
          longitude: v.geo_lng,
          capacity_default: v.capacity_default,
          review_status: v.review_status,
          has_plan: v.has_plan,
          has_own_plan: v.has_own_plan,
        }),
      ),
      quick_pick: (res.quick_pick ?? []).map((c) => c.toUpperCase()),
      total: res.total,
    };
  },
};

// ─── the fallback: the calls that exist today ────────────────────────────────

/** The reads the fallback needs, so a test can hand in fakes. */
export interface FallbackDeps {
  listVenues(orgId: string): Promise<{ venues: VenueItem[] }>;
  listCountries(lang: string): Promise<{ countries: { iso2: string; name: string }[] }>;
  searchCities(iso2: string, q: string, lang: string): Promise<GeoCitiesResponse>;
}

const realDeps: FallbackDeps = { listVenues, listCountries, searchCities };

const norm = (s: string) => s.trim().toLowerCase();

export function directoryVenueOf(v: VenueItem, orgId: string, cityName: string): DirectoryVenue {
  return {
    id: v.id,
    name: v.name,
    own: v.org_id === orgId,
    country: (v.country ?? "").trim().toUpperCase(),
    city_id: v.city_id ?? null,
    city_name: cityName,
    address_line1: (v.address_line1 ?? v.address ?? "").trim(),
    timezone: v.timezone ?? "",
    latitude: typeof v.geo_lat === "number" ? v.geo_lat : null,
    longitude: typeof v.geo_lng === "number" ? v.geo_lng : null,
    capacity_default: v.capacity_default ?? null,
    review_status: v.review_status ?? "",
    has_plan: null,
  };
}

export function fallbackSource(deps: FallbackDeps = realDeps): VenueDirectorySource {
  // Countries and cities are reference data: kept for the life of the source.
  const memo = new Map<string, Promise<unknown>>();
  const once = <T>(key: string, load: () => Promise<T>): Promise<T> => {
    let p = memo.get(key) as Promise<T> | undefined;
    if (p === undefined) {
      p = load();
      memo.set(key, p);
      p.catch(() => memo.delete(key));
    }
    return p;
  };
  const refCountries = (lang: string) => once(`c:${lang}`, () => deps.listCountries(lang).then((r) => r.countries));
  const citiesOf = (iso2: string, lang: string) => once(`k:${iso2}:${lang}`, () => deps.searchCities(iso2, "", lang).then((r) => r.cities));

  const cityNames = async (iso2: string, lang: string): Promise<Map<string, string>> => {
    if (iso2 === "" || iso2 === NO_COUNTRY) return new Map();
    return new Map((await citiesOf(iso2, lang)).map((c) => [c.id, c.name]));
  };

  return {
    kind: "fallback",
    async worldCountries() {
      return [];
    },
    async directory(orgId, query, locale) {
      const all = (await deps.listVenues(orgId)).venues.filter((v) => v.status !== "archived");
      const countryOf = (v: VenueItem) => {
        const c = (v.country ?? "").trim().toUpperCase();
        return c === "" ? NO_COUNTRY : c;
      };

      // Countries: every reference country (so the quick pick offers them all) and the venues' own countries.
      const [local, english] = await Promise.all([refCountries(locale), locale === "en" ? refCountries(locale) : refCountries("en")]);
      const enName = new Map(english.map((c) => [c.iso2.toUpperCase(), c.name]));
      const counts = new Map<string, number>();
      for (const v of all) counts.set(countryOf(v), (counts.get(countryOf(v)) ?? 0) + 1);
      const countries: DirectoryCountry[] = [];
      const seen = new Set<string>();
      for (const c of local) {
        const iso2 = c.iso2.toUpperCase();
        seen.add(iso2);
        countries.push({ iso2, name: c.name, name_en: enName.get(iso2) ?? c.name, venues: counts.get(iso2) ?? 0 });
      }
      for (const [iso2, n] of counts) if (!seen.has(iso2)) countries.push({ iso2, name: iso2 === NO_COUNTRY ? "" : iso2, venues: n });

      // Cities of the chosen country that hold a venue.
      const country = (query.country ?? "").trim().toUpperCase();
      const names = await cityNames(country, locale);
      const cityKey = (v: VenueItem) => (v.city_id != null && names.has(v.city_id) ? v.city_id : `${NO_CITY_PREFIX}${countryOf(v)}`);
      const cities: DirectoryCity[] = [];
      if (country !== "") {
        const byCity = new Map<string, number>();
        for (const v of all) if (countryOf(v) === country) byCity.set(cityKey(v), (byCity.get(cityKey(v)) ?? 0) + 1);
        for (const [id, n] of byCity) cities.push({ id, name: isNoCity(id) ? "" : (names.get(id) ?? ""), country, venues: n });
      }

      // Venues of a city, or found by name.
      const q = norm(query.q ?? "");
      const cityId = query.cityId ?? "";
      let venues: DirectoryVenue[] = [];
      if (cityId !== "" || q !== "") {
        const scoped = all.filter((v) => (country === "" || countryOf(v) === country) && (cityId === "" || cityKey(v) === cityId || v.city_id === cityId) && (q === "" || norm(v.name).includes(q)));
        const nameCache = new Map<string, Map<string, string>>();
        for (const v of scoped) {
          const c = countryOf(v);
          if (!nameCache.has(c)) nameCache.set(c, c === country ? names : await cityNames(c, locale));
        }
        venues = scoped.slice(0, 100).map((v) => directoryVenueOf(v, orgId, nameCache.get(countryOf(v))?.get(v.city_id ?? "") ?? ""));
      }
      return { countries, cities, venues };
    },
  };
}

// ─── the choice between them ─────────────────────────────────────────────────

/**
 * Tries the server and, when a route is missing, answers from the fallback
 * for the rest of the life of this object (the tab).
 */
export function autoSource(server: VenueDirectorySource = serverSource, fallback: VenueDirectorySource = fallbackSource()): VenueDirectorySource {
  let useFallback = false;
  const run = async <T>(f: (s: VenueDirectorySource) => Promise<T>): Promise<T> => {
    if (useFallback) return f(fallback);
    try {
      return await f(server);
    } catch (err) {
      if (!isRouteMissing(err)) throw err;
      useFallback = true;
      return f(fallback);
    }
  };
  return {
    get kind() {
      return useFallback ? fallback.kind : server.kind;
    },
    worldCountries: (locale) => run((s) => s.worldCountries(locale)),
    directory: (orgId, query, locale) => run((s) => s.directory(orgId, query, locale)),
  };
}

/** The source of the tab: every chooser shares it, so the 404 is paid once. */
export const defaultSource: VenueDirectorySource = autoSource();
