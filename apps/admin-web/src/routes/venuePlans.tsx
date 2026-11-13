/**
 * Seating plans (spec 08_architecture/64, sections 3.3 and 3.4), platform staff only.
 *
 *   /venue-plans                 the queue of plan intakes and the tab "Waiting for verification"
 *   /venue-plans/$intakeId       one intake's card (venuePlanDetail.tsx)
 *
 * The administrator notifications link to /venue-plans/<intake_id>. The queue needs
 * venue_plan_intake.curate; the verification tab needs seating_plan.verify. Each tab
 * shows only to a caller who holds its permission.
 *
 * Mock data: NONE. The page hits the live backend.
 */
import { createRoute, Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ResponsiveTable, type ResponsiveTableColumn } from "@/components/layout";
import { ApiError } from "@/lib/api/client";
import {
  INTAKE_STATES,
  intakeStateLabel,
  isFinalState,
  listIntakes,
  waitingLabel,
  type VenuePlanIntakeListItem,
} from "@/lib/api/venuePlans";
import { getUserId } from "@/lib/api/tokenStore";
import { formatDateTime } from "@/lib/admin/supportConsole";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { useAuth } from "@/lib/auth/useAuth";
import { VerificationPanel } from "./venuePlanVerification";
import {
  Pill,
  buttonStyle,
  errorStyle,
  headerRowStyle,
  headingStyle,
  inputStyle,
  linkButtonStyle,
  mutedStyle,
  pageStyle,
  subheadingStyle,
} from "./onboardingUi";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/venue-plans",
  component: VenuePlansRoute,
  validateSearch: (raw: Record<string, unknown>): VenuePlansSearch => parseVenuePlansSearch(raw),
});

export interface VenuePlansSearch {
  readonly tab?: VenuePlansTab;
  readonly plan?: string;
}

/** Reads ?tab=queue|verification and ?plan=<plan id> from the address. */
export function parseVenuePlansSearch(raw: Record<string, unknown>): VenuePlansSearch {
  const tab = raw.tab === "queue" || raw.tab === "verification" ? raw.tab : undefined;
  const plan = typeof raw.plan === "string" && raw.plan.length > 0 ? raw.plan : undefined;
  return { tab, plan };
}

const VENUE_PLANS_NAV_ENTRY = NAV_BY_PATH["/venue-plans"];
if (VENUE_PLANS_NAV_ENTRY === undefined) {
  throw new Error("venue plans route: NAV_BY_PATH['/venue-plans'] missing");
}

