/**
 * The venue chooser as a PURE state machine (spec 64 section 2.1.1): a
 * reducer and selectors, no React, no calls. The component feeds it what the
 * directory source answered.
 *
 * Steps:
 *   quick   a row of countries (where the organization has venues first),
 *           a button "Other countries" and a search field;
 *   region  the seven regions of the world;
 *   country the countries of one region;
 *   city    the cities of the chosen country, with counters;
 *   venue   the venues of the chosen city.
 * "Back" goes one step back by the way the person came. The search field
 * filters the list of the current step by name (in the language of the
 * interface and in English for a country).
 */
import {
  NO_COUNTRY,
  REGIONS,
  isNoCity,
  type DirectoryCity,
  type DirectoryCountry,
  type DirectoryVenue,
  type WorldCountry,
} from "@/lib/venues/directoryApi";

export type Step = "quick" | "region" | "country" | "city" | "venue";

export interface ChooserState {
  step: Step;
  /** The region opened at the "country" step. */
  region: string;
  /** ISO code of the chosen country ("" until chosen). */
  country: string;
  cityId: string;
  cityName: string;
  /** How the person reached the city step: from the quick row, or through a region. */
  via: "quick" | "region";
  /** What the person typed in the search field. */
  query: string;
  /** The default country was applied (once). */
  started: boolean;
}

export type ChooserAction =
  /** The first answer of the directory: opens the organization's country when it has venues. */
  | { type: "start"; defaultCountry: string; countries: readonly DirectoryCountry[] }
  | { type: "openRegions" }
  | { type: "region"; region: string }
  | { type: "country"; iso2: string }
  | { type: "city"; cityId: string; cityName: string }
  | { type: "back" }
  | { type: "query"; value: string };

export function initialState(): ChooserState {
  return { step: "quick", region: "", country: "", cityId: "", cityName: "", via: "quick", query: "", started: false };
}

export function reduceChooser(s: ChooserState, a: ChooserAction): ChooserState {
  switch (a.type) {
    case "start": {
      if (s.started) return s;
      const want = a.defaultCountry.trim().toUpperCase();
      const hit = want !== "" && a.countries.some((c) => c.iso2.toUpperCase() === want && c.venues > 0);
      if (!hit || s.step !== "quick" || s.query !== "") return { ...s, started: true };
      return { ...s, started: true, step: "city", country: want, via: "quick" };
    }
    case "openRegions":
      return { ...s, step: "region", query: "", started: true };
    case "region":
      return { ...s, step: "country", region: a.region, query: "", started: true };
    case "country":
      return {
        ...s,
        step: "city",
        country: a.iso2.toUpperCase(),
        cityId: "",
        cityName: "",
        via: s.step === "country" ? "region" : "quick",
        query: "",
        started: true,
      };
    case "city":
      return { ...s, step: "venue", cityId: a.cityId, cityName: a.cityName, query: "", started: true };
    case "back":
      switch (s.step) {
        case "venue":
          return { ...s, step: "city", cityId: "", cityName: "", query: "" };
        case "city":
          return s.via === "region" ? { ...s, step: "country", country: "", query: "" } : { ...s, step: "quick", country: "", query: "" };
        case "country":
          return { ...s, step: "region", region: "", query: "" };
        case "region":
          return { ...s, step: "quick", query: "" };
        default:
          return s;
      }
    case "query":
      return { ...s, query: a.value };
  }
}

/** Whether "Back" does anything at this step. */
export const canGoBack = (s: ChooserState): boolean => s.step !== "quick";

// ─── countries ───────────────────────────────────────────────────────────────

/** One country as the chooser shows it: the world list joined with the counters of the directory. */
export interface ChooserCountry {
  iso2: string;
  name: string;
  nameEn: string;
  /** null = the source has no regions (the fallback). */
  region: string | null;
  venues: number;
  inReference: boolean;
}

export function mergeCountries(world: readonly WorldCountry[], counters: readonly DirectoryCountry[]): ChooserCountry[] {
  const byCode = new Map(counters.map((c) => [c.iso2.toUpperCase(), c]));
  const out: ChooserCountry[] = [];
  const seen = new Set<string>();
  for (const w of world) {
    const iso2 = w.iso2.toUpperCase();
    seen.add(iso2);
    out.push({
      iso2,
      name: w.name !== "" ? w.name : w.name_en,
      nameEn: w.name_en,
      region: w.region !== "" ? w.region : null,
      venues: byCode.get(iso2)?.venues ?? 0,
      inReference: w.in_reference,
    });
  }
  for (const [iso2, c] of byCode) {
    if (seen.has(iso2)) continue;
    out.push({ iso2, name: c.name, nameEn: c.name_en ?? c.name, region: null, venues: c.venues, inReference: true });
  }
  return out;
}

/** Lower case, no accents: "México" matches "mexico". */
export function fold(s: string): string {
  return s
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .trim()
    .toLowerCase();
}

