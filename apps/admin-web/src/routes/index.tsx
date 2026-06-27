import { createRoute } from "@tanstack/react-router";
import type { CSSProperties } from "react";
import { Route as RootRoute } from "./__root";

/**
 * Authenticated admin workspace landing.
 *
 * Deliberately operational: no hero copy, no marketing. Shows the panels
 * an operator needs in their first 5 seconds -- recent activity slots,
 * environment indicator, and shortcuts. Real data wiring lands in the
 * follow-up auth + /v1/me feature (SAUI-02); this scaffold renders empty
 * states so the first authenticated screen is the workspace, not a stub.
 */
export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/",
  component: WorkspaceRoute,
});

function WorkspaceRoute() {
  return (
    <section aria-labelledby="workspace-heading" style={{ display: "grid", gap: 16 }}>
      <header>
        <h1 id="workspace-heading" style={headingStyle}>
          Workspace
        </h1>
        <p style={subheadingStyle}>
          Operational landing page. Authenticated context, recent activity, and
          shortcuts will populate here once /v1/me is wired (SAUI-02).
        </p>
      </header>

      <div style={cardGridStyle}>
        <WorkspaceCard title="Recent activity" hint="Audit log feed (pending wiring)." />
        <WorkspaceCard title="Assigned networks" hint="From /v1/me.assigned_networks (pending wiring)." />
        <WorkspaceCard title="Pending tasks" hint="Operator queues (pending wiring)." />
      </div>
    </section>
  );
}

function WorkspaceCard({ title, hint }: { title: string; hint: string }) {
  return (
    <article style={cardStyle}>
      <h2 style={cardTitleStyle}>{title}</h2>
      <p style={cardHintStyle}>{hint}</p>
      <div style={emptyStateStyle} role="status">
        Empty — no data yet.
      </div>
    </article>
  );
}

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
};

const cardGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))",
  gap: 12,
};

const cardStyle: CSSProperties = {
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  padding: 16,
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const cardTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 14,
  fontWeight: 600,
  color: "#0f172a",
};

const cardHintStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#64748b",
};

const emptyStateStyle: CSSProperties = {
  padding: 12,
  border: "1px dashed #cbd5e1",
  borderRadius: 4,
  background: "#f8fafc",
  fontSize: 12,
  color: "#64748b",
  textAlign: "center",
};
