/**
 * SAUI-12 -- shared placeholder UI for legacy-derived module routes.
 *
 * Renders a permission-gated "shell-only" surface that names:
 *
 *   - which legacy app the module supersedes (TixManager/TixEditor/
 *     TixCassa/TixReporter) and which screenshots in
 *     04_legacy_screenshots/* the planning artifact references;
 *   - the legacy_admin_reference_map.yaml module id so an operator can
 *     cross-check the planned scope, role matrix, and MVP priority;
 *   - the EXPECTED future scope of the real module (bullet list); and
 *   - the SPECIFIC deferral reason (why this milestone does not ship
 *     the module yet).
 *
 * The component intentionally renders NO tables, NO synthetic counts,
 * and NO mocked rows. That is the SAUI-12 contract -- a shell that
 * tells the truth instead of pretending to be a working module.
 *
 * When a follow-up SAUI-* task lands the real module, replace the
 * placeholder route file with a real route module (mirroring SAUI-07
 * networks, SAUI-10 orders/tickets/refunds, SAUI-11 audit/observability)
 * and remove the corresponding entry from LEGACY_MODULE_PLACEHOLDERS.
 */
import type { CSSProperties } from "react";
import { RequirePermission } from "@/components/RequirePermission";
import {
  NAV_BY_PATH,
  describeRule,
  type NavEntry,
} from "@/lib/auth/navConfig";
import type { LegacyModulePlaceholder } from "@/lib/admin/legacyModules";

export interface LegacyModulePlaceholderProps {
  readonly module: LegacyModulePlaceholder;
}

export function LegacyModulePlaceholderRoute({
  module,
}: LegacyModulePlaceholderProps) {
  const navEntry = navEntryForModule(module);
  return (
    <RequirePermission entry={navEntry}>
      <PlaceholderBody module={module} navEntry={navEntry} />
    </RequirePermission>
  );
}

function navEntryForModule(module: LegacyModulePlaceholder): NavEntry {
  const entry = NAV_BY_PATH[module.path];
  if (entry === undefined) {
    throw new Error(
      `LegacyModulePlaceholder: nav entry missing for path ${module.path}. ` +
        `Did navConfig.NAV_ENTRIES forget to register this module?`,
    );
  }
  return entry;
}

function PlaceholderBody({
  module,
  navEntry,
}: {
  readonly module: LegacyModulePlaceholder;
  readonly navEntry: NavEntry;
}) {
  const h1Id = `lm-${module.id}-h1`;
  return (
    <section
      style={pageStyle}
      aria-labelledby={h1Id}
      data-testid={`legacy-module-${module.id}`}
    >
      <header style={headerStyle}>
        <div>
          <p style={eyebrowStyle}>
            Legacy-derived module placeholder (SAUI-12)
          </p>
          <h1 id={h1Id} style={headingStyle}>
            {module.label}
          </h1>
          <p style={subheadingStyle}>{module.purpose}</p>
          <p style={subheadingStyle}>
            Permission rule: {describeRule(navEntry.permission)}. Scope
            filter:{" "}
            {navEntry.scopeKinds === undefined
              ? "any scope"
              : navEntry.scopeKinds.join(" / ")}
            .
          </p>
        </div>
        <div
          style={priorityBadgeStyle(module.mvpPriority)}
          data-testid={`legacy-module-${module.id}-priority`}
          aria-label={`MVP priority ${module.mvpPriority}`}
        >
          MVP {module.mvpPriority}
        </div>
      </header>

      <div style={statusBoxStyle} role="status">
        Shell only. No tables, counts, or mocked rows are rendered here.
        SAUI-12 deliberately ships this surface as a placeholder so the
        unified admin navigation matches the legacy_admin_reference_map
        plan WITHOUT pretending the module is live. A follow-up SAUI-*
        task will replace this route with a real module.
      </div>

      <section aria-labelledby={`${h1Id}-source`} style={sectionStyle}>
        <h2 id={`${h1Id}-source`} style={sectionTitleStyle}>
          Legacy source reference
        </h2>
        <dl style={metaListStyle}>
          <dt style={metaKeyStyle}>Legacy map module id</dt>
          <dd
            style={metaValStyle}
            data-testid={`legacy-module-${module.id}-mapid`}
          >
            <code>{module.sourceReference.legacyMapModuleId}</code> in{" "}
            <code>
              09_autoforge/admin_ui/legacy_admin_reference_map.yaml
            </code>
          </dd>
          <dt style={metaKeyStyle}>Supersedes legacy apps</dt>
          <dd style={metaValStyle}>
            {module.sourceReference.legacyApps.join(", ")}
          </dd>
          <dt style={metaKeyStyle}>Legacy screenshots</dt>
          <dd style={metaValStyle}>
            <ul style={screenListStyle}>
              {module.sourceReference.legacyScreens.map((s) => (
                <li key={s} style={screenItemStyle}>
                  <code>{s}</code>
                </li>
              ))}
            </ul>
          </dd>
          <dt style={metaKeyStyle}>Workflow shape</dt>
          <dd style={metaValStyle}>{module.workflowShape.join(", ")}</dd>
        </dl>
      </section>

      <section aria-labelledby={`${h1Id}-future`} style={sectionStyle}>
        <h2 id={`${h1Id}-future`} style={sectionTitleStyle}>
          Expected future scope
        </h2>
        <ul
          style={bulletListStyle}
          data-testid={`legacy-module-${module.id}-future`}
        >
          {module.futureScope.map((line) => (
            <li key={line} style={bulletItemStyle}>
              {line}
            </li>
          ))}
        </ul>
      </section>

      <section aria-labelledby={`${h1Id}-deferral`} style={sectionStyle}>
        <h2 id={`${h1Id}-deferral`} style={sectionTitleStyle}>
          Reason for deferral
        </h2>
        <p
          style={deferralStyle}
          data-testid={`legacy-module-${module.id}-deferral`}
        >
          {module.deferralReason}
        </p>
      </section>
    </section>
  );
}

