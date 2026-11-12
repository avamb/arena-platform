import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { GeoCitiesResponse, VenueItem } from "@/lib/api/types";
import {
  NO_COUNTRY,
  autoSource,
  fallbackSource,
  isRouteMissing,
  serverSource,
  type Directory,
  type FallbackDeps,
  type VenueDirectorySource,
} from "@/lib/venues/directoryApi";

function item(id: string, name: string, extra: Partial<VenueItem> = {}): VenueItem {
  return { id, org_id: "org-1", name, status: "active", created_at: "", updated_at: "", ...extra } as VenueItem;
}

const VENUES: VenueItem[] = [
  item("v1", "Teatro Lara", { country: "ES", city_id: "mad", timezone: "Europe/Madrid", geo_lat: 40.4, geo_lng: -3.7, review_status: "verified", capacity_default: 300 }),
  item("v2", "Sala Apolo", { country: "ES", city_id: "bcn", org_id: "other" }),
  item("v3", "Sala Sin Ciudad", { country: "ES" }),
  item("v4", "E2E Hall"),
  item("v5", "Kuala Hall", { country: "MY" }),
  item("v6", "Old Hall", { country: "ES", city_id: "mad", status: "archived" }),
];

function deps(): FallbackDeps & { calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    listVenues: async () => {
      calls.push("venues");
      return { venues: VENUES };
    },
    listCountries: async (lang) => {
      calls.push(`countries:${lang}`);
      return {
        countries: [
          { iso2: "ES", name: lang === "ru" ? "Испания" : "Spain" },
          { iso2: "CZ", name: lang === "ru" ? "Чехия" : "Czechia" },
        ],
      };
    },
    searchCities: async (iso2): Promise<GeoCitiesResponse> => {
      calls.push(`cities:${iso2}`);
      const cities = iso2 === "ES" ? [{ id: "mad", name: "Madrid" }, { id: "bcn", name: "Barcelona" }, { id: "sev", name: "Sevilla" }] : [];
      return { cities } as unknown as GeoCitiesResponse;
    },
  };
}

describe("fallbackSource", () => {
  it("has no regions and does not know the plans", async () => {
    const d = deps();
    const src = fallbackSource(d);
    expect(src.kind).toBe("fallback");
    expect(await src.worldCountries("en")).toEqual([]);
    const dir = await src.directory("org-1", { cityId: "mad", country: "ES" }, "ru");
    expect(dir.venues.map((v) => v.has_plan)).toEqual([null]);
  });

  it("counts venues per country: every reference country, the others, and the venues without a country", async () => {
    const dir = await fallbackSource(deps()).directory("org-1", {}, "ru");
    const by = Object.fromEntries(dir.countries.map((c) => [c.iso2, c.venues]));
    expect(by).toEqual({ ES: 3, CZ: 0, MY: 1, [NO_COUNTRY]: 1 });
    expect(dir.countries.find((c) => c.iso2 === "ES")).toMatchObject({ name: "Испания", name_en: "Spain" });
    expect(dir.cities).toEqual([]);
    expect(dir.venues).toEqual([]);
  });

  it("lists the cities of a country that hold a venue, with counters and the pseudo city", async () => {
    const dir = await fallbackSource(deps()).directory("org-1", { country: "es" }, "en");
    expect(dir.cities).toEqual(
      expect.arrayContaining([
        { id: "mad", name: "Madrid", country: "ES", venues: 1 },
        { id: "bcn", name: "Barcelona", country: "ES", venues: 1 },
        { id: "nocity:ES", name: "", country: "ES", venues: 1 },
      ]),
    );
    expect(dir.cities.find((c) => c.id === "sev")).toBeUndefined();
  });

  it("lists the venues of a city, with the facts of the card", async () => {
    const dir = await fallbackSource(deps()).directory("org-1", { country: "ES", cityId: "mad" }, "en");
    expect(dir.venues).toHaveLength(1);
    expect(dir.venues[0]).toMatchObject({
      id: "v1",
      own: true,
      city_name: "Madrid",
      timezone: "Europe/Madrid",
      latitude: 40.4,
      longitude: -3.7,
      capacity_default: 300,
      review_status: "verified",
    });
    const other = await fallbackSource(deps()).directory("org-1", { country: "ES", cityId: "bcn" }, "en");
    expect(other.venues[0].own).toBe(false);
  });

  it("finds a venue by name in the whole scope and inside a country", async () => {
    const src = fallbackSource(deps());
    expect((await src.directory("org-1", { q: "hall" }, "en")).venues.map((v) => v.id).sort()).toEqual(["v4", "v5"]);
    expect((await src.directory("org-1", { q: "sala", country: "ES" }, "en")).venues.map((v) => v.id).sort()).toEqual(["v2", "v3"]);
  });

  it("reaches a venue with no country through the pseudo country", async () => {
    const dir = await fallbackSource(deps()).directory("org-1", { country: NO_COUNTRY, cityId: `nocity:${NO_COUNTRY}` }, "en");
    expect(dir.venues.map((v) => v.id)).toEqual(["v4"]);
  });

  it("offers a country outside the reference with its venue (in_reference is a world list fact)", async () => {
    const dir = await fallbackSource(deps()).directory("org-1", { country: "MY" }, "en");
    expect(dir.cities).toEqual([{ id: "nocity:MY", name: "", country: "MY", venues: 1 }]);
  });

  it("reads the reference data once", async () => {
    const d = deps();
    const src = fallbackSource(d);
    await src.directory("org-1", { country: "ES" }, "en");
    await src.directory("org-1", { country: "ES" }, "en");
    expect(d.calls.filter((c) => c === "cities:ES")).toHaveLength(1);
    expect(d.calls.filter((c) => c === "countries:en")).toHaveLength(1);
  });
});

