/**
 * Onboarding — the queue of organizer applications (backlog APP-09, spec
 * 08_architecture/34_onboarding_applications_ru.md §3 and §8).
 *
 *   GET /v1/admin/onboarding/applications?status=&q=&limit=&offset=
 *   GET|PUT /v1/admin/onboarding/settings
 *
 * Six tabs, one per status, each with its count (the server returns the
 * counters of ALL statuses with every page, so they stay right while one tab
 * is filtered). The page is platform_superadmin only (`onboarding.review`);
 * the Settings view needs `onboarding.settings` and is hidden without it.
 * One application's card is /onboarding/$applicationId (onboardingDetail.tsx).
 *
 * Mock data: NONE. The page hits the live backend.
 */
import { createRoute, Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ResponsiveTable, type ResponsiveTableColumn } from "@/components/layout";
import { ApiError } from "@/lib/api/client";
import {
  APPROVAL_MODES,
  APPROVAL_MODE_LABELS,
  AUTO_MODE_WARNING,
  DEFAULT_ONBOARDING_TAB,
  ONBOARDING_PAGE_SIZE,
  ONBOARDING_TABS,
  getOnboardingSettings,
  listOnboardingApplications,
  relativeAge,
  serverFieldErrors,
  settingsToForm,
  sourceLabel,
  tabCount,
  updateOnboardingSettings,
  validateSettings,
  type OnboardingListItem,
  type OnboardingListResponse,
  type OnboardingSettings,
  type OnboardingSettingsRequest,
  type OnboardingStatus,
  type SettingsForm,
} from "@/lib/api/onboarding";
import { formatDateTime } from "@/lib/admin/supportConsole";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { useAuth } from "@/lib/auth/useAuth";
import {
  Pill,
  StatusPill,
  buttonStyle,
  disabledButtonStyle,
  errorStyle,
  fieldErrorStyle,
  headerRowStyle,
  headingStyle,
  hintStyle,
  inputStyle,
  labelStyle,
  linkButtonStyle,
  mutedStyle,
  noticeStyle,
  pageStyle,
  primaryButtonStyle,
  sectionStyle,
  subheadingStyle,
  warningStyle,
} from "./onboardingUi";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/onboarding",
  component: OnboardingRoute,
});

const ONBOARDING_NAV_ENTRY = NAV_BY_PATH["/onboarding"];
if (ONBOARDING_NAV_ENTRY === undefined) {
  throw new Error("onboarding route: NAV_BY_PATH['/onboarding'] missing");
}

