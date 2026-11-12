import { describe, expect, it } from "vitest";
import {
  canCreateNew,
  canGoBack,
  countriesOfRegion,
  createPrefill,
  filterCities,
  filterVenues,
  initialState,
  mergeCountries,
  quickCountries,
  reduceChooser,
  regionRows,
  searchCountries,
  type ChooserAction,
  type ChooserState,
} from "@/lib/venues/chooser";
import type { DirectoryCity, DirectoryCountry, DirectoryVenue, WorldCountry } from "@/lib/venues/directoryApi";

const world: WorldCountry[] = [
  { iso2: "ES", name: "Испания", name_en: "Spain", region: "europe", in_reference: true },
  { iso2: "CZ", name: "Чехия", name_en: "Czechia", region: "europe", in_reference: true },
  { iso2: "FR", name: "Франция", name_en: "France", region: "europe", in_reference: false },
  { iso2: "MY", name: "Малайзия", name_en: "Malaysia", region: "asia", in_reference: false },
  { iso2: "JP", name: "Япония", name_en: "Japan", region: "asia", in_reference: false },
];
const counters: DirectoryCountry[] = [
  { iso2: "ES", name: "Испания", venues: 41 },
  { iso2: "CZ", name: "Чехия", venues: 3 },
];
const all = mergeCountries(world, counters);

const run = (...actions: ChooserAction[]): ChooserState => actions.reduce(reduceChooser, initialState());

function venue(id: string, name: string, extra: Partial<DirectoryVenue> = {}): DirectoryVenue {
  return {
    id,
    name,
    own: false,
    country: "ES",
    city_id: "c1",
    city_name: "Madrid",
    address_line1: "",
    timezone: "Europe/Madrid",
    latitude: null,
    longitude: null,
    capacity_default: null,
    review_status: "",
    has_plan: null,
    ...extra,
  };
}

describe("the steps", () => {
  it("starts at the quick row with nothing chosen", () => {
    const s = initialState();
    expect(s.step).toBe("quick");
    expect(canGoBack(s)).toBe(false);
  });

  it("goes quick -> city -> venue and back the same way", () => {
    const city = run({ type: "country", iso2: "es" });
    expect(city).toMatchObject({ step: "city", country: "ES", via: "quick" });
    const venues = reduceChooser(city, { type: "city", cityId: "c1", cityName: "Madrid" });
    expect(venues).toMatchObject({ step: "venue", cityId: "c1", cityName: "Madrid" });
    const back1 = reduceChooser(venues, { type: "back" });
    expect(back1).toMatchObject({ step: "city", cityId: "" });
    const back2 = reduceChooser(back1, { type: "back" });
    expect(back2).toMatchObject({ step: "quick", country: "" });
    expect(reduceChooser(back2, { type: "back" })).toBe(back2);
  });

  it("goes through a region and comes back through it", () => {
    const s = run({ type: "openRegions" }, { type: "region", region: "asia" }, { type: "country", iso2: "MY" });
    expect(s).toMatchObject({ step: "city", country: "MY", via: "region", region: "asia" });
    const back = reduceChooser(s, { type: "back" });
    expect(back).toMatchObject({ step: "country", region: "asia", country: "" });
    expect(reduceChooser(back, { type: "back" }).step).toBe("region");
    expect(run({ type: "openRegions" }, { type: "back" }).step).toBe("quick");
  });

  it("clears the typed text on every step", () => {
    const s = run({ type: "query", value: "mad" }, { type: "country", iso2: "ES" });
    expect(s.query).toBe("");
    expect(reduceChooser(reduceChooser(s, { type: "query", value: "x" }), { type: "city", cityId: "c", cityName: "C" }).query).toBe("");
  });
});

describe("the default country", () => {
  it("opens the country of the organization when it has venues", () => {
    expect(run({ type: "start", defaultCountry: "es", countries: counters })).toMatchObject({ step: "city", country: "ES", started: true });
  });
  it("stays at the quick row when the country has no venues, and only starts once", () => {
    const s = run({ type: "start", defaultCountry: "FR", countries: counters });
    expect(s).toMatchObject({ step: "quick", started: true });
    expect(reduceChooser(s, { type: "start", defaultCountry: "ES", countries: counters })).toBe(s);
  });
  it("does not jump while the person has already typed", () => {
    const s = run({ type: "query", value: "te" }, { type: "start", defaultCountry: "ES", countries: counters });
    expect(s.step).toBe("quick");
  });
});