function VenuePlansRoute() {
  return (
    <RequirePermission entry={VENUE_PLANS_NAV_ENTRY}>
      <VenuePlansPage />
    </RequirePermission>
  );
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

export type VenuePlansTab = "queue" | "verification";

/** The tabs the caller may open, in order. */
export function availableTabs(permissions: ReadonlySet<string>): readonly VenuePlansTab[] {
  const tabs: VenuePlansTab[] = [];
  if (permissions.has("venue_plan_intake.curate")) tabs.push("queue");
  if (permissions.has("seating_plan.verify")) tabs.push("verification");
  return tabs;
}

export function stateTone(state: string): "good" | "warn" | "bad" | "info" | "muted" {
  switch (state) {
    case "approved":
      return "good";
    case "rejected":
      return "bad";
    case "operator":
      return "warn";
    case "received":
    case "generating":
    case "review":
      return "info";
    default:
      return "muted";
  }
}

/** The waiting time of a row: counted for open intakes only. */
export function waitingFor(item: Pick<VenuePlanIntakeListItem, "state" | "created_at">, now?: number): string {
  return isFinalState(item.state) ? "—" : waitingLabel(item.created_at, now);
}

export function claimedByText(item: Pick<VenuePlanIntakeListItem, "operator_claimed" | "operator_id">, callerId: string | null): string {
  if (!item.operator_claimed || item.operator_id === null || item.operator_id === undefined) return "—";
  if (callerId !== null && item.operator_id === callerId) return "You";
  return `Administrator ${item.operator_id.slice(0, 8)}`;
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

function VenuePlansPage(): JSX.Element {
  const { permissions } = useAuth();
  const tabs = availableTabs(permissions);
  const search = useSearch({ from: "/venue-plans" }) as VenuePlansSearch;
  const navigate = useNavigate({ from: "/venue-plans" });
  const active: VenuePlansTab | null = search.tab !== undefined && tabs.includes(search.tab) ? search.tab : (tabs[0] ?? null);
  const setTab = (t: VenuePlansTab): void => {
    void navigate({ search: { tab: t } });
  };

  return (
    <div style={pageStyle} data-testid="venue-plans-page">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle}>Seating plans</h1>
          <p style={subheadingStyle}>
            Organizers send seating plans for their venues. Work through the queue here. Verify a plan on the second tab.
            A verified plan becomes the template of its venue.
          </p>
        </div>
      </div>

      <PlansTabs tabs={tabs} active={active} onSelect={setTab} />

      {active === "queue" ? <QueuePanel /> : null}
      {active === "verification" ? <VerificationPanel planId={search.plan} /> : null}
      {active === null ? <p style={mutedStyle}>Your account has no permission for a tab here. Tell a platform administrator to give you access.</p> : null}
    </div>
  );
}

const TAB_LABELS: Readonly<Record<VenuePlansTab, string>> = {
  queue: "Queue",
  verification: "Plans to verify",
};

export function PlansTabs({
  tabs,
  active,
  onSelect,
}: {
  readonly tabs: readonly VenuePlansTab[];
  readonly active: VenuePlansTab | null;
  readonly onSelect: (t: VenuePlansTab) => void;
}): JSX.Element {
  return (
    <div
      role="tablist"
      aria-label="Seating plans"
      style={{ display: "flex", gap: 4, flexWrap: "wrap", borderBottom: "1px solid #e2e8f0", marginBottom: 12 }}
    >
      {tabs.map((t) => {
        const selected = t === active;
        return (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={selected}
            onClick={() => onSelect(t)}
            data-testid={`venue-plans-tab-${t}`}
            style={{
              ...buttonStyle,
              border: "none",
              borderBottom: selected ? "2px solid #0369a1" : "2px solid transparent",
              borderRadius: 0,
              background: "transparent",
              fontWeight: selected ? 600 : 400,
            }}
          >
            {TAB_LABELS[t]}
          </button>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

function QueuePanel(): JSX.Element {
  const [state, setState] = useState("");
  const [unclaimed, setUnclaimed] = useState(false);
  const query = useQuery<readonly VenuePlanIntakeListItem[], ApiError>({
    queryKey: ["admin", "venue-plans", "intakes", state, unclaimed],
    queryFn: () => listIntakes({ state, unclaimed }),
    refetchOnWindowFocus: false,
    retry: false,
  });

  return (
    <div data-testid="venue-plans-queue">
      <QueueFilters
        state={state}
        unclaimed={unclaimed}
        onState={setState}
        onUnclaimed={setUnclaimed}
        onReload={() => void query.refetch()}
      />
      {query.isLoading ? <p style={mutedStyle}>Loading…</p> : null}
      {query.error ? (
        <div style={errorStyle} data-testid="venue-plans-error">
          We did not load the requests:{query.error.message}
        </div>
      ) : null}
      {query.data ? (
        <IntakeQueueTable
          items={query.data}
          callerId={getUserId()}
          renderCardLink={(id, children) => (
            <Link to="/venue-plans/$intakeId" params={{ intakeId: id }} data-testid={`venue-plans-open-${id}`}>
              {children}
            </Link>
          )}
        />
      ) : null}
    </div>
  );
}

export function QueueFilters({
  state,
  unclaimed,
  onState,
  onUnclaimed,
  onReload,
}: {
  readonly state: string;
  readonly unclaimed: boolean;
  readonly onState: (s: string) => void;
  readonly onUnclaimed: (u: boolean) => void;
  readonly onReload: () => void;
}): JSX.Element {
  return (
    <div style={{ display: "flex", gap: 12, alignItems: "flex-end", flexWrap: "wrap", marginBottom: 12 }}>
      <label style={{ fontSize: 12, fontWeight: 600, color: "#334155" }}>
        <span style={{ display: "block", marginBottom: 4 }}>Status</span>
        <select
          value={state}
          onChange={(e) => onState(e.target.value)}
          style={{ ...inputStyle, width: 220 }}
          data-testid="venue-plans-filter-state"
        >
          <option value="">All states</option>
          {INTAKE_STATES.map((s) => (
            <option key={s} value={s}>
              {intakeStateLabel(s)}
            </option>
          ))}
        </select>
      </label>
      <label style={{ fontSize: 13, display: "flex", gap: 6, alignItems: "center", paddingBottom: 6 }}>
        <input
          type="checkbox"
          checked={unclaimed}
          onChange={(e) => onUnclaimed(e.target.checked)}
          data-testid="venue-plans-filter-unclaimed"
        />
        No administrator holds it
      </label>
      <button type="button" style={buttonStyle} onClick={onReload} data-testid="venue-plans-reload">
        Reload
      </button>
    </div>
  );
}

export interface IntakeQueueTableProps {
  readonly items: readonly VenuePlanIntakeListItem[];
  readonly renderCardLink: (id: string, children: ReactNode) => ReactNode;
  readonly callerId: string | null;
  readonly now?: number;
  readonly forceLayout?: "desktop" | "mobile";
}

export function IntakeQueueTable({ items, renderCardLink, callerId, now, forceLayout }: IntakeQueueTableProps): JSX.Element {
  const columns: ResponsiveTableColumn<VenuePlanIntakeListItem>[] = [
    {
      id: "organization",
      header: "Organization",
      primary: true,
      renderCell: (i) => (
        <div data-testid={`venue-plans-row-${i.id}`} style={{ fontWeight: 600 }}>
          {renderCardLink(i.id, i.org_name ?? i.org_id.slice(0, 8))}
        </div>
      ),
    },
    {
      id: "venue",
      header: "Venue",
      renderCell: (i) => (
        <div>
          <div>{i.venue_name}</div>
          {i.venue_city ? <div style={{ fontSize: 12, color: "#64748b" }}>{i.venue_city}</div> : null}
        </div>
      ),
    },
    {
      id: "received",
      header: "Received",
      renderCell: (i) => formatDateTime(i.created_at),
    },
    {
      id: "state",
      header: "State",
      renderCell: (i) => (
        <Pill tone={stateTone(i.state)} testId={`venue-plans-state-${i.id}`}>
          {intakeStateLabel(i.state)}
        </Pill>
      ),
    },
    {
      id: "waiting",
      header: "Waiting",
      renderCell: (i) => <span data-testid={`venue-plans-waiting-${i.id}`}>{waitingFor(i, now)}</span>,
    },
    {
      id: "claimed",
      header: "Held by",
      renderCell: (i) => claimedByText(i, callerId),
    },
    {
      id: "open",
      header: "",
      hideOnMobile: true,
      renderCell: (i) => renderCardLink(i.id, <span style={linkButtonStyle}>Open</span>),
    },
  ];
  return (
    <ResponsiveTable<VenuePlanIntakeListItem>
      id="venue-plans-table"
      caption="Seating plan requests"
      columns={columns}
      rows={items}
      rowKey={(i) => i.id}
      forceLayout={forceLayout}
      empty="No requests for these filters."
    />
  );
}
