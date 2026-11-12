/**
 * F-004: the venue reads and writes of the wizard's save, on the real API.
 * Kept apart from `save.ts` so the save stays a pure state machine with
 * fakes in its tests.
 */
import { call } from "@/lib/api/http";
import { createOrgCity, createVenue, listCountries, listVenues } from "@/lib/api/endpoints";
import { API } from "@/lib/api/routes";
import type { OrgCityResponse, VenueItem } from "@/lib/api/types";
import type { VenueChoice } from "@/lib/wizard/draft";
import type { VenueApi } from "@/lib/wizard/save";

export function venueChoiceOf(x: VenueItem): VenueChoice {
  return {
    id: x.id,
    name: x.name,
    timezone: x.timezone ?? null,
    capacity: x.capacity_default ?? null,
    country: x.country ?? null,
  };
}

/** `locale` is read at call time: the city is named in the language the person types in. */
export function wizardVenueApi(locale: () => string): VenueApi {
  return {
    listVenues: async (orgId) => (await listVenues(orgId)).venues.map(venueChoiceOf),
    countryId: async (iso2) => {
      const want = iso2.trim().toUpperCase();
      const found = (await listCountries()).countries.find((c) => c.iso2.toUpperCase() === want);
      return found === undefined ? null : found.id;
    },
    createCity: async (orgId, countryId, name) =>
      (await createOrgCity(orgId, { country_id: countryId, name, locale: locale() })).city.id,
    // A country outside the reference (assumed contract, plan-screens-geo-contract section 3):
    // the body names the country by its ISO code instead of `country_id`.
    createCityInCountry: async (orgId, iso2, name) =>
      (
        await call<OrgCityResponse>({
          route: API.orgCityCreate,
          params: { org_id: orgId },
          body: { country_iso2: iso2, name, locale: locale() },
        })
      ).city.id,
    createVenue: async (orgId, body) => venueChoiceOf((await createVenue(orgId, body)).venue),
  };
}
