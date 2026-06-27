/**
 * Guarded route placeholders (SAUI-03).
 *
 * Each route below wraps an honest "not yet wired" placeholder in
 * <RequirePermission /> so that:
 *
 *   - operators with the right permission see an empty-state surface
 *     telling them which downstream SAUI-* task will populate it;
 *   - operators without the right permission see the explicit 403 UI
 *     when they hit the URL directly (bookmark, paste, etc).
 *
 * NEVER ship mock business data on these routes. The follow-up
 * SuperAdmin tasks (SAUI-05+) will replace the placeholders with real
 * data fetched via the authenticated API client.
 */
import { createRoute } from "@tanstack/react-router";
import type { CSSProperties } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH, type NavEntry } from "@/lib/auth/navConfig";

function placeholderFor(path: string): NavEntry {
  const entry = NAV_BY_PATH[path];
  if (entry === undefined) {
    throw new Error(`guarded.tsx: no nav entry registered for ${path}`);
  }
  return entry;
}

function GuardedPlaceholder({ entry }: { entry: NavEntry }) {
  return (
    <RequirePermission entry={entry}>
      <section style={pageStyle} aria-labelledby={`gp-${entry.id}`}>
        <h1 id={`gp-${entry.id}`} style={headingStyle}>
          {entry.label}
        </h1>
        <p style={subStyle}>{entry.purpose}</p>
        <div style={emptyStyle} role="status" data-testid={`placeholder-${entry.id}`}>
          Surface authorized. Data wiring will land in a follow-up
          SAUI-* task; no mock data is shown here.
        </div>
      </section>
    </RequirePermission>
  );
}

function makeRoute(path: string) {
  const entry = placeholderFor(path);
  return createRoute({
    getParentRoute: () => RootRoute,
    path,
    component: () => <GuardedPlaceholder entry={entry} />,
  });
}

export const NetworksRoute = makeRoute("/networks");
export const OrganizationsRoute = makeRoute("/organizations");
export const OrdersRoute = makeRoute("/orders");
export const TicketsRoute = makeRoute("/tickets");
export const RefundsRoute = makeRoute("/refunds");
export const GeoRoute = makeRoute("/geo");

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  maxWidth: 720,
};
const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: -0.2,
};
const subStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#475569",
};
const emptyStyle: CSSProperties = {
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 4,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};
