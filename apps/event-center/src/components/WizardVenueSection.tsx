/**
 * The section "Venue" of the wizard (task 6-1a). Two ways in: pick a venue
 * of the organization, or describe a new one (country, city, name, time
 * zone, seats). The currency follows the country and is shown with the
 * section, because it is decided by the place.
 *
 * Nothing is written here: a new venue is only described. The save (task
 * 6-1c) decides how it is created. The geo reads are public and cached.
 */
import { useEffect, useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { countryCurrency, countryTimezones, listCountries, listVenues, searchCities } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import type { GeoCitiesResponse } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import type { VenueMode } from "@/lib/wizard/draft";
import { SelectField, TextField } from "@/components/FormField";
import type { SectionProps } from "@/components/WizardEventSection";
import { useOrg } from "@/screens/OrgLayout";

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

const FOREVER = { staleTime: Number.POSITIVE_INFINITY, retry: false } as const;

export function WizardVenueSection({ draft, dispatch, touch, errorOf }: SectionProps) {
  const { t, locale } = useI18n();
  const ids = useId();
  const { orgId, access } = useOrg();
  const v = draft.venue;
  const canRead = access.can("venue.read");

  const venues = useQuery({ queryKey: ["venues", orgId], queryFn: () => listVenues(orgId), enabled: canRead });
  const list = useMemo(() => [...(venues.data?.venues ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [venues.data]);
  const chosen = list.find((x) => x.id === v.existingId);

  const countries = useQuery({ queryKey: ["geo", "countries", locale], queryFn: () => listCountries(locale), ...FOREVER });

  // The country that decides the currency: the new venue's, or the chosen venue's own.
  const currencyCountry = v.mode === "new" ? v.countryIso2 : v.mode === "existing" ? (chosen?.country ?? "") : "";
  const currency = useQuery({
    queryKey: ["geo", "currency", currencyCountry],
    queryFn: () => countryCurrency(currencyCountry),
    enabled: currencyCountry.length === 2,
    ...FOREVER,
  });
  useEffect(() => {
    if (currency.data !== undefined) dispatch({ type: "currency", value: currency.data.currency, source: "auto" });
  }, [currency.data, dispatch]);

  const newMode = v.mode === "new";
  const zones = useQuery({
    queryKey: ["geo", "tz", v.countryIso2],
    queryFn: () => countryTimezones(v.countryIso2),
    enabled: newMode && v.countryIso2.length === 2,
    ...FOREVER,
  });
  const zoneList = zones.data?.timezones ?? [];
  const single = zones.data?.single === true;
  // A country with one zone needs no question: the zone is filled in.
  useEffect(() => {
    if (newMode && single && zoneList.length === 1 && v.timezone !== zoneList[0].id) {
      dispatch({ type: "timezone", value: zoneList[0].id });
    }
  }, [newMode, single, zoneList, v.timezone, dispatch]);
  // The data does not know the country: the zone is typed (the server judges it).
  const zonesUnknown = zones.error instanceof ApiError && zones.error.status === 404;

  // City suggestions: after a pause, from two letters, for the chosen country.
  const typedCity = useDebounced(v.cityName.trim(), 300);
  const [showCities, setShowCities] = useState(false);
  const cities = useQuery({
    queryKey: ["geo", "cities", v.countryIso2, typedCity, locale],
    queryFn: () => searchCities(v.countryIso2, typedCity, locale),
    enabled: newMode && v.countryIso2.length === 2 && typedCity.length >= 2,
    staleTime: 60_000,
    retry: false,
  });
  const offered = cityOffers(cities.data);

  const setMode = (mode: VenueMode) => {
    touch("venueExisting");
    dispatch({ type: "venueMode", mode });
  };

  return (
    <fieldset className="org-form" data-testid="wizard-venue">
      <legend>{t("wizard.section.venue")}</legend>
      <div role="radiogroup" aria-label={t("wizard.venue.mode")}>
        {(["existing", "new"] as const).map((m) => (
          <label key={m} className="check">
            <input type="radio" name={`${ids}-mode`} checked={v.mode === m} onChange={() => setMode(m)} data-testid={`wizard-venue-mode-${m}`} />
            <span>{t(m === "existing" ? "wizard.venue.modeExisting" : "wizard.venue.modeNew")}</span>
          </label>
        ))}
      </div>

      {v.mode === "existing" && (
        <>
          {canRead && !venues.isLoading && list.length === 0 && <p className="field-hint">{t("wizard.venue.none")}</p>}
          <SelectField
            label={t("wizard.venue.pick")}
            error={errorOf("venueExisting")}
            value={chosen === undefined ? "" : v.existingId}
            onChange={(id) => {
              touch("venueExisting");
              const found = list.find((x) => x.id === id);
              dispatch({
                type: "venueExisting",
                venue:
                  found === undefined
                    ? null
                    : { id: found.id, name: found.name, timezone: found.timezone ?? null, capacity: found.capacity_default ?? null, country: found.country ?? null },
              });
            }}
            testId="wizard-venue-pick"
          >
            <option value="">{t("wizard.venue.pickPlaceholder")}</option>
            {list.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}
              </option>
            ))}
          </SelectField>
          {chosen !== undefined && (
            <p className="field-hint" data-testid="wizard-venue-facts">
              {t("wizard.venue.facts", {
                tz: v.existingTimezone === "" ? "?" : v.existingTimezone,
                seats: v.existingCapacity === null ? t("wizard.venue.seatsUnknown") : v.existingCapacity,
              })}
            </p>
          )}
        </>
      )}

      {newMode && (
        <>
          <TextField
            label={t("wizard.f.venueName")}
            error={errorOf("venueName")}
            value={v.newName}
            onChange={(value) => {
              touch("venueName");
              dispatch({ type: "venueNew", field: "newName", value });
            }}
            onBlur={() => touch("venueName")}
            testId="wizard-venue-name"
          />
          <SelectField
            label={t("wizard.f.country")}
            error={errorOf("venueCountry")}
            value={v.countryIso2}
            onChange={(iso2) => {
              touch("venueCountry");
              dispatch({ type: "country", iso2 });
              setShowCities(false);
            }}
            testId="wizard-venue-country"
          >
            <option value="">{t("wizard.f.countryPlaceholder")}</option>
            {(countries.data?.countries ?? []).map((c) => (
              <option key={c.iso2} value={c.iso2}>
                {c.name}
              </option>
            ))}
          </SelectField>

          <TextField
            label={t("wizard.f.city")}
            hint={t("wizard.f.cityHint")}
            error={errorOf("venueCity")}
            value={v.cityName}
            onChange={(value) => {
              touch("venueCity");
              dispatch({ type: "venueNew", field: "cityName", value });
              setShowCities(true);
            }}
            onBlur={() => touch("venueCity")}
            testId="wizard-venue-city"
          />
          {showCities && offered.length > 0 && (
            <ul className="suggestions" role="listbox" aria-label={t("wizard.f.city")} data-testid="wizard-city-offers">
              {offered.map((o) => (
                <li key={`${o.name}|${o.timezone}`} role="option" aria-selected={false}>
                  <button
                    type="button"
                    className="btn btn-link"
                    onClick={() => {
                      dispatch({ type: "venueNew", field: "cityName", value: o.name });
                      if (o.timezone !== "" && !single && zoneList.some((z) => z.id === o.timezone)) {
                        dispatch({ type: "timezone", value: o.timezone });
                      }
                      setShowCities(false);
                    }}
                  >
                    {o.name}
                  </button>
                </li>
              ))}
            </ul>
          )}

          {single && zoneList.length === 1 ? (
            <p className="field-hint" data-testid="wizard-tz-single">
              {t("wizard.tz.single", { tz: zoneList[0].id })}
            </p>
          ) : zonesUnknown ? (
            <TextField
              label={t("wizard.f.timezoneText")}
              error={errorOf("venueTimezone")}
              value={v.timezone}
              onChange={(value) => {
                touch("venueTimezone");
                dispatch({ type: "timezone", value: value.trim() });
              }}
              testId="wizard-venue-timezone-text"
            />
          ) : (
            <SelectField
              label={t("wizard.f.timezone")}
              hint={v.countryIso2 === "" ? t("wizard.f.timezoneFirst") : undefined}
              error={errorOf("venueTimezone")}
              value={v.timezone}
              onChange={(value) => {
                touch("venueTimezone");
                dispatch({ type: "timezone", value });
              }}
              testId="wizard-venue-timezone"
            >
              <option value="">{t("wizard.f.timezonePlaceholder")}</option>
              {zoneList.map((z) => (
                <option key={z.id} value={z.id}>
                  {z.label} ({z.id})
                </option>
              ))}
            </SelectField>
          )}

          <TextField
            label={t("wizard.f.capacity")}
            hint={t("wizard.f.capacityHint")}
            error={errorOf("venueCapacity")}
            value={v.capacity}
            onChange={(value) => {
              touch("venueCapacity");
              dispatch({ type: "venueNew", field: "capacity", value });
            }}
            onBlur={() => touch("venueCapacity")}
            testId="wizard-venue-capacity"
          />
        </>
      )}

      {v.mode !== "" && (
        <TextField
          label={t("wizard.f.currency")}
          hint={t("wizard.f.currencyHint")}
          error={errorOf("currency")}
          value={draft.currency}
          onChange={(value) => {
            touch("currency");
            dispatch({ type: "currency", value, source: "typed" });
          }}
          onBlur={() => touch("currency")}
          testId="wizard-currency"
        />
      )}
    </fieldset>
  );
}

interface CityOffer {
  name: string;
  timezone: string;
}

/** Platform cities first, then the GeoNames places the platform does not have yet. */
export function cityOffers(res: GeoCitiesResponse | undefined): CityOffer[] {
  if (res === undefined) return [];
  const out: CityOffer[] = [];
  const seen = new Set<string>();
  const add = (name: string, timezone: string) => {
    const key = name.trim().toLowerCase();
    if (key === "" || seen.has(key)) return;
    seen.add(key);
    out.push({ name, timezone });
  };
  for (const c of res.cities) add(c.name, c.timezone ?? "");
  for (const s of res.suggestions ?? []) add(s.name, s.timezone);
  return out.slice(0, 20);
}
