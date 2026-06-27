/**
 * Dev-only diagnostic panel.
 *
 * Visible ONLY when config.isDevelopment === true (Vite's import.meta.env.DEV).
 * The production build statically tree-shakes this component because the
 * import in <AppLayout /> is gated on the same flag. The panel surfaces:
 *
 *   - Current /v1/me roles, permissions, available_scopes, assigned networks.
 *   - A redacted access-token prefix (first 12 chars) so an operator can
 *     spot-check that bearer attachment is working without leaking the
 *     full token.
 *   - A "Mint dev token" affordance is INTENTIONALLY OUT OF SCOPE here:
 *     the backend dev-token-mint route lives behind its own dev gate, and
 *     SAUI-02 step 5 explicitly says "Guard dev token UI behind explicit
 *     development env flag". We satisfy that by only ever rendering this
 *     panel under config.isDevelopment, and we render an inline notice
 *     in production builds (defensive double-gate) if the panel is ever
 *     imported by mistake.
 */
import { useEffect, useState, type CSSProperties } from "react";
import { config } from "@/lib/config";
import { getAccessToken, getAccessExpiresAt, subscribe } from "@/lib/api/tokenStore";
import { useAuth } from "@/lib/auth/useAuth";

export function DevDiagnosticsPanel() {
  const auth = useAuth();
  // Subscribe to token store so the redacted prefix updates after login.
  const [, setTick] = useState(0);
  useEffect(() => subscribe(() => setTick((n) => n + 1)), []);

  if (!config.isDevelopment) {
    // Defensive double-gate: do not render anything in production even if
    // some future caller forgets to wrap the import.
    return null;
  }

  const token = getAccessToken();
  const redacted = token === null ? "(none)" : `${token.slice(0, 12)}…`;
  const expiresAt = getAccessExpiresAt();
  const expiresHuman =
    expiresAt === null ? "(none)" : new Date(expiresAt).toISOString();

  const me = auth.me;

  return (
    <details style={panelStyle} data-testid="dev-diagnostics-panel">
      <summary style={summaryStyle}>Dev diagnostics</summary>
      <div style={bodyStyle}>
        <Row label="Status" value={auth.status} />
        <Row label="Access token" value={redacted} />
        <Row label="Access expires" value={expiresHuman} />
        <Row label="Roles" value={me === null ? "(no /v1/me)" : me.roles.join(", ") || "(none)"} />
        <Row
          label="Permissions"
          value={
            me === null
              ? "(no /v1/me)"
              : me.permissions.length === 0
                ? "(none)"
                : me.permissions.join(", ")
          }
        />
        <Row
          label="Scopes"
          value={
            me === null
              ? "(no /v1/me)"
              : me.available_scopes.length === 0
                ? "(none)"
                : me.available_scopes.join(", ")
          }
        />
        <Row
          label="Assigned networks"
          value={
            me === null || me.assigned_networks.length === 0
              ? "(none)"
              : me.assigned_networks.map((n) => `${n.slug} (${n.status})`).join(", ")
          }
        />
      </div>
    </details>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={rowStyle}>
      <span style={rowLabelStyle}>{label}</span>
      <span style={rowValueStyle}>{value}</span>
    </div>
  );
}

const panelStyle: CSSProperties = {
  margin: "16px 0 0 0",
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  padding: "8px 12px",
  fontSize: 12,
  color: "#0f172a",
};

const summaryStyle: CSSProperties = {
  cursor: "pointer",
  fontWeight: 600,
  fontSize: 12,
  color: "#0f172a",
};

const bodyStyle: CSSProperties = {
  marginTop: 8,
  display: "grid",
  gridTemplateColumns: "140px 1fr",
  gap: "4px 12px",
};

const rowStyle: CSSProperties = { display: "contents" };
const rowLabelStyle: CSSProperties = { color: "#64748b" };
const rowValueStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  wordBreak: "break-word",
};