describe("the countries", () => {
  it("joins the world list with the counters, venues first", () => {
    expect(all.find((c) => c.iso2 === "ES")).toMatchObject({ venues: 41, region: "europe", inReference: true });
    expect(all.find((c) => c.iso2 === "MY")).toMatchObject({ venues: 0, inReference: false });
    expect(quickCountries(all, true).map((c) => c.iso2)).toEqual(["ES", "CZ"]);
    expect(countriesOfRegion(all, "europe", "").map((c) => c.iso2)).toEqual(["ES", "CZ", "FR"]);
  });

  it("offers every reference country at the quick row when there are no regions", () => {
    const merged = mergeCountries([], [
      { iso2: "ES", name: "Spain", name_en: "Spain", venues: 2 },
      { iso2: "CZ", name: "Czechia", name_en: "Czechia", venues: 0 },
      { iso2: "IL", name: "Israel", name_en: "Israel", venues: 0 },
    ]);
    expect(quickCountries(merged, false).map((c) => c.iso2)).toEqual(["ES", "CZ", "IL"]);
    expect(regionRows(merged)).toEqual([]);
  });

  it("lists the regions that hold a country, in the fixed order", () => {
    expect(regionRows(all)).toEqual([
      { region: "europe", countries: 3, venues: 44 },
      { region: "asia", countries: 2, venues: 0 },
    ]);
  });

  it("searches by the name in the interface language, in English, by code, without accents", () => {
    expect(searchCountries(all, "малай").map((c) => c.iso2)).toEqual(["MY"]);
    expect(searchCountries(all, "Malay").map((c) => c.iso2)).toEqual(["MY"]);
    expect(searchCountries(all, "jp").map((c) => c.iso2)).toEqual(["JP"]);
    expect(searchCountries(mergeCountries([{ iso2: "MX", name: "México", name_en: "Mexico", region: "north_america", in_reference: true }], []), "mexi")).toHaveLength(1);
    expect(searchCountries(all, "")).toEqual([]);
    expect(countriesOfRegion(all, "europe", "spai").map((c) => c.iso2)).toEqual(["ES"]);
  });

  it("keeps a country outside the reference as such", () => {
    const my = searchCountries(all, "Malaysia")[0];
    expect(my.inReference).toBe(false);
    const s = run({ type: "openRegions" }, { type: "region", region: "asia" }, { type: "country", iso2: "MY" });
    expect(createPrefill(s, all)).toEqual({ iso2: "MY", countryName: "Малайзия", cityName: "", region: "asia" });
  });
});

describe("cities and venues", () => {
  const cities: DirectoryCity[] = [
    { id: "nocity:ES", name: "", country: "ES", venues: 9 },
    { id: "b", name: "Barcelona", country: "ES", venues: 5 },
    { id: "m", name: "Madrid", country: "ES", venues: 12 },
  ];
  it("lists cities by the counter, with the pseudo city last, and filters by name", () => {
    expect(filterCities(cities, "").map((c) => c.id)).toEqual(["m", "b", "nocity:ES"]);
    expect(filterCities(cities, "barc").map((c) => c.id)).toEqual(["b", "nocity:ES"]);
  });
  it("lists verified venues first and filters by name", () => {
    const rows = [venue("1", "Zeta"), venue("2", "Alfa"), venue("3", "Mega", { review_status: "verified" })];
    expect(filterVenues(rows, "").map((v) => v.id)).toEqual(["3", "2", "1"]);
    expect(filterVenues(rows, "ALF").map((v) => v.id)).toEqual(["2"]);
  });
});

describe("My venue is not in the list", () => {
  it("is offered from the city step on, and carries country, city and region", () => {
    expect(canCreateNew(initialState())).toBe(false);
    const city = run({ type: "country", iso2: "ES" });
    expect(canCreateNew(city)).toBe(true);
    expect(createPrefill(city, all)).toEqual({ iso2: "ES", countryName: "Испания", cityName: "", region: "europe" });
    const venues = reduceChooser(city, { type: "city", cityId: "c1", cityName: "Madrid" });
    expect(createPrefill(venues, all)).toEqual({ iso2: "ES", countryName: "Испания", cityName: "Madrid", cityId: "c1", region: "europe" });
  });
  it("does not carry the pseudo city or the pseudo country", () => {
    const s = run({ type: "country", iso2: "ES" }, { type: "city", cityId: "nocity:ES", cityName: "" });
    expect(createPrefill(s, all)?.cityId).toBeUndefined();
    expect(canCreateNew(run({ type: "country", iso2: "--" }))).toBe(false);
  });
});
