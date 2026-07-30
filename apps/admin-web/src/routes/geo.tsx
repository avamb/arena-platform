/**
 * Geo Registry administration (AB-27 / feature #413).
 *
 * The registry is the durable source for the country and city pickers used by
 * organization, venue, and event forms. It deliberately renders only records
 * returned by the API: there is no seeded browser-side or development data.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type CSSProperties, type FormEvent } from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { ResponsiveTable, type ResponsiveTableColumn } from "@/components/layout";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/geo",
  component: GeoRoute,
});

export interface GeoCountry {
  readonly id: string;
  readonly iso2: string;
  readonly iso3: string;
  readonly slug: string;
  readonly name: string;
}

export interface GeoCity {
  readonly id: string;
  readonly country_id: string;
  readonly country_iso2: string;
  readonly slug: string;
  readonly name: string;
}

interface CountriesEnvelope { readonly countries: readonly GeoCountry[] }
interface CitiesEnvelope { readonly cities: readonly GeoCity[] }
interface CountryEnvelope { readonly country: GeoCountry }
interface CityEnvelope { readonly city: GeoCity }

const GEO_NAV_ENTRY = NAV_BY_PATH["/geo"];
if (GEO_NAV_ENTRY === undefined) throw new Error("geo route: missing /geo navigation entry");

export const GEO_SLUG_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
export function validateGeoSlug(value: string): string | null {
  if (value.trim() === "") return "Slug is required.";
  return GEO_SLUG_RE.test(value.trim()) ? null : "Use lowercase letters, digits, and hyphens.";
}

function GeoRoute() {
  return <RequirePermission entry={GEO_NAV_ENTRY}><GeoRegistry /></RequirePermission>;
}

function GeoRegistry() {
  const client = useQueryClient();
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const countries = useQuery<CountriesEnvelope, ApiError>({
    queryKey: ["geo", "countries"],
    queryFn: () => authedFetch({ method: "GET", path: "/v1/geo/countries" }),
    retry: false,
  });
  const cities = useQuery<CitiesEnvelope, ApiError>({
    queryKey: ["geo", "cities"],
    queryFn: () => authedFetch({ method: "GET", path: "/v1/geo/cities" }),
    retry: false,
  });
  const rows = useMemo(() => [...(countries.data?.countries ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [countries.data]);
  const cityRows = cities.data?.cities ?? [];
  const cityCount = (id: string) => cityRows.filter((city) => city.country_id === id).length;
  const toggle = (id: string) => setExpanded((current) => {
    const next = new Set(current);
    next.has(id) ? next.delete(id) : next.add(id);
    return next;
  });
  const refresh = () => void Promise.all([countries.refetch(), cities.refetch()]);
  const columns: readonly ResponsiveTableColumn<GeoCountry>[] = [
    { id: "country", header: "Country", primary: true, renderCell: (country) => <button type="button" onClick={() => toggle(country.id)} aria-expanded={expanded.has(country.id)} style={linkButtonStyle}>{expanded.has(country.id) ? "Hide" : "Show"} cities · {country.name}</button> },
    { id: "iso", header: "ISO", renderCell: (country) => <code>{country.iso2} / {country.iso3}</code> },
    { id: "slug", header: "Slug", renderCell: (country) => <code>{country.slug}</code> },
    { id: "cities", header: "Cities", align: "right", renderCell: (country) => cityCount(country.id) },
  ];

  return <section aria-labelledby="geo-heading" style={pageStyle}>
    <header style={headerStyle}>
      <div><h1 id="geo-heading" style={headingStyle}>Geo Registry</h1><p style={subStyle}>Maintain the countries and cities consumed by organization country suggestions, venue address forms, and event location pickers.</p></div>
      <button type="button" onClick={refresh} disabled={countries.isFetching || cities.isFetching} style={buttonStyle}>{countries.isFetching || cities.isFetching ? "Refreshing…" : "Refresh"}</button>
    </header>
    <div style={hintStyle}><strong>Usage:</strong> add a country before adding its cities. Venue forms can create a missing city inline, but this page is the authoritative registry.</div>
    <div style={formGridStyle}><CountryForm onCreated={() => void client.invalidateQueries({ queryKey: ["geo"] })} /><CityForm countries={rows} onCreated={() => void client.invalidateQueries({ queryKey: ["geo", "cities"] })} /></div>
    {countries.isError || cities.isError ? <p role="alert" style={errorStyle}>{(countries.error ?? cities.error)?.message ?? "Unable to load the geo registry."}</p> : null}
    {countries.isLoading || cities.isLoading ? <p role="status">Loading registry…</p> : <>
      <ResponsiveTable id="geo-countries" caption="Countries in the geo registry" columns={columns} rows={rows} rowKey={(country) => country.id} empty="No countries have been added yet." />
      {rows.filter((country) => expanded.has(country.id)).map((country) => <CitiesForCountry key={country.id} country={country} cities={cityRows.filter((city) => city.country_id === country.id)} />)}
    </>}
  </section>;
}

function CountryForm({ onCreated }: { onCreated: () => void }) {
  const [iso2, setIso2] = useState(""); const [iso3, setIso3] = useState(""); const [slug, setSlug] = useState(""); const [nameEn, setNameEn] = useState(""); const [nameRu, setNameRu] = useState("");
  const mutation = useMutation<CountryEnvelope, ApiError>({ mutationFn: () => authedFetch({ method: "POST", path: "/v1/admin/geo/countries", body: { iso2: iso2.trim().toUpperCase(), iso3: iso3.trim().toUpperCase(), slug: slug.trim(), name_en: nameEn.trim(), name_ru: nameRu.trim() } }), onSuccess: () => { setIso2(""); setIso3(""); setSlug(""); setNameEn(""); setNameRu(""); onCreated(); } });
  const submit = (event: FormEvent) => { event.preventDefault(); if (iso2.trim().length === 2 && iso3.trim().length === 3 && validateGeoSlug(slug) === null) mutation.mutate(); };
  return <form onSubmit={submit} style={formStyle} aria-label="Create country"><h2 style={formHeadingStyle}>Add country</h2><label>English name<input required value={nameEn} onChange={(e) => setNameEn(e.target.value)} /></label><label>Russian name <span style={optionalStyle}>(optional)</span><input value={nameRu} onChange={(e) => setNameRu(e.target.value)} /></label><label>ISO alpha-2<input required minLength={2} maxLength={2} value={iso2} onChange={(e) => setIso2(e.target.value.toUpperCase())} /></label><label>ISO alpha-3<input required minLength={3} maxLength={3} value={iso3} onChange={(e) => setIso3(e.target.value.toUpperCase())} /></label><label>Slug<input required value={slug} onChange={(e) => setSlug(e.target.value.toLowerCase())} /></label>{mutation.error ? <p role="alert" style={errorStyle}>{mutation.error.message}</p> : null}<button type="submit" style={primaryStyle} disabled={mutation.isPending}> {mutation.isPending ? "Creating…" : "Create country"}</button></form>;
}

function CityForm({ countries, onCreated }: { countries: readonly GeoCountry[]; onCreated: () => void }) {
  const [countryID, setCountryID] = useState(""); const [slug, setSlug] = useState(""); const [nameEn, setNameEn] = useState(""); const [nameRu, setNameRu] = useState("");
  const mutation = useMutation<CityEnvelope, ApiError>({ mutationFn: () => authedFetch({ method: "POST", path: "/v1/admin/geo/cities", body: { country_id: countryID, slug: slug.trim(), name_en: nameEn.trim(), name_ru: nameRu.trim() } }), onSuccess: () => { setSlug(""); setNameEn(""); setNameRu(""); onCreated(); } });
  const submit = (event: FormEvent) => { event.preventDefault(); if (countryID !== "" && validateGeoSlug(slug) === null && nameEn.trim() !== "") mutation.mutate(); };
  return <form onSubmit={submit} style={formStyle} aria-label="Create city"><h2 style={formHeadingStyle}>Add city</h2><label>Country<select required value={countryID} onChange={(e) => setCountryID(e.target.value)}><option value="">Choose a country</option>{countries.map((country) => <option key={country.id} value={country.id}>{country.name} ({country.iso2})</option>)}</select></label><label>English name<input required value={nameEn} onChange={(e) => setNameEn(e.target.value)} /></label><label>Russian name <span style={optionalStyle}>(optional)</span><input value={nameRu} onChange={(e) => setNameRu(e.target.value)} /></label><label>Slug<input required value={slug} onChange={(e) => setSlug(e.target.value.toLowerCase())} /></label>{countries.length === 0 ? <p style={errorStyle}>Create a country first.</p> : null}{mutation.error ? <p role="alert" style={errorStyle}>{mutation.error.message}</p> : null}<button type="submit" style={primaryStyle} disabled={mutation.isPending || countries.length === 0}>{mutation.isPending ? "Creating…" : "Create city"}</button></form>;
}

function CitiesForCountry({ country, cities }: { country: GeoCountry; cities: readonly GeoCity[] }) {
  return <section aria-label={`Cities in ${country.name}`} style={citiesStyle}><h2 style={citiesHeadingStyle}>Cities in {country.name}</h2>{cities.length === 0 ? <p>No cities registered.</p> : <ul style={cityListStyle}>{[...cities].sort((a, b) => a.name.localeCompare(b.name)).map((city) => <li key={city.id}><strong>{city.name}</strong> <code>{city.slug}</code></li>)}</ul>}</section>;
}

const pageStyle: CSSProperties = { display: "flex", flexDirection: "column", gap: 16, maxWidth: 980 };
const headerStyle: CSSProperties = { display: "flex", alignItems: "start", justifyContent: "space-between", gap: 12, flexWrap: "wrap" };
const headingStyle: CSSProperties = { margin: 0, fontSize: 22, color: "#0f172a" }; const subStyle: CSSProperties = { margin: "6px 0 0", maxWidth: 720, color: "#475569", fontSize: 13, lineHeight: 1.5 };
const hintStyle: CSSProperties = { padding: 12, background: "#eff6ff", border: "1px solid #bfdbfe", borderRadius: 6, color: "#1e3a5f", fontSize: 13 };
const formGridStyle: CSSProperties = { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))", gap: 12 };
const formStyle: CSSProperties = { display: "flex", flexDirection: "column", gap: 8, padding: 14, border: "1px solid #e2e8f0", borderRadius: 6, background: "#fff" };
const formHeadingStyle: CSSProperties = { margin: 0, fontSize: 16 }; const optionalStyle: CSSProperties = { color: "#64748b", fontWeight: "normal" };
const buttonStyle: CSSProperties = { padding: "8px 12px", border: "1px solid #94a3b8", borderRadius: 4, background: "#fff", cursor: "pointer" }; const primaryStyle: CSSProperties = { ...buttonStyle, background: "#0f4c81", borderColor: "#0f4c81", color: "#fff", fontWeight: 600 };
const linkButtonStyle: CSSProperties = { border: 0, padding: 0, background: "transparent", color: "#075985", cursor: "pointer", textAlign: "left", font: "inherit" }; const errorStyle: CSSProperties = { margin: 0, color: "#b91c1c", fontSize: 13 };
const citiesStyle: CSSProperties = { padding: "12px 14px", border: "1px solid #e2e8f0", borderRadius: 6, background: "#f8fafc" }; const citiesHeadingStyle: CSSProperties = { margin: 0, fontSize: 15 }; const cityListStyle: CSSProperties = { margin: "8px 0 0", paddingLeft: 20, display: "grid", gap: 4 };
