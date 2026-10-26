/**
 * <StaffOnlyScreen /> replaces the whole console for a signed-in user who is
 * not Arena platform staff (GET /v1/me `platform_staff` === false). The
 * console is for staff only, who get access through an individual superadmin
 * invitation; organizers work in the web event center. Nothing of the console
 * is rendered behind this screen. The server refuses the same user at sign-in
 * and refresh (403 `auth.console_staff_only`), so this is the second line.
 */
import type { CSSProperties } from "react";
import { CONSOLE_STAFF_ONLY_MESSAGE, eventCenterUrl } from "@/lib/api/channel";
import { useAuth } from "@/lib/auth/useAuth";

export function StaffOnlyScreen() {
  const auth = useAuth();
  return (
    <div role="alert" style={boxStyle} data-testid="staff-only-screen">
      <h1 style={{ margin: 0, fontSize: 20 }}>This console is for Arena staff</h1>
      <p style={{ margin: "8px 0", fontSize: 14 }}>{CONSOLE_STAFF_ONLY_MESSAGE}</p>
      <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
        <a href={eventCenterUrl()} style={primaryBtn} data-testid="staff-only-event-center-link">
          Open the event center
        </a>
        <button
          type="button"
          onClick={() => void auth.logout()}
          style={secondaryBtn}
          data-testid="staff-only-sign-out"
        >
          Sign out
        </button>
      </div>
    </div>
  );
}

const boxStyle: CSSProperties = {
  fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif",
  padding: 24,
  maxWidth: 520,
  margin: "10vh auto",
  border: "1px solid #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  color: "#0f172a",
};

const primaryBtn: CSSProperties = {
  padding: "10px 16px",
  minHeight: 44,
  boxSizing: "border-box",
  border: "1px solid #0f172a",
  background: "#0f172a",
  color: "#f8fafc",
  borderRadius: 6,
  fontSize: 14,
  textDecoration: "none",
  display: "inline-flex",
  alignItems: "center",
};

const secondaryBtn: CSSProperties = {
  padding: "10px 16px",
  minHeight: 44,
  boxSizing: "border-box",
  border: "1px solid #0f172a",
  background: "#fff",
  color: "#0f172a",
  borderRadius: 6,
  cursor: "pointer",
  fontSize: 14,
};
