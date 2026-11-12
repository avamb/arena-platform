/**
 * SuperAdmin Audit shell (SAUI-11).
 *
 * The platform records audit rows in `audit_logs` (server-side) for
 * every cross-tenant mutation (see SAUI-04 reason injection + SAUI-08
 * roster mutations). However, the backend does NOT currently expose a
 * read endpoint for the audit log; there is no `GET /v1/admin/audit`
 * handler, no `/v1/admin/networks/{id}/audit` reader, and no
 * dedicated query layer for filtering audit rows by actor, action,
 * organization, or time window.
 *
 * This shell is intentionally honest:
 *   - The page is gated by `superadmin.read` (RequirePermission), so
 *     direct-URL navigation by an unprivileged operator hits the
 *     canonical 403 surface and the audit shell is never rendered.
 *   - No mock/sample/fake audit rows are shown. Mock data on an
 *     audit surface is worse than no data: it teaches operators to
 *     trust a screen that lies during an incident.
 *   - Each missing capability is rendered as a "backend gap" tile
 *     naming the exact endpoint family that has to land before the
 *     surface can be populated. When those endpoints ship, this
 *     shell can be expanded in place without a redesign.
 *   - Sensitive log access (raw payloads, masked PII) is gated
 *     behind a separate permission (`superadmin.view_sensitive_logs`)
 *     per 08_platform_superadmin_observability_ru.md. Because that
 *     permission is not granted on this build, the corresponding tile
 *     is rendered as masked/locked rather than silently absent.
 *
 * Backend gap list (G-IDs match SAUI-09 backlog format):
 *   GA1  GET /v1/admin/audit         -- list with actor/action/time filters
 *   GA2  GET /v1/admin/audit/{id}    -- detail record + linked entities
 *   GA3  GET /v1/admin/networks/{id}/audit -- network-scoped reader
 *   GA4  GET /v1/admin/orgs/{id}/audit     -- organization-scoped reader
 *   GA5  Sensitive log channel (raw bodies/PII) behind
 *        `superadmin.view_sensitive_logs` + step-up auth
 *   GA6  Audit row -> request/trace correlation surface
 *
 * Mock data: NONE.
 */
import { createRoute } from "@tanstack/react-router";
import type { CSSProperties } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH, describeRule } from "@/lib/auth/navConfig";
import { useAuth } from "@/lib/auth/useAuth";
import * as S from "@/lib/admin/supportStyles";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/audit",
  component: AuditRoute,
});

const NAV_ENTRY = NAV_BY_PATH["/audit"];
if (NAV_ENTRY === undefined) {
  throw new Error("audit route: NAV_BY_PATH['/audit'] missing");
}

/**
 * Permission used to gate raw / sensitive log access. Aligned with the
 * `platform.superadmin.view_sensitive_logs` permission specified in
 * 08_platform_superadmin_observability_ru.md. Exported so tests can
 * assert the exact string the UI checks.
 */
export const SENSITIVE_LOGS_PERMISSION = "superadmin.view_sensitive_logs";

export interface AuditBackendGap {
  readonly id: string;
  readonly label: string;
  readonly endpoint: string;
  readonly purpose: string;
  /**
   * When defined and not held by the caller, the tile is rendered in
   * its masked/locked variant (still shown -- never hidden -- so the
   * operator can see what they would gain with the missing grant).
   */
  readonly gatedBy?: string;
}