/** A country matches by its name in the interface language, its English name or its code. */
export function countryMatches(c: ChooserCountry, query: string): boolean {
  const q = fold(query);
  if (q === "") return true;
  if (c.iso2 === NO_COUNTRY) return false;
  return fold(c.name).includes(q) || fold(c.nameEn).includes(q) || c.iso2.toLowerCase() === q;
}

/** Countries with venues first (the most venues first), then by name. */
function byVenuesThenName(a: ChooserCountry, b: ChooserCountry): number {
  if ((a.venues > 0) !== (b.venues > 0)) return a.venues > 0 ? -1 : 1;
  if (a.venues !== b.venues) return b.venues - a.venues;
  return a.name.localeCompare(b.name);
}

/** The source knows regions when the world list is not empty. */
export const hasRegions = (world: readonly WorldCountry[]): boolean => world.length > 0;

/**
 * The quick row: the countries that have venues. Without regions (the
 * fallback) the other reference countries follow, because there is no other
 * way to reach them.
 */
export function quickCountries(all: readonly ChooserCountry[], regions: boolean): ChooserCountry[] {
  const withVenues = all.filter((c) => c.venues > 0).sort(byVenuesThenName);
  if (regions) return withVenues;
  return [...withVenues, ...all.filter((c) => c.venues === 0 && c.inReference).sort(byVenuesThenName)];
}

export interface RegionRow {
  region: string;
  countries: number;
  venues: number;
}

/** The regions that hold a country, in the order of `REGIONS`. */
export function regionRows(all: readonly ChooserCountry[]): RegionRow[] {
  return REGIONS.map((region) => {
    const inside = all.filter((c) => c.region === region);
    return { region, countries: inside.length, venues: inside.reduce((n, c) => n + c.venues, 0) };
  }).filter((r) => r.countries > 0);
}

/** The countries of a region (those with venues first), filtered by the typed text. */
export function countriesOfRegion(all: readonly ChooserCountry[], region: string, query: string): ChooserCountry[] {
  return all.filter((c) => c.region === region && countryMatches(c, query)).sort(byVenuesThenName);
}

/** The search over every country (the quick and region steps). Empty text finds nothing: the lists show instead. */
export function searchCountries(all: readonly ChooserCountry[], query: string): ChooserCountry[] {
  if (fold(query) === "") return [];
  return all.filter((c) => countryMatches(c, query)).sort(byVenuesThenName);
}

/** A reference country is the only kind a source without regions lists, and an outside one needs the server. */
export const countryOf = (all: readonly ChooserCountry[], iso2: string): ChooserCountry | undefined =>
  all.find((c) => c.iso2 === iso2.toUpperCase());

// ─── cities and venues ───────────────────────────────────────────────────────

export function filterCities(cities: readonly DirectoryCity[], query: string): DirectoryCity[] {
  const q = fold(query);
  return cities
    .filter((c) => q === "" || isNoCity(c.id) || fold(c.name).includes(q))
    .sort((a, b) => {
      if (isNoCity(a.id) !== isNoCity(b.id)) return isNoCity(a.id) ? 1 : -1;
      return b.venues - a.venues || a.name.localeCompare(b.name);
    });
}

export const isVerified = (v: DirectoryVenue): boolean => v.review_status === "verified";

/** Venues by the typed text. Checked venues first, then by name. */
export function filterVenues(venues: readonly DirectoryVenue[], query: string): DirectoryVenue[] {
  const q = fold(query);
  return venues
    .filter((v) => q === "" || fold(v.name).includes(q))
    .sort((a, b) => Number(isVerified(b)) - Number(isVerified(a)) || a.name.localeCompare(b.name));
}

// ─── "My venue is not in the list" ───────────────────────────────────────────

/** What the person has already chosen, carried to the form of a new venue. */
export interface CreatePrefill {
  iso2: string;
  countryName: string;
  cityName: string;
  /** The platform city, when the person picked one (not the pseudo city "no city"). */
  cityId?: string;
  region: string;
}

/** Creating needs a country: from the city step on. */
export const canCreateNew = (s: ChooserState): boolean => (s.step === "city" || s.step === "venue") && s.country !== "" && s.country !== NO_COUNTRY;

export function createPrefill(s: ChooserState, all: readonly ChooserCountry[]): CreatePrefill | null {
  if (!canCreateNew(s)) return null;
  const c = countryOf(all, s.country);
  const out: CreatePrefill = {
    iso2: s.country,
    countryName: c?.name ?? s.country,
    cityName: s.cityName,
    region: c?.region ?? s.region,
  };
  if (s.cityId !== "" && !isNoCity(s.cityId)) out.cityId = s.cityId;
  return out;
}

/** The step a person sees first, for a venue that is already chosen: its card, not the chooser. */
export const isFreshStart = (s: ChooserState): boolean => s.step === "quick" && s.query === "";