function OnboardingRoute() {
  return (
    <RequirePermission entry={ONBOARDING_NAV_ENTRY}>
      <OnboardingPage />
    </RequirePermission>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

type View = "applications" | "settings";

function OnboardingPage(): JSX.Element {
  const { permissions } = useAuth();
  const canSeeSettings = permissions.has("onboarding.settings");
  const [view, setView] = useState<View>("applications");
  const [tab, setTab] = useState<OnboardingStatus>(DEFAULT_ONBOARDING_TAB);
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  const [offset, setOffset] = useState(0);

  useEffect(() => {
    const t = window.setTimeout(() => {
      setDebounced(search);
      setOffset(0);
    }, 300);
    return () => window.clearTimeout(t);
  }, [search]);

  const query = useQuery<OnboardingListResponse, ApiError>({
    queryKey: ["admin", "onboarding", "list", tab, debounced, offset],
    queryFn: () => listOnboardingApplications(tab, debounced, ONBOARDING_PAGE_SIZE, offset),
    enabled: view === "applications",
    refetchOnWindowFocus: false,
    retry: false,
  });

  const data = query.data;

  return (
    <div style={pageStyle} data-testid="onboarding-page">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>Onboarding</h1>
          <p style={subheadingStyle}>
            Applications from organizers who want to sell on arena. Nothing is created for an applicant until an
            operator approves the application.
          </p>
        </div>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          <ViewSwitch view={view} onChange={setView} canSeeSettings={canSeeSettings} />
          {view === "applications" ? (
            <button
              type="button"
              style={buttonStyle}
              onClick={() => void query.refetch()}
              data-testid="onboarding-refresh"
            >
              Refresh
            </button>
          ) : null}
        </div>
      </div>

      {view === "settings" && canSeeSettings ? (
        <OnboardingSettingsPanel />
      ) : (
        <>
          <OnboardingTabs counts={data?.counts} active={tab} onSelect={(s) => { setTab(s); setOffset(0); }} />

          <div style={{ margin: "12px 0" }}>
            <input
              type="search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search by organization, e-mail, name or country"
              aria-label="Search applications"
              style={{ ...inputStyle, maxWidth: 420 }}
              data-testid="onboarding-search"
            />
          </div>

          {query.isLoading ? <p style={mutedStyle}>Loading…</p> : null}
          {query.error ? (
            <div style={errorStyle} data-testid="onboarding-error">
              Could not load applications: {query.error.message}
            </div>
          ) : null}

          {data ? (
            <>
              <OnboardingQueueTable
                items={data.items}
                renderCardLink={(id, children) => (
                  <Link to="/onboarding/$applicationId" params={{ applicationId: id }} data-testid={`onboarding-open-${id}`}>
                    {children}
                  </Link>
                )}
              />
              <Pager
                total={data.total}
                offset={offset}
                shown={data.items.length}
                limit={ONBOARDING_PAGE_SIZE}
                onPrev={() => setOffset(Math.max(0, offset - ONBOARDING_PAGE_SIZE))}
                onNext={() => setOffset(offset + ONBOARDING_PAGE_SIZE)}
              />
            </>
          ) : null}
        </>
      )}
    </div>
  );
}

export function ViewSwitch({
  view,
  onChange,
  canSeeSettings,
}: {
  readonly view: View;
  readonly onChange: (v: View) => void;
  readonly canSeeSettings: boolean;
}): JSX.Element | null {
  if (!canSeeSettings) return null;
  const btn = (id: View, label: string) => (
    <button
      type="button"
      key={id}
      onClick={() => onChange(id)}
      aria-pressed={view === id}
      style={view === id ? primaryButtonStyle : buttonStyle}
      data-testid={`onboarding-view-${id}`}
    >
      {label}
    </button>
  );
  return (
    <div style={{ display: "flex", gap: 4 }} role="group" aria-label="Onboarding view">
      {btn("applications", "Applications")}
      {btn("settings", "Settings")}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tabs
// ---------------------------------------------------------------------------

export function OnboardingTabs({
  counts,
  active,
  onSelect,
}: {
  readonly counts: Readonly<Record<string, number>> | undefined;
  readonly active: OnboardingStatus;
  readonly onSelect: (s: OnboardingStatus) => void;
}): JSX.Element {
  return (
    <div role="tablist" aria-label="Application status" style={{ display: "flex", gap: 4, flexWrap: "wrap", borderBottom: "1px solid #e2e8f0" }}>
      {ONBOARDING_TABS.map((t) => {
        const selected = t.status === active;
        const n = tabCount(counts, t.status);
        return (
          <button
            key={t.status}
            type="button"
            role="tab"
            aria-selected={selected}
            onClick={() => onSelect(t.status)}
            data-testid={`onboarding-tab-${t.status}`}
            style={{
              ...buttonStyle,
              border: "none",
              borderBottom: selected ? "2px solid #0369a1" : "2px solid transparent",
              borderRadius: 0,
              background: "transparent",
              fontWeight: selected ? 600 : 400,
              fontSize: 13,
              padding: "8px 12px",
            }}
          >
            {t.label}{" "}
            <span data-testid={`onboarding-tab-count-${t.status}`} style={countStyle(n, t.status === "pending_approval")}>
              {n}
            </span>
          </button>
        );
      })}
    </div>
  );
}

function countStyle(n: number, highlight: boolean): CSSProperties {
  return {
    display: "inline-block",
    minWidth: 18,
    padding: "0 6px",
    borderRadius: 9,
    textAlign: "center",
    fontSize: 11,
    fontWeight: 600,
    background: highlight && n > 0 ? "#0369a1" : "#e2e8f0",
    color: highlight && n > 0 ? "#ffffff" : "#334155",
  };
}

// ---------------------------------------------------------------------------
// Queue table
// ---------------------------------------------------------------------------

/** Row text that never shows a blank cell. */
function orDash(v: string | null | undefined): string {
  return v === null || v === undefined || v.trim() === "" ? "—" : v;
}

export interface OnboardingQueueTableProps {
  readonly items: readonly OnboardingListItem[];
  /** Wraps the cell content in a link to the card; a router <Link> in the page. */
  readonly renderCardLink: (id: string, children: ReactNode) => ReactNode;
  /** Clock for the "last activity" column; fixed in tests. */
  readonly now?: number;
  readonly forceLayout?: "desktop" | "mobile";
}

export function OnboardingQueueTable({
  items,
  renderCardLink,
  now,
  forceLayout,
}: OnboardingQueueTableProps): JSX.Element {
  const columns: ResponsiveTableColumn<OnboardingListItem>[] = [
    {
      id: "organization",
      header: "Organization",
      primary: true,
      renderCell: (a) => (
        <div data-testid={`onboarding-row-${a.id}`}>
          <div style={{ fontWeight: 600 }}>{renderCardLink(a.id, orDash(a.org_name))}</div>
          {a.legal_name ? <div style={{ fontSize: 12, color: "#64748b" }}>{a.legal_name}</div> : null}
          {a.status === "approved" ? null : (
            <div style={{ marginTop: 2 }}>
              <StatusPill status={a.status} />
            </div>
          )}
        </div>
      ),
    },
    { id: "country", header: "Country", renderCell: (a) => orDash(a.country) },
    {
      id: "applicant",
      header: "Applicant",
      renderCell: (a) => (
        <div style={{ lineHeight: 1.4 }}>
          <div>{orDash(a.applicant_name)}</div>
          <div style={{ fontSize: 12 }}>{a.email}</div>
          <div style={{ fontSize: 12, color: "#64748b" }}>{orDash(a.phone)}</div>
        </div>
      ),
    },
    {
      id: "progress",
      header: "Step",
      renderCell: (a) => (
        <div style={{ minWidth: 110 }}>
          <div style={{ fontSize: 12 }}>
            {a.current_step} · {a.progress_pct}%
          </div>
          <div style={{ height: 6, borderRadius: 3, background: "#e2e8f0", marginTop: 4, overflow: "hidden" }}>
            <div
              style={{ width: `${Math.max(0, Math.min(100, a.progress_pct))}%`, height: "100%", background: "#0369a1" }}
            />
          </div>
        </div>
      ),
    },
    {
      id: "activity",
      header: "Last activity",
      renderCell: (a) => <span title={formatDateTime(a.last_activity_at)}>{relativeAge(a.last_activity_at, now)}</span>,
    },
    {
      id: "source",
      header: "Source",
      renderCell: (a) => <Pill tone={a.source === "telegram" ? "info" : "muted"}>{sourceLabel(a.source)}</Pill>,
    },
    {
      id: "verdict",
      header: "System verdict",
      renderCell: (a) =>
        a.would_approve ? (
          <Pill tone="good" testId={`onboarding-would-approve-${a.id}`}>
            System would approve
          </Pill>
        ) : (
          <Pill tone="muted" testId={`onboarding-would-approve-${a.id}`}>
            Needs a look
          </Pill>
        ),
    },
    {
      id: "open",
      header: "",
      hideOnMobile: true,
      renderCell: (a) => renderCardLink(a.id, <span style={linkButtonStyle}>Open</span>),
    },
  ];

  return (
    <ResponsiveTable<OnboardingListItem>
      id="onboarding-table"
      caption="Organizer applications"
      columns={columns}
      rows={items}
      rowKey={(a) => a.id}
      forceLayout={forceLayout}
      empty={<span data-testid="onboarding-empty">No applications in this tab.</span>}
    />
  );
}

export function Pager({
  total,
  offset,
  shown,
  limit,
  onPrev,
  onNext,
}: {
  readonly total: number;
  readonly offset: number;
  readonly shown: number;
  readonly limit: number;
  readonly onPrev: () => void;
  readonly onNext: () => void;
}): JSX.Element {
  const from = shown === 0 ? 0 : offset + 1;
  const to = offset + shown;
  const hasPrev = offset > 0;
  const hasNext = offset + limit < total;
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 12 }} data-testid="onboarding-pager">
      <button
        type="button"
        style={hasPrev ? buttonStyle : { ...buttonStyle, ...disabledButtonStyle }}
        disabled={!hasPrev}
        onClick={onPrev}
        data-testid="onboarding-prev"
      >
        Previous
      </button>
      <span style={{ fontSize: 12, color: "#475569" }} data-testid="onboarding-range">
        {from}–{to} of {total}
      </span>
      <button
        type="button"
        style={hasNext ? buttonStyle : { ...buttonStyle, ...disabledButtonStyle }}
        disabled={!hasNext}
        onClick={onNext}
        data-testid="onboarding-next"
      >
        Next
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

function OnboardingSettingsPanel(): JSX.Element {
  const queryClient = useQueryClient();
  const query = useQuery<OnboardingSettings, ApiError>({
    queryKey: ["admin", "onboarding", "settings"],
    queryFn: getOnboardingSettings,
    refetchOnWindowFocus: false,
    retry: false,
  });
  const [form, setForm] = useState<SettingsForm | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (query.data !== undefined) {
      setForm(settingsToForm(query.data));
    }
  }, [query.data]);

  const save = useMutation<OnboardingSettings, ApiError, OnboardingSettingsRequest>({
    mutationFn: (req) => updateOnboardingSettings(req),
    onSuccess: async (next) => {
      setSaved(true);
      setErrors({});
      setFormError(null);
      setForm(settingsToForm(next));
      await queryClient.invalidateQueries({ queryKey: ["admin", "onboarding", "settings"] });
    },
    onError: (err) => {
      setSaved(false);
      const fields = serverFieldErrors(err.details);
      if (Object.keys(fields).length > 0) setErrors(fields);
      setFormError(err.message);
    },
  });

  if (query.isLoading || form === null) {
    return query.error ? (
      <div style={errorStyle} data-testid="onboarding-settings-error">
        Could not load the settings: {query.error.message}
      </div>
    ) : (
      <p style={mutedStyle}>Loading…</p>
    );
  }

  return (
    <OnboardingSettingsView
      form={form}
      errors={errors}
      formError={formError}
      saved={saved}
      saving={save.isPending}
      updatedAt={query.data?.updated_at}
      onChange={(next) => {
        setSaved(false);
        setForm(next);
      }}
      onSave={() => {
        const check = validateSettings(form);
        setErrors(check.errors);
        setFormError(null);
        if (check.request !== undefined) save.mutate(check.request);
      }}
    />
  );
}

export interface OnboardingSettingsViewProps {
  readonly form: SettingsForm;
  readonly errors: Readonly<Record<string, string>>;
  readonly formError: string | null;
  readonly saved: boolean;
  readonly saving: boolean;
  readonly updatedAt?: string;
  readonly onChange: (next: SettingsForm) => void;
  readonly onSave: () => void;
}

/** The settings form. State-free so the Node-only test environment can render it. */
export function OnboardingSettingsView({
  form,
  errors,
  formError,
  saved,
  saving,
  updatedAt,
  onChange,
  onSave,
}: OnboardingSettingsViewProps): JSX.Element {
  const set = (patch: Partial<SettingsForm>) => onChange({ ...form, ...patch });
  const text = (key: keyof SettingsForm, label: string, hint?: string, mode?: "numeric") => (
    <div key={key}>
      <label style={labelStyle} htmlFor={`onboarding-set-${key}`}>
        {label}
      </label>
      <input
        id={`onboarding-set-${key}`}
        style={inputStyle}
        value={form[key]}
        inputMode={mode}
        onChange={(e) => set({ [key]: e.target.value } as Partial<SettingsForm>)}
        aria-invalid={errors[key] !== undefined}
        data-testid={`onboarding-set-${key}`}
      />
      {hint ? <p style={{ ...hintStyle, margin: "4px 0 0 0" }}>{hint}</p> : null}
      {errors[key] ? (
        <p style={fieldErrorStyle} data-testid={`onboarding-set-${key}-error`}>
          {errors[key]}
        </p>
      ) : null}
    </div>
  );

  return (
    <form
      style={{ ...sectionStyle, maxWidth: 720 }}
      data-testid="onboarding-settings"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      <h2 style={{ margin: "0 0 12px 0", fontSize: 16, fontWeight: 600 }}>Onboarding settings</h2>
      <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
        <div>
          <label style={labelStyle} htmlFor="onboarding-set-approval_mode">
            Approval mode
          </label>
          <select
            id="onboarding-set-approval_mode"
            style={inputStyle}
            value={form.approval_mode}
            onChange={(e) => set({ approval_mode: e.target.value })}
            data-testid="onboarding-set-approval_mode"
          >
            {APPROVAL_MODES.map((m) => (
              <option key={m} value={m}>
                {APPROVAL_MODE_LABELS[m]}
              </option>
            ))}
          </select>
          {errors.approval_mode ? <p style={fieldErrorStyle}>{errors.approval_mode}</p> : null}
          {form.approval_mode === "auto_when_complete" ? (
            <div style={{ ...warningStyle, marginTop: 8 }} role="alert" data-testid="onboarding-auto-warning">
              <strong>Automatic approval.</strong> {AUTO_MODE_WARNING}
            </div>
          ) : null}
        </div>
        {text("draft_ttl_days", "Draft lifetime, days", "How long an unfinished draft lives after the applicant's last activity (7–1095).", "numeric")}
        {text("purge_after_days", "Erase personal data after, days", "Expired and rejected applications lose their personal fields after this long (30–3650).", "numeric")}
        {text("countries", "Accepted countries", "ISO codes (two letters) separated by commas or spaces, for example CZ, DE, ES. Empty means every country.")}
        {text("max_new_per_day", "New applications per day, at most", "A daily ceiling on new drafts (1–100000).", "numeric")}
        {text("terms_version", "Terms of service version", "Written into every application at the moment of consent.")}
        {text("privacy_version", "Privacy policy version")}
      </div>

      {formError ? (
        <div style={{ ...errorStyle, marginTop: 14, marginBottom: 0 }} data-testid="onboarding-settings-form-error">
          {formError}
        </div>
      ) : null}
      {saved ? (
        <div style={{ ...noticeStyle, marginTop: 14, marginBottom: 0 }} data-testid="onboarding-settings-saved">
          Settings saved.
        </div>
      ) : null}

      <div style={{ display: "flex", alignItems: "center", gap: 12, marginTop: 16 }}>
        <button
          type="submit"
          style={saving ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
          disabled={saving}
          data-testid="onboarding-settings-save"
        >
          {saving ? "Saving…" : "Save settings"}
        </button>
        {updatedAt ? <span style={mutedStyle}>Last changed {formatDateTime(updatedAt)}</span> : null}
      </div>
      <p style={{ ...hintStyle, marginTop: 10, marginBottom: 0 }}>
        Saving asks for an audit reason; every change is recorded in the audit log.
      </p>
    </form>
  );
}
