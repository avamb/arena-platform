/**
 * Events admin module (feature #281 / E-3).
 *
 * Replaces the SAUI-12 /events placeholder shell with a real list +
 * detail screen backed by the events API in
 * apps/backend/internal/platform/httpserver/events.go:
 *
 *   GET    /v1/events?visibility=...                       cross-org list (event.read)
 *   GET    /v1/events/{id}                                 single event   (event.read)
 *   POST   /v1/organizations/{org_id}/events/{id}/status   status txn     (event.publish)
 *   GET    /v1/organizations/{org_id}/events/{event_id}/sessions
 *                                                          drawer sessions
 *   GET    /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers
 *                                                          drawer tiers
 *   GET    /v1/events/{event_id}/publications              drawer pubs (publication.read)
 *   GET    /v1/organizations                               org filter dropdown
 *
 * The route is intentionally read-only-plus-status-transitions: full
 * CRUD (create / edit / delete) is delegated to a later wave. This
 * scope ships the operator surface the spec called out -- list with
 * filters, detail drawer with five tabs, and lifecycle transitions.
 *
 * Status transitions (event lifecycle):
 *
 *   draft     → published, cancelled
 *   published → cancelled, archived
 *   cancelled → archived
 *
 * 422 `event.invalid_transition` from the backend is surfaced inline
 * with the action button so the operator immediately sees why a move
 * was rejected.
 *
 * Channels column:
 *   The events table has no first-class "channels" field. We render a
 *   small badge based on the lazily-fetched publications inside the
 *   detail drawer's Publications tab; the LIST view shows a dash for
 *   the column with a hint to open the drawer (a per-row publications
 *   fan-out would multiply N+1 queries against the API). When a future
 *   list-side publications summary is added to the EventItem shape we
 *   wire it in here.
 *
 * "Next session" column:
 *   The EventItem shape does not currently expose an aggregated
 *   next-session timestamp. We approximate by rendering the event's
 *   own `start_at` (events represent the umbrella; their start_at is
 *   the earliest scheduled time). When an `events.next_session_at`
 *   field is added server-side, replace the column source here.
 *
 * Activity tab:
 *   There is no per-event audit endpoint yet. The tab renders an
 *   honest empty-state instead of a fake feed.
 *
 * Mock data: NONE. Everything in this module hits the live backend.
 * No globalThis / devStore / mockDb.
 */
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useEffect,
  useMemo,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { useAuth } from "@/lib/auth/useAuth";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/events",
  component: EventsRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

export const EVENT_STATUSES = [
  "draft",
  "published",
  "cancelled",
  "archived",
] as const;
export type EventStatus = (typeof EVENT_STATUSES)[number];

export const EVENT_VISIBILITIES = ["public", "private", "unlisted"] as const;
export type EventVisibility = (typeof EVENT_VISIBILITIES)[number];

export type EventVisibilityFilter = EventVisibility | "all";