describe("serverSource", () => {
  it("reads the real routes and maps country_iso2, geo_lat/geo_lng, quick_pick and total", async () => {
    const calls: { path: string; query?: Record<string, unknown> }[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = String(input);
      calls.push({ path: url });
      const body = url.includes("world-countries")
        ? { regions: [], countries: [{ iso2: "MY", iso3: "MYS", name: "Malaysia", name_en: "Malaysia", currency: "MYR", region: "asia", in_reference: false, country_id: null, has_organizers: false }] }
        : {
            quick_pick: ["es", "IL"],
            countries: [{ iso2: "ES", country_id: "c1", name: "Spain", region: "europe", venues: 2, own_venues: 1 }],
            cities: [{ id: "mad", name: "Madrid", country_iso2: "ES", venues: 2, own_venues: 1 }],
            venues: [
              { id: "v1", name: "Teatro Real", city_id: "mad", city_name: "Madrid", country_iso2: "ES", address: "old", address_line1: null, geo_lat: 40.4, geo_lng: -3.7, timezone: "Europe/Madrid", capacity_default: 1700, own: false, review_status: "verified", has_plan: true, has_own_plan: false },
            ],
            total: 1,
          };
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const world = await serverSource.worldCountries("ru");
    expect(world).toHaveLength(1);
    expect(world[0]).toMatchObject({ iso2: "MY", in_reference: false, region: "asia" });
    const dir = await serverSource.directory("org-1", { country: "ES" }, "ru");
    expect(dir.quick_pick).toEqual(["ES", "IL"]);
    expect(dir.total).toBe(1);
    expect(dir.cities).toEqual([{ id: "mad", name: "Madrid", country: "ES", venues: 2 }]);
    expect(dir.venues[0]).toMatchObject({ country: "ES", latitude: 40.4, longitude: -3.7, address_line1: "old", has_plan: true, has_own_plan: false });
    expect(calls[0].path).toContain("/v1/geo/world-countries");
    expect(calls[1].path).toContain("/v1/organizations/org-1/venue-directory");
    vi.restoreAllMocks();
  });
});

const EMPTY: Directory ={ countries: [], cities: [], venues: [] };

function fakeServer(fail: unknown): VenueDirectorySource & { world: ReturnType<typeof vi.fn>; dir: ReturnType<typeof vi.fn> } {
  const world = vi.fn(async () => {
    if (fail !== null) throw fail;
    return [{ iso2: "MY", name: "Malaysia", name_en: "Malaysia", region: "asia", in_reference: false }];
  });
  const dir = vi.fn(async () => {
    if (fail !== null) throw fail;
    return EMPTY;
  });
  return { kind: "server", world, dir, worldCountries: world, directory: dir };
}

describe("autoSource", () => {
  const missing = new ApiError(404, "http.not_found", "We cannot find this page.");

  it("knows a missing route", () => {
    expect(isRouteMissing(missing)).toBe(true);
    expect(isRouteMissing(new ApiError(405, "http.method_not_allowed", ""))).toBe(true);
    expect(isRouteMissing(new ApiError(404, "geo.country_not_found", ""))).toBe(false);
    expect(isRouteMissing(new ApiError(500, "internal", ""))).toBe(false);
    expect(isRouteMissing(new Error("x"))).toBe(false);
  });

  it("uses the server while it answers", async () => {
    const server = fakeServer(null);
    const auto = autoSource(server, fallbackSource(deps()));
    expect(await auto.worldCountries("en")).toHaveLength(1);
    expect(auto.kind).toBe("server");
  });

  it("switches to the fallback on a 404 and stays there for the tab", async () => {
    const server = fakeServer(missing);
    const d = deps();
    const auto = autoSource(server, fallbackSource(d));
    expect(await auto.worldCountries("en")).toEqual([]);
    expect(auto.kind).toBe("fallback");
    const dir = await auto.directory("org-1", {}, "en");
    expect(dir.countries.length).toBeGreaterThan(0);
    expect(server.world).toHaveBeenCalledTimes(1);
    expect(server.dir).not.toHaveBeenCalled();
  });

  it("does not hide another failure", async () => {
    const auto = autoSource(fakeServer(new ApiError(500, "internal", "boom")), fallbackSource(deps()));
    await expect(auto.directory("org-1", {}, "en")).rejects.toBeInstanceOf(ApiError);
    expect(auto.kind).toBe("server");
  });
});
