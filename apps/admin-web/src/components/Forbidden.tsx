import type { CSSProperties } from "react";
import { Link } from "@tanstack/react-router";
import { describeRule, type PermissionRule } from "@/lib/auth/navConfig";

/**
 * Generic 403 surface used when:
 *   - a route guard refuses navigation (RequirePermission), or
 *   - the backend returns 403 on a programmatic call (not yet wired
 *     here -- TanQuery error path will reuse this component).
 *
 * It is deliberately explicit:
 *   - states the route the operator tried to reach,
 *   - states which permission rule blocked them,
 *   - shows the caller's actual permission set on demand for self-debugging,
 *   - links back to the workspace.
 *
 * Never use as a generic "something went wrong" surface -- that hides
 * real authorization defects from operators.
 */
export interface ForbiddenProps {
  readonly route: string;
  readonly purpose: string;
  readonly required: PermissionRule;
  readonly heldPermissions: ReadonlySet<string>;
  readonly activeScopeLabel: string | null;
}

export function Forbidden({
  route,
  purpose,
  required,
  heldPermissions,
  activeScopeLabel,
}: ForbiddenProps) {
  return (
    <section aria-labelledby="forbidden-heading" style={containerStyle}>
      <h1 id="forbidden-heading" style={headingStyle}>
        403 — Access denied
      </h1>
      <p style={subStyle} data-testid="forbidden-route">
        You do not have permission to view <code style={codeStyle}>{route}</code>.
      </p>
      <dl style={dlStyle}>
        <dt style={dtStyle}>Surface</dt>
        <dd style={ddStyle}>{purpose}</dd>
        <dt style={dtStyle}>Required permission</dt>
        <dd style={ddStyle} data-testid="forbidden-required">
          {describeRule(required)}
        </dd>
        <dt style={dtStyle}>Active scope</dt>
        <dd style={ddStyle}>{activeScopeLabel ?? "none"}</dd>
        <dt style={dtStyle}>Permissions you currently hold</dt>
        <dd style={ddStyle}>
          {heldPermissions.size === 0 ? (
            <span>(none)</span>
          ) : (
            <ul style={permListStyle}>
              {[...heldPermissions].sort().map((p) => (
                <li key={p}>
                  <code style={codeStyle}>{p}</code>
                </li>
              ))}
            </ul>
          )}
        </dd>
      </dl>
      <p style={subStyle}>
        If you believe this is incorrect, ask a platform_superadmin to grant the
        missing permission, then sign out and back in to refresh your
        authorization context.
      </p>
      <p style={subStyle}>
        <Link to="/" style={linkStyle}>
          ← Back to workspace
        </Link>
      </p>
    </section>
  );
}

const containerStyle: CSSProperties = {
  maxWidth: 640,
  margin: "0 auto",
  display: "flex",
  flexDirection: "column",
  gap: 12,
  background: "#fff7ed",
  border: "1px solid #fdba74",
  padding: 20,
  borderRadius: 6,
  color: "#7c2d12",
};
const headingStyle: CSSProperties = { margin: 0, fontSize: 20, fontWeight: 600 };
const subStyle: CSSProperties = { margin: 0, fontSize: 13 };
const dlStyle: CSSProperties = { margin: 0, display: "grid", rowGap: 4 };
const dtStyle: CSSProperties = { fontSize: 11, fontWeight: 600, marginTop: 8 };
const ddStyle: CSSProperties = { margin: 0, fontSize: 13 };
const codeStyle: CSSProperties = {
  background: "#fed7aa",
  padding: "1px 4px",
  borderRadius: 3,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};
const permListStyle: CSSProperties = {
  margin: "4px 0 0 0",
  paddingLeft: 16,
  display: "flex",
  flexDirection: "column",
  gap: 2,
};
const linkStyle: CSSProperties = { color: "#7c2d12", textDecoration: "underline" };
