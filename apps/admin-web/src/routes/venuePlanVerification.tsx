/**
 * The tab "Waiting for verification" of the Seating plans screen (spec 64, section 3.4).
 *
 *   GET  /v1/admin/seating-plans?verification_state=unverified|needs_fix|verified
 *   POST /v1/admin/seating-plans/{id}/verify | needs-fix
 *
 * One request gives a page of plans, the total and the count of each status. The
 * filters (status, organization, country, city, venue name) go to the server.
 *
 * Mock data: NONE. Everything here comes from the live backend.
 */
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { ResponsiveTable, type ResponsiveTableColumn } from "@/components/layout";
import { ApiError, authedFetch } from "@/lib/api/client";
import {
  FILTER_OPTIONS,
  INITIAL_PLAN_FILTER,
  PLANS_PAGE_SIZE,
  PLANS_SEARCH_MAX,
  applyFilterChange,
  applyPage,
  cityChoices,
  countryChoices,
  fetchPlanPreviewUrl,
  filterOptionLabel,
  listAdminSeatingPlans,
  placeLabel,
  planNeedsFix,
  rowFromAdminPlan,
  seatsLabel,
  verificationLabel,
  verifyErrorMessage,
  verifyPlan,
  type AdminSeatingPlanPage,
  type GeoCity,
  type GeoCountry,
  type PlanListFilter,
  type VerificationFilter,
  type VerificationRow,
} from "@/lib/api/venuePlans";
import { formatDateTime } from "@/lib/admin/supportConsole";
import { Pager } from "./onboarding";
import {
  Dialog,
  Pill,
  buttonStyle,
  dangerButtonStyle,
  disabledButtonStyle,
  errorStyle,
  fieldErrorStyle,
  hintStyle,
  inputStyle,
  labelStyle,
  mutedStyle,
  noticeStyle,
  primaryButtonStyle,
  sectionStyle,
  sectionTitleStyle,
} from "./onboardingUi";

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

export const NEEDS_FIX_NOTE_MAX = 1000;

/** The note of Needs fix: 1 to 1000 characters after trimming. */
export function validateNeedsFixNote(note: string): string | null {
  const t = note.trim();
  if (t === "") return "Write the correction that the owner must make.";
  if (t.length > NEEDS_FIX_NOTE_MAX) return `Write up to ${NEEDS_FIX_NOTE_MAX} characters.`;
  return null;
}

export function verificationTone(state: string | null): "good" | "warn" | "bad" | "muted" {
  switch (state) {
    case "verified":
      return "good";
    case "needs_fix":
      return "bad";
    case "unverified":
      return "warn";
    default:
      return "muted";
  }
}

// ---------------------------------------------------------------------------
// Table (presentational)
// ---------------------------------------------------------------------------

export interface VerificationTableProps {
  readonly rows: readonly VerificationRow[];
  readonly selectedId: string | null;
  readonly busyId: string | null;
  readonly onPreview: (row: VerificationRow) => void;
  readonly onVerify: (row: VerificationRow) => void;
  readonly onNeedsFix: (row: VerificationRow) => void;
  readonly forceLayout?: "desktop" | "mobile";
}

