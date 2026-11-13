/**
 * The stepwise venue chooser (spec 64 section 2.1.1): a quick row of
 * countries, a world region, a country, a city, a venue, with a search field.
 * Shared by the "Venues" screen and the event wizard. The rules are in
 * lib/venues/chooser.ts (a pure state machine); this file only draws them and
 * reads the directory (lib/venues/directoryApi.ts).
 *
 * `countryOnly` stops after the country: the wizard uses it to choose the
 * country of a NEW venue through the same quick row and regions.
 */
import { useEffect, useMemo, useReducer, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Loading } from "@/components/ui";
import { fetchOrganization } from "@/lib/api/endpoints";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  canCreateNew,
  canGoBack,
  countriesOfRegion,
  countryOf,
  createPrefill,
  filterCities,
  filterVenues,
  hasRegions,
  initialState,
  isVerified,
  mergeCountries,
  quickCountries,
  reduceChooser,
  regionRows,
  searchCountries,
  type ChooserCountry,
  type CreatePrefill,
} from "@/lib/venues/chooser";
import { NO_COUNTRY, defaultSource, isNoCity, type DirectoryVenue, type VenueDirectorySource } from "@/lib/venues/directoryApi";

export interface VenueChooserProps {
  orgId: string;
  /** The id of the venue that is chosen now, if any. */
  value?: string;
  onPick?: (venue: DirectoryVenue) => void;
  /** "My venue is not in the list": the form of a new venue, with what was chosen. Without it the button is not shown. */
  onCreateNew?: (prefill: CreatePrefill) => void;
  source?: VenueDirectorySource;
  /** The country that opens first. Empty = the legal country of the organization, when it can be read. */
  defaultCountry?: string;
  /** Stop after the country (the wizard's new venue). */
  countryOnly?: boolean;
  onPickCountry?: (country: ChooserCountry) => void;
  /** testid prefix, `venue-chooser` by default. */
  testId?: string;
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

const REGION_KEYS: Record<string, MessageKey> = {
  europe: "venues.region.europe",
  middle_east: "venues.region.middle_east",
  asia: "venues.region.asia",
  africa: "venues.region.africa",
  north_america: "venues.region.north_america",
  latin_america: "venues.region.latin_america",
  oceania: "venues.region.oceania",
};

export function VenueChooser({ orgId, value, onPick, onCreateNew, source = defaultSource, defaultCountry, countryOnly = false, onPickCountry, testId = "venue-chooser" }: VenueChooserProps) {
  const { t, locale } = useI18n();
  const [state, dispatch] = useReducer(reduceChooser, undefined, initialState);
  const typed = state.query.trim();
  const debounced = useDebounced(typed, 250);

  const world = useQuery({ queryKey: ["venueChooser", "world", locale], queryFn: () => source.worldCountries(locale), staleTime: Number.POSITIVE_INFINITY, retry: false });
  const base = useQuery({ queryKey: ["venueChooser", "base", orgId, locale], queryFn: () => source.directory(orgId, {}, locale), staleTime: 30_000, retry: false });
  const needOrgCountry = defaultCountry === undefined && !countryOnly;
  const org = useQuery({ queryKey: ["organization", orgId], queryFn: () => fetchOrganization(orgId), enabled: needOrgCountry, staleTime: 5 * 60_000, retry: false });
  const orgCountry = defaultCountry ?? org.data?.organization.legal_address_country ?? "";
  const orgCountryReady = !needOrgCountry || org.isSuccess || org.isError;

  const worldList = world.data ?? [];
  const regions = hasRegions(worldList);
  const all = useMemo(() => {
    const merged = mergeCountries(worldList, base.data?.countries ?? []);
    return countryOnly ? merged.filter((c) => c.iso2 !== NO_COUNTRY) : merged;
  }, [worldList, base.data, countryOnly]);

  // The country of the organization opens first, once the directory has answered.
  const ready = base.isSuccess && !world.isLoading && orgCountryReady;
  useEffect(() => {
    if (ready && !countryOnly) dispatch({ type: "start", defaultCountry: orgCountry, countries: base.data?.countries ?? [] });
  }, [ready, countryOnly, orgCountry, base.data]);

  const inCities = state.step === "city" || state.step === "venue";
  const cities = useQuery({
    queryKey: ["venueChooser", "cities", orgId, locale, state.country],
    queryFn: () => source.directory(orgId, { country: state.country }, locale),
    enabled: !countryOnly && inCities && state.country !== "",
    staleTime: 30_000,
    retry: false,
  });
  const inVenues = state.step === "venue";
  const venues = useQuery({
    queryKey: ["venueChooser", "venues", orgId, locale, state.country, state.cityId],
    queryFn: () => source.directory(orgId, { country: state.country, cityId: state.cityId }, locale),
    enabled: !countryOnly && inVenues && state.cityId !== "",
    staleTime: 30_000,
    retry: false,
  });
  // A name typed at any step above the venues also looks for venues (in the chosen country, once there is one).
  const searching = !countryOnly && !inVenues && debounced.length >= 2;
  const found = useQuery({
    queryKey: ["venueChooser", "found", orgId, locale, state.step === "city" ? state.country : "", debounced],
    queryFn: () => source.directory(orgId, { q: debounced, country: state.step === "city" ? state.country : undefined }, locale),
    enabled: searching,
    staleTime: 30_000,
    retry: false,
  });

  const label = (c: ChooserCountry) => (c.iso2 === NO_COUNTRY ? t("venues.noCountry") : c.name);
  const regionName = (r: string) => (REGION_KEYS[r] !== undefined ? t(REGION_KEYS[r]) : r);
  const chosenCountry = countryOf(all, state.country);
  const prefill = createPrefill(state, all);
  const id = (s: string) => `${testId}-${s}`;

  const pickCountry = (c: ChooserCountry) => {
    if (countryOnly) onPickCountry?.(c);
    else dispatch({ type: "country", iso2: c.iso2 });
  };
  const countryButton = (c: ChooserCountry, kind: "quick" | "country") => (
    <li key={c.iso2}>
      <button type="button" className="btn chooser-chip" onClick={() => pickCountry(c)} data-testid={id(`${kind}-${c.iso2 === NO_COUNTRY ? "none" : c.iso2}`)}>
        <span>{label(c)}</span>
        {c.venues > 0 && <span className="muted chooser-count">{c.venues}</span>}
      </button>
    </li>
  );

  const venueRows = (rows: readonly DirectoryVenue[]) => (
    <ul className="chooser-list" data-testid={id("venues")}>
      {rows.map((v) => (
        <li key={v.id}>
          <button type="button" className="chooser-row" aria-current={value === v.id ? "true" : undefined} onClick={() => onPick?.(v)} data-testid={id(`venue-${v.id}`)}>
            <span className="chooser-row-name">{v.name}</span>
            {(v.city_name !== "" || v.address_line1 !== "") && <span className="muted">{[v.city_name, v.address_line1].filter((x) => x !== "").join(", ")}</span>}
            <span className="chooser-marks">
              {isVerified(v) && (
                <span className="badge badge-ok" data-testid={id(`verified-${v.id}`)}>
                  {t("venues.mark.verified")}
                </span>
              )}
              {v.has_plan === true && (
                <span className="badge badge-wait" data-testid={id(`plan-${v.id}`)}>
                  {t("venues.mark.plan")}
                </span>
              )}
              {v.own && <span className="badge badge-neutral">{t("venues.mark.own")}</span>}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );

  if (base.isLoading || world.isLoading) return <Loading />;
  if (base.isError) return <ErrorNotice error={base.error} testId={id("failed")} />;

  const quick = quickCountries(all, regions, base.data?.quick_pick);
  const hits = state.step === "quick" || state.step === "region" || state.step === "country" ? searchCountries(all, typed) : [];
  const foundVenues = searching ? filterVenues(found.data?.venues ?? [], "") : [];
  const cityList = filterCities(cities.data?.cities ?? [], state.query);
  const venueList = filterVenues(venues.data?.venues ?? [], state.query);

  const title =
    state.step === "quick"
      ? t("venues.step.quick")
      : state.step === "region"
        ? t("venues.step.region")
        : state.step === "country"
          ? t("venues.step.country", { region: regionName(state.region) })
          : state.step === "city"
            ? t("venues.step.city", { country: chosenCountry !== undefined ? label(chosenCountry) : state.country })
            : t("venues.step.venue", { city: state.cityName !== "" ? state.cityName : t("venues.noCity") });

  return (
    <div className="venue-chooser" data-testid={testId}>
      <div className="chooser-head">
        {canGoBack(state) && (
          <button type="button" className="btn btn-link" onClick={() => dispatch({ type: "back" })} data-testid={id("back")}>
            {t("venues.back")}
          </button>
        )}
        <h3 className="chooser-title">{title}</h3>
      </div>

      <label htmlFor={`${testId}-search`}>{t("venues.search")}</label>
      <input
        id={`${testId}-search`}
        type="search"
        autoComplete="off"
        value={state.query}
        onChange={(e) => dispatch({ type: "query", value: e.target.value })}
        data-testid={id("search")}
      />

      {state.step === "quick" && typed === "" && (
        <>
          {quick.length === 0 && <p className="field-hint">{t("venues.empty.quick")}</p>}
          <ul className="chooser-chips" data-testid={id("quick")}>
            {quick.map((c) => countryButton(c, "quick"))}
          </ul>
          {regions && (
            <p>
              <button type="button" className="btn" onClick={() => dispatch({ type: "openRegions" })} data-testid={id("regions")}>
                {t("venues.otherCountries")}
              </button>
            </p>
          )}
        </>
      )}

      {state.step === "region" && typed === "" && (
        <ul className="chooser-list" data-testid={id("regions-list")}>
          {regionRows(all).map((r) => (
            <li key={r.region}>
              <button type="button" className="chooser-row" onClick={() => dispatch({ type: "region", region: r.region })} data-testid={id(`region-${r.region}`)}>
                <span className="chooser-row-name">{regionName(r.region)}</span>
                <span className="muted">{t("venues.region.count", { count: r.countries })}</span>
              </button>
            </li>
          ))}
        </ul>
      )}

      {state.step === "country" && (
        <ul className="chooser-chips" data-testid={id("countries")}>
          {countriesOfRegion(all, state.region, state.query).map((c) => countryButton(c, "country"))}
        </ul>
      )}

      {hits.length > 0 && state.step !== "country" && (
        <ul className="chooser-chips" data-testid={id("country-hits")}>
          {hits.map((c) => countryButton(c, "country"))}
        </ul>
      )}
      {typed !== "" && hits.length === 0 && foundVenues.length === 0 && (state.step === "quick" || state.step === "region" || state.step === "country") && !found.isFetching && (
        <p className="field-hint" data-testid={id("nothing")}>
          {t("venues.empty.search")}
        </p>
      )}

      {state.step === "city" && (
        <>
          {cities.isLoading && <Loading />}
          {cities.isError && <ErrorNotice error={cities.error} testId={id("failed")} />}
          {cities.isSuccess && cityList.length === 0 && (
            <p className="field-hint" data-testid={id("no-cities")}>
              {t(onCreateNew !== undefined ? "venues.empty.cities" : "venues.empty.citiesNoCreate")}
            </p>
          )}
          {chosenCountry !== undefined && !chosenCountry.inReference && (
            <p className="field-hint" data-testid={id("outside")}>
              {t("venues.outside")}
            </p>
          )}
          <ul className="chooser-list" data-testid={id("cities")}>
            {cityList.map((c) => (
              <li key={c.id}>
                <button type="button" className="chooser-row" onClick={() => dispatch({ type: "city", cityId: c.id, cityName: c.name })} data-testid={id(`city-${c.id}`)}>
                  <span className="chooser-row-name">{isNoCity(c.id) ? t("venues.noCity") : c.name}</span>
                  <span className="muted">{t("venues.counter", { count: c.venues })}</span>
                </button>
              </li>
            ))}
          </ul>
        </>
      )}

      {foundVenues.length > 0 && (
        <>
          <h4 className="chooser-sub">{t("venues.found")}</h4>
          {venueRows(foundVenues)}
        </>
      )}

      {state.step === "venue" && (
        <>
          {venues.isLoading && <Loading />}
          {venues.isError && <ErrorNotice error={venues.error} testId={id("failed")} />}
          {venues.isSuccess && venueList.length === 0 && (
            <p className="field-hint" data-testid={id("no-venues")}>
              {t(state.query.trim() === "" ? "venues.empty.venues" : "venues.empty.search")}
            </p>
          )}
          {venueRows(venueList)}
        </>
      )}

      {!countryOnly && onCreateNew !== undefined && canCreateNew(state) && prefill !== null && (
        <p>
          <button type="button" className="btn" onClick={() => onCreateNew(prefill)} data-testid={id("not-listed")}>
            {t("venues.notListed")}
          </button>
        </p>
      )}
    </div>
  );
}