export const AUDIT_GAPS: readonly AuditBackendGap[] = [
  {
    id: "GA1",
    label: "List audit events",
    endpoint: "GET /v1/admin/audit",
    purpose:
      "Filter by actor, action group, organization, network, and time period. Go through the results page by page.",
  },
  {
    id: "GA2",
    label: "Audit event detail",
    endpoint: "GET /v1/admin/audit/{id}",
    purpose:
      "One audit row and the linked objects (order, ticket, refund, network, organization).",
  },
  {
    id: "GA3",
    label: "Audit for one network",
    endpoint: "GET /v1/admin/networks/{id}/audit",
    purpose:
      "Audit feed for one operator network only (matches SAUI-08 backlog gap G3).",
  },
  {
    id: "GA4",
    label: "Audit for one organization",
    endpoint: "GET /v1/admin/orgs/{id}/audit",
    purpose:
      "Audit feed for one tenant organization only. No rows of other organizations.",
  },
  {
    id: "GA5",
    label: "Sensitive log channel",
    endpoint: "GET /v1/admin/audit/{id}/raw",
    purpose:
      "Raw request and response bodies and PII without masks. A second sign-in step and a reason are necessary.",
    gatedBy: SENSITIVE_LOGS_PERMISSION,
  },
  {
    id: "GA6",
    label: "Trace link",
    endpoint: "GET /v1/admin/audit/{id}/trace",
    purpose:
      "Find the log span and the outbound webhook deliveries for a request_id or trace_id.",
  },
];

function AuditRoute() {
  return (
    <RequirePermission entry={NAV_ENTRY}>
      <AuditShell />
    </RequirePermission>
  );
}

function AuditShell() {
  const { permissions } = useAuth();
  return (
    <section style={S.pageStyle} aria-labelledby="audit-h1">
      <header style={S.headerStyle}>
        <div>
          <h1 id="audit-h1" style={S.headingStyle}>
            Audit log
          </h1>
          <p style={S.subheadingStyle}>
            This page is a shell. It shows no audit rows until a backend
            reader exists. Made-up data on an audit page can mislead
            operators during an incident. The endpoints below name the
            backlog items for this page. Permission rule: {describeRule(NAV_ENTRY.permission)}.
          </p>
        </div>
      </header>

      <div style={S.statusBoxStyle} role="status">
        The server records audit rows. Each cross-tenant change through
        /v1/admin/* has an X-Admin-Reason header and writes an audit_logs
        row. The API does not give these rows for reading. Do not add a
        placeholder table here. See the backlog items below.
      </div>

      <div style={S.drawerSectionStyle} aria-labelledby="audit-gaps-h2">
        <h2 id="audit-gaps-h2" style={S.drawerSectionTitleStyle}>
          Backend gaps for this page
        </h2>
        <div style={S.relatedGridStyle}>
          {AUDIT_GAPS.map((gap) => (
            <GapTile
              key={gap.id}
              gap={gap}
              permissions={permissions}
            />
          ))}
        </div>
      </div>

      <div style={S.gapNoteStyle}>
        <strong>No preview on this page.</strong> The audit_logs table is
        the source of truth for cross-tenant access. A fake preview,
        also with test data, teaches operators to trust an incorrect
        screen during a real incident. When the audit reader exists,
        the table will replace this note.
      </div>
    </section>
  );
}

function GapTile({
  gap,
  permissions,
}: {
  readonly gap: AuditBackendGap;
  readonly permissions: ReadonlySet<string>;
}) {
  const locked =
    gap.gatedBy !== undefined && !permissions.has(gap.gatedBy);
  return (
    <div
      style={S.relatedTileDisabledStyle}
      data-testid={`audit-gap-${gap.id}`}
      aria-label={`Backend gap ${gap.id}: ${gap.label}`}
    >
      <span style={S.relatedTileGapBadgeStyle}>
        Backend gap {gap.id}
      </span>
      <span style={S.relatedTileLabelStyle}>{gap.label}</span>
      <span style={S.relatedTileHintStyle}>{gap.endpoint}</span>
      <span style={hintParaStyle}>{gap.purpose}</span>
      {locked ? (
        <span
          style={S.errorBadgeStyle}
          data-testid={`audit-gap-${gap.id}-locked`}
        >
          Masked. Necessary permission: {gap.gatedBy}
        </span>
      ) : null}
    </div>
  );
}

const hintParaStyle: CSSProperties = {
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.4,
};