export function VerificationTable({
  rows,
  selectedId,
  busyId,
  onPreview,
  onVerify,
  onNeedsFix,
  forceLayout,
}: VerificationTableProps): JSX.Element {
  const columns: ResponsiveTableColumn<VerificationRow>[] = [
    {
      id: "plan",
      header: "Plan",
      primary: true,
      renderCell: (r) => (
        <div data-testid={`venue-plans-verify-row-${r.planId}`}>
          <div style={{ fontWeight: 600 }}>{r.name}</div>
          <Pill tone={verificationTone(r.state)}>{verificationLabel(r.state)}</Pill>
          {r.note ? <div style={{ fontSize: 12, color: "#991b1b", marginTop: 2 }}>Note: {r.note}</div> : null}
        </div>
      ),
    },
    { id: "venue", header: "Venue", renderCell: (r) => r.venueName },
    { id: "place", header: "City and country", renderCell: (r) => placeLabel(r.cityName, r.countryIso2) },
    { id: "organization", header: "Organization", renderCell: (r) => r.orgName },
    {
      id: "seats",
      header: "Seats",
      align: "right",
      renderCell: (r) => seatsLabel(r.seats, r.standing),
    },
    { id: "created", header: "Created", renderCell: (r) => formatDateTime(r.createdAt) },
    {
      id: "actions",
      header: "",
      renderCell: (r) => {
        const busy = busyId === r.planId;
        const verified = r.state === "verified";
        return (
          <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
            <button
              type="button"
              style={r.planId === selectedId ? primaryButtonStyle : buttonStyle}
              onClick={() => onPreview(r)}
              data-testid={`venue-plans-preview-${r.planId}`}
            >
              Preview
            </button>
            <button
              type="button"
              style={busy || verified ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
              disabled={busy || verified}
              title={verified ? "The plan is verified." : undefined}
              onClick={() => onVerify(r)}
              data-testid={`venue-plans-verify-${r.planId}`}
            >
              Verify
            </button>
            <button
              type="button"
              style={busy ? { ...buttonStyle, ...disabledButtonStyle } : buttonStyle}
              disabled={busy}
              onClick={() => onNeedsFix(r)}
              data-testid={`venue-plans-needsfix-${r.planId}`}
            >
              Correction necessary
            </button>
          </div>
        );
      },
    },
  ];
  return (
    <ResponsiveTable<VerificationRow>
      id="venue-plans-verify-table"
      caption="Seating plans"
      columns={columns}
      rows={rows}
      rowKey={(r) => r.planId}
      forceLayout={forceLayout}
      empty={<span data-testid="venue-plans-verify-empty">No seating plan matches these filters.</span>}
    />
  );
}

// ---------------------------------------------------------------------------
// Preview: the image of the current version, next to the list
// ---------------------------------------------------------------------------

export function PlanPreviewBox({
  row,
  url,
  loading,
  error,
}: {
  readonly row: VerificationRow;
  readonly url: string | null | undefined;
  readonly loading: boolean;
  readonly error: ApiError | null;
}): JSX.Element {
  return (
    <div style={sectionStyle} data-testid="venue-plans-preview-box">
      <div style={sectionTitleStyle}>
        {row.name} · {row.venueName}
      </div>
      {loading ? <p style={mutedStyle}>Loading the image…</p> : null}
      {error !== null ? <div style={errorStyle}>We did not load the image. Reload the page. {error.message}</div> : null}
      {!loading && error === null && url ? (
        <img
          src={url}
          alt={`Seating plan ${row.name}`}
          style={{ maxWidth: "100%", maxHeight: 520, display: "block", border: "1px solid #e2e8f0", background: "#ffffff" }}
          data-testid="venue-plans-preview-img"
        />
      ) : null}
      {!loading && error === null && !url ? (
        <p style={hintStyle}>The version has no stored SVG. Open the plan on the Venues screen to see its geometry.</p>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Dialog: Needs fix
// ---------------------------------------------------------------------------

function NeedsFixDialog({
  row,
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly row: VerificationRow;
  readonly busy: boolean;
  readonly error: string | null;
  readonly onClose: () => void;
  readonly onConfirm: (note: string) => void;
}): JSX.Element {
  const [note, setNote] = useState("");
  const [touched, setTouched] = useState(false);
  const problem = validateNeedsFixNote(note);
  return (
    <Dialog
      title={`Correction necessary: ${row.name}`}
      testId="venue-plans-needsfix-dialog"
      onClose={onClose}
      onSubmit={() => {
        setTouched(true);
        if (problem === null) onConfirm(note);
      }}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" style={busy ? { ...dangerButtonStyle, ...disabledButtonStyle } : dangerButtonStyle} disabled={busy} data-testid="venue-plans-needsfix-confirm">
            {busy ? "Saving…" : "Send back for correction"}
          </button>
        </>
      }
    >
      <p style={hintStyle}>
        The plan goes back to private. No other organization can copy it. Copies stay in use. The owner sees the
        note.
      </p>
      <label style={labelStyle} htmlFor="venue-plans-needsfix-note">
        Correction necessary
      </label>
      <textarea
        id="venue-plans-needsfix-note"
        value={note}
        onChange={(e) => setNote(e.target.value)}
        rows={4}
        style={inputStyle}
        data-testid="venue-plans-needsfix-note"
      />
      {touched && problem !== null ? <p style={fieldErrorStyle}>{problem}</p> : null}
      {error !== null ? (
        <div style={errorStyle} data-testid="venue-plans-needsfix-error">
          {error}
        </div>
      ) : null}
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Filters (presentational)
// ---------------------------------------------------------------------------

interface OrganizationItem {
  readonly id: string;
  readonly name: string;
}

const fieldLabelStyle = { fontSize: 12, fontWeight: 600, color: "#334155" } as const;
const fieldCaptionStyle = { display: "block", marginBottom: 4 } as const;

export interface VerificationFiltersProps {
  readonly filter: PlanListFilter;
  readonly search: string;
  readonly counts: AdminSeatingPlanPage["counts"] | undefined;
  readonly organizations: readonly OrganizationItem[];
  readonly countries: readonly { readonly value: string; readonly label: string }[];
  readonly cities: readonly { readonly value: string; readonly label: string }[];
  readonly onState: (s: VerificationFilter) => void;
  readonly onOrg: (id: string) => void;
  readonly onCountry: (iso2: string) => void;
  readonly onCity: (id: string) => void;
  readonly onSearch: (text: string) => void;
  readonly onReload: () => void;
}

export function VerificationFilters(p: VerificationFiltersProps): JSX.Element {
  return (
    <div style={{ display: "flex", gap: 12, alignItems: "flex-end", flexWrap: "wrap", marginBottom: 12 }}>
      <label style={fieldLabelStyle}>
        <span style={fieldCaptionStyle}>Status of the plan</span>
        <select
          value={p.filter.state}
          onChange={(e) => p.onState(e.target.value as VerificationFilter)}
          style={{ ...inputStyle, width: 240 }}
          data-testid="venue-plans-verify-filter"
        >
          {FILTER_OPTIONS.map((f) => (
            <option key={f} value={f}>
              {filterOptionLabel(f, p.counts)}
            </option>
          ))}
        </select>
      </label>
      <label style={fieldLabelStyle}>
        <span style={fieldCaptionStyle}>Organization</span>
        <select
          value={p.filter.orgId}
          onChange={(e) => p.onOrg(e.target.value)}
          style={{ ...inputStyle, width: 240 }}
          data-testid="venue-plans-verify-org"
        >
          <option value="">All organizations</option>
          {p.organizations.map((o) => (
            <option key={o.id} value={o.id}>
              {o.name}
            </option>
          ))}
        </select>
      </label>
      <label style={fieldLabelStyle}>
        <span style={fieldCaptionStyle}>Country</span>
        <select
          value={p.filter.country}
          onChange={(e) => p.onCountry(e.target.value)}
          style={{ ...inputStyle, width: 200 }}
          data-testid="venue-plans-verify-country"
        >
          <option value="">All countries</option>
          {p.countries.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
      </label>
      <label style={fieldLabelStyle}>
        <span style={fieldCaptionStyle}>City</span>
        <select
          value={p.filter.cityId}
          onChange={(e) => p.onCity(e.target.value)}
          disabled={p.filter.country === ""}
          style={{ ...inputStyle, width: 200 }}
          data-testid="venue-plans-verify-city"
        >
          <option value="">All cities</option>
          {p.cities.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
      </label>
      <label style={fieldLabelStyle}>
        <span style={fieldCaptionStyle}>Venue name</span>
        <input
          type="search"
          value={p.search}
          maxLength={PLANS_SEARCH_MAX}
          onChange={(e) => p.onSearch(e.target.value)}
          placeholder="Part of the venue name"
          style={{ ...inputStyle, width: 220 }}
          data-testid="venue-plans-verify-search"
        />
      </label>
      <button type="button" style={buttonStyle} onClick={p.onReload} data-testid="venue-plans-verify-reload">
        Reload
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Panel (stateful)
// ---------------------------------------------------------------------------

const SEARCH_DEBOUNCE_MS = 300;

export function VerificationPanel(): JSX.Element {
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState<PlanListFilter>(INITIAL_PLAN_FILTER);
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<VerificationRow | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [needsFixRow, setNeedsFixRow] = useState<VerificationRow | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);

  // The search text reaches the filter 300 ms after the last key; that returns to page one.
  useEffect(() => {
    const t = window.setTimeout(() => {
      setFilter((prev) => (prev.q === search.trim() ? prev : applyFilterChange(prev, { q: search.trim() })));
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(t);
  }, [search]);

  const listQuery = useQuery<AdminSeatingPlanPage, ApiError>({
    queryKey: ["admin", "venue-plans", "verification", filter],
    queryFn: () => listAdminSeatingPlans(filter),
    placeholderData: keepPreviousData,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const orgsQuery = useQuery<readonly OrganizationItem[], ApiError>({
    queryKey: ["admin", "venue-plans", "organizations"],
    queryFn: async () => {
      const res = await authedFetch<{ organizations: OrganizationItem[] }>({ method: "GET", path: "/v1/organizations" });
      return [...res.organizations].sort((a, b) => a.name.localeCompare(b.name));
    },
    refetchOnWindowFocus: false,
    retry: false,
  });

  // The same geo registry the Venues screen reads (GET /v1/geo/cities has the same query key there).
  const countriesQuery = useQuery<{ countries: readonly GeoCountry[] }, ApiError>({
    queryKey: ["geo", "countries", "venue-picker"],
    queryFn: () => authedFetch<{ countries: readonly GeoCountry[] }>({ method: "GET", path: "/v1/geo/countries" }),
    refetchOnWindowFocus: false,
    retry: false,
  });
  const citiesQuery = useQuery<{ cities: readonly GeoCity[] }, ApiError>({
    queryKey: ["geo", "cities", "venue-picker"],
    queryFn: () => authedFetch<{ cities: readonly GeoCity[] }>({ method: "GET", path: "/v1/geo/cities" }),
    refetchOnWindowFocus: false,
    retry: false,
  });
  const countries = useMemo(() => countryChoices(countriesQuery.data?.countries ?? []), [countriesQuery.data]);
  const cities = useMemo(
    () => cityChoices(citiesQuery.data?.cities ?? [], filter.country),
    [citiesQuery.data, filter.country],
  );

  const previewQuery = useQuery<string | null, ApiError>({
    queryKey: ["admin", "venue-plans", "preview", selected?.planId ?? "", selected?.currentVersionId ?? ""],
    queryFn: () => fetchPlanPreviewUrl(selected?.planId ?? "", selected?.currentVersionId ?? null),
    enabled: selected !== null,
    refetchOnWindowFocus: false,
    retry: false,
    staleTime: 5 * 60 * 1000,
  });

  const page = listQuery.data;
  const rows: readonly VerificationRow[] = useMemo(() => (page?.seating_plans ?? []).map(rowFromAdminPlan), [page]);

  const change = (patch: Parameters<typeof applyFilterChange>[1]): void => {
    setFilter((prev) => applyFilterChange(prev, patch));
    setSelected(null);
    setMessage(null);
    setActionError(null);
  };

  const afterChange = async (row: VerificationRow): Promise<void> => {
    if (selected?.planId === row.planId) setSelected(null);
    await queryClient.invalidateQueries({ queryKey: ["admin", "venue-plans", "verification"] });
  };

  const onVerify = async (row: VerificationRow): Promise<void> => {
    setBusyId(row.planId);
    setActionError(null);
    setMessage(null);
    try {
      const res = await verifyPlan(row.planId);
      setMessage(
        res.changed
          ? `Verified “${row.name}”. It is the template of ${row.venueName}.`
          : `“${row.name}” is verified. No change.`,
      );
      await afterChange(row);
    } catch (err) {
      setActionError(err instanceof ApiError ? verifyErrorMessage(err) : String(err));
    } finally {
      setBusyId(null);
    }
  };

  const onNeedsFix = async (row: VerificationRow, note: string): Promise<void> => {
    setBusyId(row.planId);
    setDialogError(null);
    try {
      await planNeedsFix(row.planId, note);
      setMessage(`Sent “${row.name}” back for correction.`);
      setNeedsFixRow(null);
      await afterChange(row);
    } catch (err) {
      setDialogError(err instanceof ApiError ? verifyErrorMessage(err) : String(err));
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div data-testid="venue-plans-verification">
      <VerificationFilters
        filter={filter}
        search={search}
        counts={page?.counts}
        organizations={orgsQuery.data ?? []}
        countries={countries}
        cities={cities}
        onState={(s) => change({ state: s })}
        onOrg={(id) => change({ orgId: id })}
        onCountry={(iso2) => change({ country: iso2 })}
        onCity={(id) => change({ cityId: id })}
        onSearch={setSearch}
        onReload={() => void listQuery.refetch()}
      />

      {message !== null ? (
        <div style={noticeStyle} data-testid="venue-plans-verify-notice">
          {message}
        </div>
      ) : null}
      {actionError !== null ? (
        <div style={errorStyle} data-testid="venue-plans-verify-error">
          {actionError}
        </div>
      ) : null}
      {listQuery.isLoading ? (
        <p style={mutedStyle} data-testid="venue-plans-verify-loading">
          Loading…
        </p>
      ) : null}
      {listQuery.isError ? (
        <div style={errorStyle} data-testid="venue-plans-verify-load-error">
          We did not load the seating plans. {listQuery.error.message}{" "}
          <button type="button" style={buttonStyle} onClick={() => void listQuery.refetch()} data-testid="venue-plans-verify-retry">
            Try again
          </button>
        </div>
      ) : null}

      {page !== undefined ? (
        <>
          <div style={{ display: "grid", gridTemplateColumns: selected !== null ? "minmax(0, 3fr) minmax(0, 2fr)" : "minmax(0, 1fr)", gap: 16, alignItems: "start" }}>
            <VerificationTable
              rows={rows}
              selectedId={selected?.planId ?? null}
              busyId={busyId}
              onPreview={(r) => setSelected(r)}
              onVerify={(r) => void onVerify(r)}
              onNeedsFix={(r) => {
                setDialogError(null);
                setNeedsFixRow(r);
              }}
            />
            {selected !== null ? (
              <PlanPreviewBox
                row={selected}
                url={previewQuery.data}
                loading={previewQuery.isLoading}
                error={previewQuery.error ?? null}
              />
            ) : null}
          </div>
          <Pager
            testIdPrefix="venue-plans-verify"
            total={page.total}
            offset={page.offset}
            shown={rows.length}
            limit={PLANS_PAGE_SIZE}
            onPrev={() => setFilter((prev) => applyPage(prev, prev.offset - PLANS_PAGE_SIZE))}
            onNext={() => setFilter((prev) => applyPage(prev, prev.offset + PLANS_PAGE_SIZE))}
          />
        </>
      ) : null}

      {needsFixRow !== null ? (
        <NeedsFixDialog
          row={needsFixRow}
          busy={busyId === needsFixRow.planId}
          error={dialogError}
          onClose={() => setNeedsFixRow(null)}
          onConfirm={(note) => void onNeedsFix(needsFixRow, note)}
        />
      ) : null}
    </div>
  );
}