export interface EventItem {
  readonly id: string;
  readonly org_id: string;
  readonly venue_id: string | null;
  readonly name: string;
  readonly description: string | null;
  readonly status: EventStatus;
  readonly start_at: string;
  readonly end_at: string;
  readonly visibility: EventVisibility;
  readonly image_url: string | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface EventListEnvelope {
  readonly events: readonly EventItem[];
}

interface EventEnvelope {
  readonly event: EventItem;
}

interface OrganizationSummary {
  readonly id: string;
  readonly name: string;
  readonly slug?: string;
}

interface OrganizationListEnvelope {
  readonly organizations: readonly OrganizationSummary[];
}

export interface SessionItem {
  readonly id: string;
  readonly event_id: string;
  readonly start_at: string;
  readonly end_at: string;
  readonly capacity_total: number;
  readonly status: "draft" | "scheduled" | "cancelled" | "completed" | string;
  readonly created_at: string;
  readonly updated_at: string;
  readonly has_overlapping_sessions?: boolean;
}

interface SessionListEnvelope {
  readonly sessions: readonly SessionItem[];
  readonly has_overlapping_sessions?: boolean;
}

export interface TicketTierItem {
  readonly id: string;
  readonly session_id: string;
  readonly name: string;
  readonly pricing_mode: "free" | "fixed" | "pwyw" | string;
  readonly price_amount: number;
  readonly currency: string;
  readonly pwyw_min?: number | null;
  readonly pwyw_max?: number | null;
  readonly capacity?: number | null;
  readonly sale_window_start?: string | null;
  readonly sale_window_end?: string | null;
  readonly sort_order: number;
}

interface TicketTierListEnvelope {
  readonly ticket_tiers?: readonly TicketTierItem[];
  readonly tiers?: readonly TicketTierItem[];
}

export interface EventPublication {
  readonly id: string;
  readonly event_id: string;
  readonly feed_token_id: string;
  readonly city_id: string | null;
  readonly published_at: string;
}

interface EventPublicationListEnvelope {
  readonly publications: readonly EventPublication[];
}

// ---------------------------------------------------------------------------
// Pure helpers (exported for unit tests)
// ---------------------------------------------------------------------------

export function isEventStatus(value: string): value is EventStatus {
  return (EVENT_STATUSES as readonly string[]).includes(value);
}

export function isEventVisibility(value: string): value is EventVisibility {
  return (EVENT_VISIBILITIES as readonly string[]).includes(value);
}

/**
 * Allowed status transitions, mirroring the backend state machine
 * documented in the OpenAPI UpdateEventStatusRequest schema. Re-applying
 * the same status is a server-side no-op and intentionally not offered
 * in the UI.
 */
export function allowedTransitions(status: EventStatus): readonly EventStatus[] {
  switch (status) {
    case "draft":
      return ["published", "cancelled"];
    case "published":
      return ["cancelled", "archived"];
    case "cancelled":
      return ["archived"];
    case "archived":
      return [];
  }
}

/**
 * Filter events whose `start_at` falls inside an inclusive date range.
 * Both bounds are optional ("" = unbounded). Inputs are
 * `<input type="date">` strings (yyyy-MM-dd, local TZ-naive); we compare
 * by ISO date prefix so an off-by-one timezone shift in the client does
 * not silently drop events near midnight UTC.
 */
export function filterEventsByDateRange<T extends { start_at: string }>(
  events: readonly T[],
  startAfter: string,
  endBefore: string,
): readonly T[] {
  const after = startAfter.trim();
  const before = endBefore.trim();
  if (after === "" && before === "") {
    return events;
  }
  return events.filter((e) => {
    const day = e.start_at.slice(0, 10);
    if (after !== "" && day < after) {
      return false;
    }
    if (before !== "" && day > before) {
      return false;
    }
    return true;
  });
}

export function filterEventsByOrg<T extends { org_id: string }>(
  events: readonly T[],
  orgID: string,
): readonly T[] {
  if (orgID.trim() === "") {
    return events;
  }
  return events.filter((e) => e.org_id === orgID);
}

export function filterEventsByStatus<T extends { status: string }>(
  events: readonly T[],
  status: EventStatus | "",
): readonly T[] {
  if (status === "") {
    return events;
  }
  return events.filter((e) => e.status === status);
}

export function paginate<T>(items: readonly T[], page: number, pageSize: number): {
  rows: readonly T[];
  page: number;
  totalPages: number;
} {
  const total = items.length;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const clamped = Math.min(Math.max(1, page), totalPages);
  const start = (clamped - 1) * pageSize;
  return {
    rows: items.slice(start, start + pageSize),
    page: clamped,
    totalPages,
  };
}

export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ` +
    `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC`
  );
}

export function formatDateOnly(iso: string): string {
  return iso.slice(0, 10);
}

export function posterInitial(name: string): string {
  const trimmed = name.trim();
  return trimmed.length > 0 ? trimmed[0]!.toUpperCase() : "?";
}

export const PAGE_SIZE = 25;

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const EVENTS_NAV_ENTRY = NAV_BY_PATH["/events"];
if (EVENTS_NAV_ENTRY === undefined) {
  throw new Error("events route: NAV_BY_PATH['/events'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function EventsRoute() {
  return (
    <RequirePermission entry={EVENTS_NAV_ENTRY}>
      <EventsModule />
    </RequirePermission>
  );
}

function EventsModule() {
  const { permissions } = useAuth();
  const canPublish = permissions.has("event.publish");
  const canReadPublications = permissions.has("publication.read");

  const [visibilityFilter, setVisibilityFilter] =
    useState<EventVisibilityFilter>("all");
  const [orgFilter, setOrgFilter] = useState<string>("");
  const [statusFilter, setStatusFilter] = useState<EventStatus | "">("");
  const [startAfter, setStartAfter] = useState<string>("");
  const [endBefore, setEndBefore] = useState<string>("");
  const [page, setPage] = useState<number>(1);
  const [selectedID, setSelectedID] = useState<string | null>(null);

  const listQuery = useQuery<EventListEnvelope, ApiError>({
    queryKey: ["events", "list", visibilityFilter],
    queryFn: () =>
      authedFetch<EventListEnvelope>({
        method: "GET",
        path: `/v1/events?visibility=${encodeURIComponent(visibilityFilter)}`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 0) {
          return false;
        }
        if (err.code === "permissions.denied") {
          return false;
        }
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const orgsQuery = useQuery<OrganizationListEnvelope, ApiError>({
    queryKey: ["events", "orgs"],
    queryFn: () =>
      authedFetch<OrganizationListEnvelope>({
        method: "GET",
        path: "/v1/organizations",
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const allEvents = listQuery.data?.events ?? [];

  const filtered = useMemo(() => {
    const byOrg = filterEventsByOrg(allEvents, orgFilter);
    const byStatus = filterEventsByStatus(byOrg, statusFilter);
    const byDate = filterEventsByDateRange(byStatus, startAfter, endBefore);
    return [...byDate].sort((a, b) => a.start_at.localeCompare(b.start_at));
  }, [allEvents, orgFilter, statusFilter, startAfter, endBefore]);

  const paged = useMemo(
    () => paginate(filtered, page, PAGE_SIZE),
    [filtered, page],
  );

  useEffect(() => {
    // Reset to page 1 whenever filters narrow the list to fewer pages.
    if (page !== paged.page) {
      setPage(paged.page);
    }
  }, [paged.page, page]);

  const orgsByID = useMemo(() => {
    const map = new Map<string, OrganizationSummary>();
    for (const o of orgsQuery.data?.organizations ?? []) {
      map.set(o.id, o);
    }
    return map;
  }, [orgsQuery.data]);

  const selectedEvent = useMemo(
    () => allEvents.find((e) => e.id === selectedID) ?? null,
    [allEvents, selectedID],
  );

  return (
    <section aria-labelledby="events-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="events-heading" style={headingStyle}>
            Events
          </h1>
          <p style={subheadingStyle}>
            Cross-organization events directory. List is shared across
            organizations; status transitions (draft, published, cancelled,
            archived) are owner-gated and require the{" "}
            <code style={monoStyle}>event.publish</code> permission. Full
            create / edit / delete will land in a later wave.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => listQuery.refetch()}
            style={refreshButtonStyle}
            disabled={listQuery.isFetching}
            data-testid="events-refresh"
          >
            {listQuery.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      <FilterBar
        visibility={visibilityFilter}
        onVisibility={(v) => {
          setVisibilityFilter(v);
          setPage(1);
        }}
        org={orgFilter}
        onOrg={(v) => {
          setOrgFilter(v);
          setPage(1);
        }}
        orgs={orgsQuery.data?.organizations ?? []}
        orgsLoading={orgsQuery.isPending}
        status={statusFilter}
        onStatus={(v) => {
          setStatusFilter(v);
          setPage(1);
        }}
        startAfter={startAfter}
        onStartAfter={(v) => {
          setStartAfter(v);
          setPage(1);
        }}
        endBefore={endBefore}
        onEndBefore={(v) => {
          setEndBefore(v);
          setPage(1);
        }}
      />

      <EventsBody
        query={listQuery}
        rows={paged.rows}
        totalFiltered={filtered.length}
        page={paged.page}
        totalPages={paged.totalPages}
        onPageChange={setPage}
        orgsByID={orgsByID}
        onSelect={(id) => setSelectedID(id)}
      />

      {selectedEvent !== null ? (
        <EventDrawer
          event={selectedEvent}
          canPublish={canPublish}
          canReadPublications={canReadPublications}
          onClose={() => setSelectedID(null)}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Filter bar
// ---------------------------------------------------------------------------

interface FilterBarProps {
  visibility: EventVisibilityFilter;
  onVisibility: (v: EventVisibilityFilter) => void;
  org: string;
  onOrg: (v: string) => void;
  orgs: readonly OrganizationSummary[];
  orgsLoading: boolean;
  status: EventStatus | "";
  onStatus: (v: EventStatus | "") => void;
  startAfter: string;
  onStartAfter: (v: string) => void;
  endBefore: string;
  onEndBefore: (v: string) => void;
}

function FilterBar(props: FilterBarProps) {
  return (
    <div style={filterBarStyle} role="search" aria-label="Events filters">
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Organization</span>
        <select
          value={props.org}
          onChange={(e) => props.onOrg(e.target.value)}
          style={filterSelectStyle}
          data-testid="events-filter-org"
          disabled={props.orgsLoading}
        >
          <option value="">All organizations</option>
          {[...props.orgs]
            .sort((a, b) => a.name.localeCompare(b.name))
            .map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
        </select>
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Status</span>
        <select
          value={props.status}
          onChange={(e) => {
            const v = e.target.value;
            props.onStatus(v === "" ? "" : (v as EventStatus));
          }}
          style={filterSelectStyle}
          data-testid="events-filter-status"
        >
          <option value="">All statuses</option>
          {EVENT_STATUSES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Visibility</span>
        <select
          value={props.visibility}
          onChange={(e) =>
            props.onVisibility(e.target.value as EventVisibilityFilter)
          }
          style={filterSelectStyle}
          data-testid="events-filter-visibility"
        >
          <option value="all">All</option>
          {EVENT_VISIBILITIES.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Starts on or after</span>
        <input
          type="date"
          value={props.startAfter}
          onChange={(e) => props.onStartAfter(e.target.value)}
          style={filterInputStyle}
          data-testid="events-filter-start"
        />
      </label>
      <label style={filterFieldStyle}>
        <span style={filterLabelStyle}>Starts on or before</span>
        <input
          type="date"
          value={props.endBefore}
          onChange={(e) => props.onEndBefore(e.target.value)}
          style={filterInputStyle}
          data-testid="events-filter-end"
        />
      </label>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Body: list table + pagination
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<EventListEnvelope, ApiError>>;
  rows: readonly EventItem[];
  totalFiltered: number;
  page: number;
  totalPages: number;
  onPageChange: (n: number) => void;
  orgsByID: ReadonlyMap<string, OrganizationSummary>;
  onSelect: (id: string) => void;
}

function EventsBody({
  query,
  rows,
  totalFiltered,
  page,
  totalPages,
  onPageChange,
  orgsByID,
  onSelect,
}: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading events from /v1/events…
      </div>
    );
  }
  if (query.isError) {
    return <EventsErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="events-empty">
        {totalFiltered === 0
          ? "No events match the current filters."
          : "No events on this page."}
      </div>
    );
  }
  return (
    <>
      <div style={tableWrapStyle} role="region" aria-label="Events">
        <table style={tableStyle} data-testid="events-table">
          <thead>
            <tr>
              <th scope="col" style={thStyle} aria-label="Poster" />
              <th scope="col" style={thStyle}>Name</th>
              <th scope="col" style={thStyle}>Venue</th>
              <th scope="col" style={thStyle}>Next session</th>
              <th scope="col" style={thStyle}>Status</th>
              <th scope="col" style={thStyle}>Channels</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((ev) => (
              <tr
                key={ev.id}
                data-testid={`events-row-${ev.id}`}
                onClick={() => onSelect(ev.id)}
                style={tableRowStyle}
              >
                <td style={tdStyle}>
                  <PosterThumb event={ev} />
                </td>
                <td style={tdStyle}>
                  <button
                    type="button"
                    style={linkButtonStyle}
                    onClick={(e) => {
                      e.stopPropagation();
                      onSelect(ev.id);
                    }}
                    data-testid={`events-open-${ev.id}`}
                  >
                    {ev.name}
                  </button>
                  <div style={mutedHintStyle}>
                    {orgsByID.get(ev.org_id)?.name ?? shortenUUID(ev.org_id)}
                  </div>
                </td>
                <td style={tdMonoStyle} title={ev.venue_id ?? ""}>
                  {ev.venue_id !== null ? shortenUUID(ev.venue_id) : "—"}
                </td>
                <td style={tdStyle}>{formatDateTime(ev.start_at)}</td>
                <td style={tdStyle}>
                  <EventStatusBadge status={ev.status} />
                </td>
                <td style={tdStyle}>
                  <span style={mutedHintStyle} title="Open the drawer's Publications tab to view channels.">
                    —
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <Pagination
        page={page}
        totalPages={totalPages}
        totalFiltered={totalFiltered}
        onChange={onPageChange}
      />
    </>
  );
}

function PosterThumb({ event }: { event: EventItem }) {
  if (event.image_url !== null && event.image_url !== "") {
    return (
      <img
        src={event.image_url}
        alt=""
        width={40}
        height={40}
        style={posterImgStyle}
      />
    );
  }
  return (
    <div style={posterFallbackStyle} aria-hidden="true">
      {posterInitial(event.name)}
    </div>
  );
}

function Pagination({
  page,
  totalPages,
  totalFiltered,
  onChange,
}: {
  page: number;
  totalPages: number;
  totalFiltered: number;
  onChange: (n: number) => void;
}) {
  if (totalFiltered <= PAGE_SIZE) {
    return null;
  }
  return (
    <div style={paginationStyle} data-testid="events-pagination">
      <button
        type="button"
        style={refreshButtonStyle}
        onClick={() => onChange(page - 1)}
        disabled={page <= 1}
        data-testid="events-prev"
      >
        Previous
      </button>
      <span style={mutedHintStyle}>
        Page {page} of {totalPages} · {totalFiltered} events
      </span>
      <button
        type="button"
        style={refreshButtonStyle}
        onClick={() => onChange(page + 1)}
        disabled={page >= totalPages}
        data-testid="events-next"
      >
        Next
      </button>
    </div>
  );
}

function EventsErrorState({
  error,
  onRetry,
}: {
  error: ApiError | null;
  onRetry: () => void;
}) {
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="events-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>event.read</code>.
          Ask a platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="events-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload events.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="events-error">
      <strong>Failed to load events.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

function EventStatusBadge({ status }: { status: EventStatus }) {
  const palette: Record<EventStatus, CSSProperties> = {
    draft: { background: "#fef3c7", color: "#854d0e", borderColor: "#fde68a" },
    published: { background: "#dcfce7", color: "#166534", borderColor: "#86efac" },
    cancelled: { background: "#fee2e2", color: "#991b1b", borderColor: "#fca5a5" },
    archived: { background: "#f1f5f9", color: "#475569", borderColor: "#cbd5e1" },
  };
  return (
    <span
      style={{ ...statusBadgeStyle, ...palette[status] }}
      data-testid={`events-status-${status}`}
    >
      {status}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Drawer: 5 tabs (Overview / Sessions / Tiers / Publications / Activity)
// ---------------------------------------------------------------------------

type DrawerTab = "overview" | "sessions" | "tiers" | "publications" | "activity";

const DRAWER_TABS: ReadonlyArray<{ id: DrawerTab; label: string }> = [
  { id: "overview", label: "Overview" },
  { id: "sessions", label: "Sessions" },
  { id: "tiers", label: "Ticket tiers" },
  { id: "publications", label: "Publications" },
  { id: "activity", label: "Activity" },
];

interface DrawerProps {
  event: EventItem;
  canPublish: boolean;
  canReadPublications: boolean;
  onClose: () => void;
}

function EventDrawer({
  event,
  canPublish,
  canReadPublications,
  onClose,
}: DrawerProps) {
  const [tab, setTab] = useState<DrawerTab>("overview");
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="events-drawer-title"
      style={drawerBackdropStyle}
      data-testid="events-drawer"
      onClick={onClose}
    >
      <aside style={drawerStyle} onClick={(e) => e.stopPropagation()}>
        <header style={drawerHeaderStyle}>
          <div>
            <h2 id="events-drawer-title" style={drawerTitleStyle}>
              {event.name}
            </h2>
            <div style={mutedHintStyle}>
              <code style={monoStyle}>{event.id}</code>
            </div>
          </div>
          <button
            type="button"
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="events-drawer-close"
          >
            ×
          </button>
        </header>
        <nav style={drawerTabBarStyle} aria-label="Event detail tabs">
          {DRAWER_TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              style={tab === t.id ? activeTabStyle : tabStyle}
              onClick={() => setTab(t.id)}
              data-testid={`events-tab-${t.id}`}
              aria-current={tab === t.id ? "page" : undefined}
            >
              {t.label}
            </button>
          ))}
        </nav>
        <div style={drawerContentStyle}>
          {tab === "overview" ? (
            <OverviewTab event={event} canPublish={canPublish} />
          ) : null}
          {tab === "sessions" ? <SessionsTab event={event} /> : null}
          {tab === "tiers" ? <TiersTab event={event} /> : null}
          {tab === "publications" ? (
            <PublicationsTab event={event} canRead={canReadPublications} />
          ) : null}
          {tab === "activity" ? <ActivityTab /> : null}
        </div>
      </aside>
    </div>
  );
}

function OverviewTab({
  event,
  canPublish,
}: {
  event: EventItem;
  canPublish: boolean;
}) {
  const queryClient = useQueryClient();
  const [errMsg, setErrMsg] = useState<string | null>(null);
  const [okMsg, setOkMsg] = useState<string | null>(null);
  const transitions = allowedTransitions(event.status);

  const mutation = useMutation<EventEnvelope, ApiError, EventStatus>({
    mutationFn: (target) =>
      authedFetch<EventEnvelope>({
        method: "POST",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/status`,
        body: { status: target },
      }),
    onSuccess: (data, target) => {
      setErrMsg(null);
      setOkMsg(`Status changed to ${target}.`);
      queryClient.invalidateQueries({ queryKey: ["events"] });
      // Re-fetch the single event too for any downstream readers.
      void queryClient.invalidateQueries({
        queryKey: ["events", "detail", data.event.id],
      });
    },
    onError: (err) => {
      setOkMsg(null);
      if (err.code === "event.invalid_transition") {
        setErrMsg(
          err.message ||
            "That status transition is not permitted from the current state.",
        );
      } else if (err.code === "permissions.denied" || err.status === 403) {
        setErrMsg(
          "Your account is missing event.publish. Ask a platform administrator.",
        );
      } else {
        setErrMsg(`${err.message} (${err.code})`);
      }
    },
  });

  return (
    <div style={tabBodyStyle}>
      <DetailRow label="Status">
        <EventStatusBadge status={event.status} />
      </DetailRow>
      <DetailRow label="Visibility">{event.visibility}</DetailRow>
      <DetailRow label="Organization">
        <code style={monoStyle}>{event.org_id}</code>
      </DetailRow>
      <DetailRow label="Venue">
        {event.venue_id !== null ? (
          <code style={monoStyle}>{event.venue_id}</code>
        ) : (
          <span style={mutedHintStyle}>no fixed venue</span>
        )}
      </DetailRow>
      <DetailRow label="Starts">{formatDateTime(event.start_at)}</DetailRow>
      <DetailRow label="Ends">{formatDateTime(event.end_at)}</DetailRow>
      <DetailRow label="Created">{formatDateOnly(event.created_at)}</DetailRow>
      <DetailRow label="Updated">{formatDateOnly(event.updated_at)}</DetailRow>
      {event.description !== null && event.description !== "" ? (
        <div style={descriptionBlockStyle}>
          <div style={detailLabelStyle}>Description</div>
          <p style={descriptionTextStyle}>{event.description}</p>
        </div>
      ) : null}

      <div style={transitionSectionStyle}>
        <div style={detailLabelStyle}>Status transitions</div>
        {transitions.length === 0 ? (
          <p style={mutedHintStyle}>
            No further transitions are allowed from <code style={monoStyle}>{event.status}</code>.
          </p>
        ) : !canPublish ? (
          <p style={mutedHintStyle}>
            Status transitions require the{" "}
            <code style={monoStyle}>event.publish</code> permission.
          </p>
        ) : (
          <div style={transitionButtonRowStyle}>
            {transitions.map((target) => (
              <button
                key={target}
                type="button"
                style={target === "cancelled" ? dangerButtonStyle : primaryButtonStyle}
                onClick={() => mutation.mutate(target)}
                disabled={mutation.isPending}
                data-testid={`events-transition-${target}`}
              >
                {mutation.isPending && mutation.variables === target
                  ? "Submitting…"
                  : `Set to ${target}`}
              </button>
            ))}
          </div>
        )}
        {errMsg !== null ? (
          <div style={formErrorStyle} role="alert" data-testid="events-transition-error">
            {errMsg}
          </div>
        ) : null}
        {okMsg !== null ? (
          <div style={successBoxStyle} role="status" data-testid="events-transition-ok">
            {okMsg}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function SessionsTab({ event }: { event: EventItem }) {
  const query = useQuery<SessionListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "sessions"],
    queryFn: () =>
      authedFetch<SessionListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  if (query.isPending) {
    return <div style={statusBoxStyle}>Loading sessions…</div>;
  }
  if (query.isError) {
    return (
      <div style={errorBoxStyle} role="alert">
        <strong>Failed to load sessions.</strong>
        <div style={errorCodeStyle}>{query.error?.code ?? "unknown.error"}</div>
      </div>
    );
  }
  const sessions = query.data?.sessions ?? [];
  if (sessions.length === 0) {
    return (
      <div style={statusBoxStyle} data-testid="events-sessions-empty">
        No sessions have been scheduled for this event.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle}>
      <table style={tableStyle} data-testid="events-sessions-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Starts</th>
            <th scope="col" style={thStyle}>Ends</th>
            <th scope="col" style={thStyle}>Capacity</th>
            <th scope="col" style={thStyle}>Status</th>
          </tr>
        </thead>
        <tbody>
          {sessions.map((s) => (
            <tr key={s.id} data-testid={`events-session-${s.id}`}>
              <td style={tdStyle}>{formatDateTime(s.start_at)}</td>
              <td style={tdStyle}>{formatDateTime(s.end_at)}</td>
              <td style={tdStyle}>{s.capacity_total.toLocaleString()}</td>
              <td style={tdStyle}>{s.status}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TiersTab({ event }: { event: EventItem }) {
  const sessionsQuery = useQuery<SessionListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "sessions"],
    queryFn: () =>
      authedFetch<SessionListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  if (sessionsQuery.isPending) {
    return <div style={statusBoxStyle}>Loading sessions…</div>;
  }
  if (sessionsQuery.isError) {
    return (
      <div style={errorBoxStyle} role="alert">
        <strong>Failed to load sessions.</strong>
        <div style={errorCodeStyle}>
          {sessionsQuery.error?.code ?? "unknown.error"}
        </div>
      </div>
    );
  }
  const sessions = sessionsQuery.data?.sessions ?? [];
  if (sessions.length === 0) {
    return (
      <div style={statusBoxStyle} data-testid="events-tiers-empty-sessions">
        No sessions yet -- create a session before adding ticket tiers.
      </div>
    );
  }
  return (
    <div style={tabBodyStyle}>
      {sessions.map((s) => (
        <SessionTiersBlock key={s.id} event={event} session={s} />
      ))}
    </div>
  );
}

function SessionTiersBlock({
  event,
  session,
}: {
  event: EventItem;
  session: SessionItem;
}) {
  const query = useQuery<TicketTierListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "session", session.id, "tiers"],
    queryFn: () =>
      authedFetch<TicketTierListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${event.org_id}/events/${event.id}/sessions/${session.id}/tiers`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const tiers = query.data?.ticket_tiers ?? query.data?.tiers ?? [];
  return (
    <section style={tierBlockStyle} data-testid={`events-tier-block-${session.id}`}>
      <header style={tierBlockHeaderStyle}>
        <div>
          <div style={detailLabelStyle}>
            Session {formatDateTime(session.start_at)}
          </div>
          <div style={mutedHintStyle}>
            {session.status} · capacity {session.capacity_total.toLocaleString()}
          </div>
        </div>
      </header>
      {query.isPending ? (
        <div style={statusBoxStyle}>Loading tiers…</div>
      ) : query.isError ? (
        <div style={errorBoxStyle} role="alert">
          <strong>Failed to load tiers.</strong>
          <div style={errorCodeStyle}>{query.error?.code ?? "unknown.error"}</div>
        </div>
      ) : tiers.length === 0 ? (
        <div style={statusBoxStyle}>No tiers configured.</div>
      ) : (
        <div style={tableWrapStyle}>
          <table style={tableStyle}>
            <thead>
              <tr>
                <th scope="col" style={thStyle}>Name</th>
                <th scope="col" style={thStyle}>Pricing</th>
                <th scope="col" style={thStyle}>Price</th>
                <th scope="col" style={thStyle}>Currency</th>
                <th scope="col" style={thStyle}>Capacity</th>
              </tr>
            </thead>
            <tbody>
              {tiers.map((t) => (
                <tr key={t.id} data-testid={`events-tier-${t.id}`}>
                  <td style={tdStyle}>{t.name}</td>
                  <td style={tdStyle}>{t.pricing_mode}</td>
                  <td style={tdStyle}>
                    {(t.price_amount / 100).toLocaleString(undefined, {
                      minimumFractionDigits: 2,
                      maximumFractionDigits: 2,
                    })}
                  </td>
                  <td style={tdStyle}>{t.currency}</td>
                  <td style={tdStyle}>
                    {t.capacity !== null && t.capacity !== undefined
                      ? t.capacity.toLocaleString()
                      : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function PublicationsTab({
  event,
  canRead,
}: {
  event: EventItem;
  canRead: boolean;
}) {
  const query = useQuery<EventPublicationListEnvelope, ApiError>({
    queryKey: ["events", "detail", event.id, "publications"],
    queryFn: () =>
      authedFetch<EventPublicationListEnvelope>({
        method: "GET",
        path: `/v1/events/${event.id}/publications`,
      }),
    enabled: canRead,
    retry: false,
    refetchOnWindowFocus: false,
  });
  if (!canRead) {
    return (
      <div style={statusBoxStyle} data-testid="events-publications-forbidden">
        Viewing publications requires the{" "}
        <code style={monoStyle}>publication.read</code> permission.
      </div>
    );
  }
  if (query.isPending) {
    return <div style={statusBoxStyle}>Loading publications…</div>;
  }
  if (query.isError) {
    return (
      <div style={errorBoxStyle} role="alert">
        <strong>Failed to load publications.</strong>
        <div style={errorCodeStyle}>{query.error?.code ?? "unknown.error"}</div>
      </div>
    );
  }
  const pubs = query.data?.publications ?? [];
  if (pubs.length === 0) {
    return (
      <div style={statusBoxStyle} data-testid="events-publications-empty">
        This event has not been published to any feed yet.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle}>
      <table style={tableStyle} data-testid="events-publications-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Feed token</th>
            <th scope="col" style={thStyle}>Scope</th>
            <th scope="col" style={thStyle}>Published</th>
          </tr>
        </thead>
        <tbody>
          {pubs.map((p) => (
            <tr key={p.id} data-testid={`events-publication-${p.id}`}>
              <td style={tdMonoStyle} title={p.feed_token_id}>
                {shortenUUID(p.feed_token_id)}
              </td>
              <td style={tdStyle}>
                {p.city_id === null ? (
                  <span style={globalScopeBadgeStyle}>global</span>
                ) : (
                  <span style={scopedBadgeStyle} title={p.city_id}>
                    city {shortenUUID(p.city_id)}
                  </span>
                )}
              </td>
              <td style={tdStyle}>{formatDateTime(p.published_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ActivityTab() {
  return (
    <div style={statusBoxStyle} data-testid="events-activity-empty">
      No activity feed available for this event yet. A per-event audit reader
      will be wired in when the backend exposes one.
    </div>
  );
}

function DetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div style={detailRowStyle}>
      <div style={detailLabelStyle}>{label}</div>
      <div style={detailValueStyle}>{children}</div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

function shortenUUID(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

// ---------------------------------------------------------------------------
// Styles (mirror venues.tsx)
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
};

const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: -0.2,
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const refreshWrapStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
  flexWrap: "wrap",
};

const refreshButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const dangerButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#b91c1c",
  border: "1px solid #b91c1c",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const mutedHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#94a3b8",
  fontStyle: "italic",
};

const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
};

const tableRowStyle: CSSProperties = {
  cursor: "pointer",
};

const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "10px 12px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const tdStyle: CSSProperties = {
  padding: "10px 12px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
  verticalAlign: "middle",
};

const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  color: "#334155",
};

const linkButtonStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  padding: 0,
  color: "#0369a1",
  cursor: "pointer",
  fontWeight: 600,
  fontSize: 13,
  textAlign: "left",
};

const statusBoxStyle: CSSProperties = {
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};

const errorBoxStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 16,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  fontSize: 12,
};
const errorParaStyle: CSSProperties = { margin: 0, fontSize: 12 };
const errorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};
const errorRetryStyle: CSSProperties = {
  alignSelf: "flex-start",
  fontSize: 12,
  padding: "6px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const filterBarStyle: CSSProperties = {
  display: "flex",
  gap: 12,
  flexWrap: "wrap",
  alignItems: "flex-end",
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const filterFieldStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  minWidth: 140,
};

const filterLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const filterSelectStyle: CSSProperties = {
  fontSize: 13,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const filterInputStyle: CSSProperties = {
  ...filterSelectStyle,
};

const paginationStyle: CSSProperties = {
  display: "flex",
  gap: 12,
  alignItems: "center",
  justifyContent: "flex-end",
  padding: "8px 0",
};

const posterImgStyle: CSSProperties = {
  width: 40,
  height: 40,
  borderRadius: 4,
  objectFit: "cover",
  border: "1px solid #e2e8f0",
  display: "block",
};

const posterFallbackStyle: CSSProperties = {
  width: 40,
  height: 40,
  borderRadius: 4,
  border: "1px solid #cbd5e1",
  background: "#f1f5f9",
  color: "#475569",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  fontWeight: 700,
  fontSize: 16,
};

const statusBadgeStyle: CSSProperties = {
  display: "inline-block",
  padding: "2px 8px",
  fontSize: 11,
  fontWeight: 600,
  borderRadius: 999,
  border: "1px solid transparent",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const drawerBackdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
  display: "flex",
  justifyContent: "flex-end",
  zIndex: 100,
};

const drawerStyle: CSSProperties = {
  background: "#ffffff",
  width: "min(560px, 100%)",
  height: "100%",
  display: "flex",
  flexDirection: "column",
  boxShadow: "-8px 0 24px rgba(15, 23, 42, 0.18)",
};

const drawerHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
  gap: 12,
};

const drawerTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 16,
  fontWeight: 600,
  color: "#0f172a",
};

const dialogCloseStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  fontSize: 22,
  lineHeight: 1,
  cursor: "pointer",
  color: "#64748b",
  padding: "0 4px",
};

const drawerTabBarStyle: CSSProperties = {
  display: "flex",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  overflowX: "auto",
};

const tabStyle: CSSProperties = {
  padding: "10px 14px",
  fontSize: 12,
  fontWeight: 600,
  border: "none",
  background: "transparent",
  color: "#475569",
  cursor: "pointer",
  borderBottom: "2px solid transparent",
};

const activeTabStyle: CSSProperties = {
  ...tabStyle,
  color: "#0f172a",
  borderBottom: "2px solid #0369a1",
};

const drawerContentStyle: CSSProperties = {
  padding: 16,
  overflowY: "auto",
  flex: 1,
};

const tabBodyStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const detailRowStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "120px 1fr",
  gap: 8,
  alignItems: "baseline",
};

const detailLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const detailValueStyle: CSSProperties = {
  fontSize: 13,
  color: "#0f172a",
};

const descriptionBlockStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const descriptionTextStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#334155",
  lineHeight: 1.5,
  whiteSpace: "pre-wrap",
};

const transitionSectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  marginTop: 8,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#f8fafc",
};

const transitionButtonRowStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  flexWrap: "wrap",
};

const formErrorStyle: CSSProperties = {
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const successBoxStyle: CSSProperties = {
  fontSize: 12,
  padding: 8,
  background: "#ecfdf5",
  border: "1px solid #86efac",
  color: "#166534",
  borderRadius: 4,
};

const tierBlockStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const tierBlockHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
};

const globalScopeBadgeStyle: CSSProperties = {
  ...statusBadgeStyle,
  background: "#dbeafe",
  color: "#1e3a8a",
  borderColor: "#93c5fd",
};

const scopedBadgeStyle: CSSProperties = {
  ...statusBadgeStyle,
  background: "#fef3c7",
  color: "#854d0e",
  borderColor: "#fde68a",
};

const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};