// ---------- styles (kept inline; no shared support tokens needed) ----------

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
  maxWidth: 880,
};

const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
};

const eyebrowStyle: CSSProperties = {
  margin: 0,
  fontSize: 11,
  textTransform: "uppercase",
  letterSpacing: 0.5,
  color: "#64748b",
  fontWeight: 600,
};

const headingStyle: CSSProperties = {
  margin: "2px 0 0 0",
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: -0.2,
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  lineHeight: 1.45,
};

function priorityBadgeStyle(p: "P0" | "P1" | "P2"): CSSProperties {
  const palette: Record<"P0" | "P1" | "P2", { bg: string; fg: string }> = {
    P0: { bg: "#fee2e2", fg: "#7f1d1d" },
    P1: { bg: "#fef3c7", fg: "#78350f" },
    P2: { bg: "#e0e7ff", fg: "#3730a3" },
  };
  const { bg, fg } = palette[p];
  return {
    alignSelf: "flex-start",
    fontSize: 11,
    fontWeight: 600,
    padding: "4px 10px",
    borderRadius: 999,
    background: bg,
    color: fg,
    textTransform: "uppercase",
    letterSpacing: 0.4,
  };
}

const statusBoxStyle: CSSProperties = {
  padding: 14,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.45,
};

const sectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 14,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const sectionTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
  textTransform: "uppercase",
  letterSpacing: 0.5,
};

const metaListStyle: CSSProperties = {
  margin: 0,
  display: "grid",
  gridTemplateColumns: "minmax(160px, max-content) 1fr",
  rowGap: 8,
  columnGap: 12,
  fontSize: 12,
};
const metaKeyStyle: CSSProperties = { margin: 0, color: "#64748b" };
const metaValStyle: CSSProperties = {
  margin: 0,
  color: "#0f172a",
  wordBreak: "break-word",
};
const screenListStyle: CSSProperties = {
  margin: 0,
  paddingLeft: 18,
  display: "flex",
  flexDirection: "column",
  gap: 2,
};
const screenItemStyle: CSSProperties = {
  fontSize: 11,
  color: "#334155",
};

const bulletListStyle: CSSProperties = {
  margin: 0,
  paddingLeft: 18,
  display: "flex",
  flexDirection: "column",
  gap: 4,
};
const bulletItemStyle: CSSProperties = {
  fontSize: 12,
  color: "#0f172a",
  lineHeight: 1.45,
};

const deferralStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.5,
};
