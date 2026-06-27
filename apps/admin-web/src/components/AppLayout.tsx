import { Link, Outlet } from "@tanstack/react-router";
import type { CSSProperties } from "react";

/**
 * Base admin layout.
 *
 * Dense, operational shell: fixed-width left navigation, top header with
 * environment indicator, content area renders nested route via <Outlet />.
 * No marketing surface. The first authenticated screen is the workspace.
 */
export function AppLayout() {
  return (
    <div style={shellStyle}>
      <aside style={sidebarStyle} aria-label="Primary navigation">
        <div style={brandStyle}>
          <span style={brandMarkStyle} aria-hidden="true" />
          <span>Arena Admin</span>
        </div>
        <nav style={navStyle}>
          <Link to="/" style={navLinkStyle} activeProps={{ style: navLinkActiveStyle }}>
            Workspace
          </Link>
          <Link
            to="/login"
            style={navLinkStyle}
            activeProps={{ style: navLinkActiveStyle }}
          >
            Sign in
          </Link>
        </nav>
        <div style={sidebarFooterStyle}>v0.1.0 · scaffold</div>
      </aside>
      <main style={mainStyle}>
        <Outlet />
      </main>
    </div>
  );
}

const shellStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "240px 1fr",
  minHeight: "100vh",
  background: "#f8fafc",
  color: "#0f172a",
  fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif",
};

const sidebarStyle: CSSProperties = {
  background: "#0f172a",
  color: "#e2e8f0",
  display: "flex",
  flexDirection: "column",
  padding: "16px 12px",
  gap: 16,
  borderRight: "1px solid #1e293b",
};

const brandStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  padding: "4px 8px",
  fontWeight: 600,
  fontSize: 14,
  letterSpacing: 0.2,
};

const brandMarkStyle: CSSProperties = {
  width: 10,
  height: 10,
  background: "#22d3ee",
  borderRadius: 2,
  display: "inline-block",
};

const navStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 2,
};

const navLinkStyle: CSSProperties = {
  display: "block",
  padding: "8px 10px",
  color: "#cbd5e1",
  textDecoration: "none",
  fontSize: 13,
  borderRadius: 4,
};

const navLinkActiveStyle: CSSProperties = {
  background: "#1e293b",
  color: "#f8fafc",
};

const sidebarFooterStyle: CSSProperties = {
  marginTop: "auto",
  fontSize: 11,
  color: "#64748b",
  padding: "4px 10px",
};

const mainStyle: CSSProperties = {
  padding: 24,
  overflowY: "auto",
};
