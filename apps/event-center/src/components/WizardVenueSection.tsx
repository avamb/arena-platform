/**
 * The section "Venue" of the wizard (task 6-1a). Two ways in: pick a venue
 * of the organization, or describe a new one (country, city, name, time
 * zone, seats). The currency follows the country and is shown with the
 * section, because it is decided by the place.
 *
 * Nothing is written here: a new venue is only described. The save creates
 * it before the first date (F-004, lib/wizard/save.ts), with the same fields
 * as the Telegram bot: country, city, name, address, time zone and seats.
 * "A new venue" is offered only to a role that may create one
 * (`venue.create`). The geo reads are public and cached.
 */
import { useEffect, useId, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { countryCurrency, countryTimezones, listCountries, listVenues, searchCities } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import type { GeoCitiesResponse } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import type { VenueMode } from "@/lib/wizard/draft";
import { SelectField, TextField } from "@/components/FormField";
import { VenueChooser } from "@/components/VenueChooser";
import type { CreatePrefill } from "@/lib/venues/chooser";
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
  const canCreate = access.can("venue.create");
  const modes = [...(canRead ? (["existing"] as const) : []), ...(canCreate ? (["new"] as const) : [])];

  const venues = useQuery({ queryKey: ["venues", orgId], queryFn: () => listVenues(orgId), enabled: canRead });
  const list = useMemo(() => [...(venues.data?.venues ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [venues.data]);
  const chosen = list.find((x) => x.id === v.existingId);

  const countries = useQuery({ queryKey: ["geo", "countries", locale], queryFn: () => listCountries(locale), ...FOREVER });
  // What the chooser told us and the list of the organization may not know (a shared venue, a country outside the reference).
  const [pickedCountry, setPickedCountry] = useState("");
  const [countryNames, setCountryNames] = useState<Record<string, string>>({});

  // The country that decides the currency: the new venue's, or the chosen venue's own.
  const currencyCountry =
    v.mode === "new" ? v.countryIso2 : v.mode === "existing" ? (chosen?.country ?? (v.existingCountry !== "" ? v.existingCountry : pickedCountry)) : "";
  const currency = useQuery({
    queryKey: ["geo", "currency", currencyCountry],
    queryFn: () => countryCurrency(currencyCountry),
    enabled: currencyCountry.length === 2,
    ...FOREVER,
  });
  // The currency follows the country. When there is no country, or its currency
  // is not known (still loading, or a 404), the value that came from a country
  // is cleared: it would belong to the previous country. A typed currency is
  // never touched (the reducer keeps it). While the venue list is loading, a
  // chosen venue's country is not known yet, so a restored draft keeps its value.
  const countryKnown = v.mode !== "existing" || !canRead || venues.data !== undefined;
  useEffect(() => {
    if (currencyCountry === "") {
      if (countryKnown) dispatch({ type: "currency", value: "", source: "auto" });
      return;
    }
    dispatch({ type: "currency", value: currency.data?.currency ?? "", source: "auto" });
  }, [currencyCountry, currency.data, countryKnown, dispatch]);

  // A draft restored by a role that cannot list venues cannot keep "existing"
  // (nor "new" without the right to create one).
  useEffect(() => {
    if (!canRead && v.mode === "existing") dispatch({ type: "venueMode", mode: canCreate ? "new" : "" });
    if (!canCreate && v.mode === "new" && canRead) dispatch({ type: "venueMode", mode: "existing" });
  }, [canRead, canCreate, v.mode, dispatch]);

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

  // "My venue is not in the list": the form of a new venue with the country and the city already chosen.
  const startNew = (p: CreatePrefill) => {
    touch("venueExisting");
    setCountryNames((m) => ({ ...m, [p.iso2]: p.countryName }));
    dispatch({ type: "venueMode", mode: "new" });
    dispatch({ type: "country", iso2: p.iso2 });
    if (p.cityName !== "") dispatch({ type: "venueNew", field: "cityName", value: p.cityName });
    setShowCities(false);
  };

  return (
    <fieldset className="org-form" data-testid="wizard-venue">
      <legend>{t("wizard.section.venue")}</legend>
      <div role="radiogroup" aria-label={t("wizard.venue.mode")}>
        {modes.map((m) => (
          <label key={m} className="check">
            <input type="radio" name={`${ids}-mode`} checked={v.mode === m} onChange={() => setMode(m)} data-testid={`wizard-venue-mode-${m}`} />
            <span>{t(m === "existing" ? "wizard.venue.modeExisting" : "wizard.venue.modeNew")}</span>
          </label>
        ))}
      </div>
      {!canRead && canCreate && (
        <p className="field-hint" data-testid="wizard-venue-noread">
          {t("wizard.venue.noRead")}
        </p>
      )}
      {canRead && !canCreate && (
        <p className="field-hint" data-testid="wizard-venue-nocreate">
          {t("wizard.venue.noCreate")}
        </p>
      )}
      {!canRead && !canCreate && (
        <p className="field-hint" data-testid="wizard-venue-noaccess">
          {t("wizard.venue.noAccess")}
        </p>
      )}
      {newMode && (
        <p className="field-hint" data-testid="wizard-venue-new-note">
          {t("wizard.venue.newNote")}
        </p>
      )}

      {v.mode === "existing" && canRead && (
        <>
          {v.existingId === "" ? (
            <>
              <VenueChooser
                orgId={orgId}
                value={v.existingId}
                onPick={(d) => {
                  touch("venueExisting");
                  setPickedCountry(d.country);
                  dispatch({
                    type: "venueExisting",
                    venue: { id: d.id, name: d.name, timezone: d.timezone === "" ? null : d.timezone, capacity: d.capacity_default, country: d.country === "" ? null : d.country },
                  });
                }}
                onCreateNew={canCreate ? startNew : undefined}
              />
              {errorOf("venueExisting") !== undefined && (
                <p className="field-error" data-testid="wizard-venue-pick-error">
                  {errorOf("venueExisting")}
                </p>
              )}
            </>
          ) : (
            <>
              <p data-testid="wizard-venue-chosen">
                <strong>{v.existingName}</strong>{" "}
                <button
                  type="button"
                  className="btn btn-link"
                  onClick={() => {
                    touch("venueExisting");
                    dispatch({ type: "venueExisting", venue: null });
                  }}
                  data-testid="wizard-venue-change"
                >
                  {t("venues.change")}
                </button>
              </p>
              <p className="field-hint" data-testid="wizard-venue-facts">
                {t("wizard.venue.facts", {
                  tz: v.existingTimezone === "" ? "?" : v.existingTimezone,
                  seats: v.existingCapacity === null ? t("wizard.venue.seatsUnknown") : v.existingCapacity,
                })}
              </p>
            </>
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
          {v.countryIso2 === "" ? (
            <div role="group" aria-label={t("wizard.venue.countryChoose")} data-testid="wizard-venue-country-chooser">
              <p>{t("wizard.venue.countryChoose")}</p>
              <VenueChooser
                orgId={orgId}
                countryOnly
                testId="venue-chooser"
                onPickCountry={(c) => {
                  touch("venueCountry");
                  setCountryNames((m) => ({ ...m, [c.iso2]: c.name }));
                  dispatch({ type: "country", iso2: c.iso2 });
                  setShowCities(false);
                }}
              />
              {errorOf("venueCountry") !== undefined && (
                <p className="field-error" data-testid="wizard-venue-country-error">
                  {errorOf("venueCountry")}
                </p>
              )}
            </div>
          ) : (
            <p data-testid="wizard-venue-country">
              {t("wizard.venue.countryPicked", {
                country: countryNames[v.countryIso2] ?? (countries.data?.countries ?? []).find((c) => c.iso2.toUpperCase() === v.countryIso2.toUpperCase())?.name ?? v.countryIso2,
              })}{" "}
              <button
                type="button"
                className="btn btn-link"
                onClick={() => {
                  touch("venueCountry");
                  dispatch({ type: "country", iso2: "" });
                  setShowCities(false);
                }}
                data-testid="wizard-venue-country-change"
              >
                {t("wizard.venue.countryChange")}
              </button>
            </p>
          )}

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
              onBlur={() => touch("venueTimezone")}
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
            label={t("wizard.f.address")}
            hint={t("wizard.f.addressHint")}
            error={errorOf("venueAddress")}
            value={v.address}
            onChange={(value) => {
              touch("venueAddress");
              dispatch({ type: "venueNew", field: "address", value });
            }}
            onBlur={() => touch("venueAddress")}
            testId="wizard-venue-address"
          />

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
