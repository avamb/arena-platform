import { Link, Outlet } from "@tanstack/react-router";
import type { CSSProperties } from "react";
import { AuthGate } from "@/components/AuthGate";
import { DevDiagnosticsPanel } from "@/components/DevDiagnosticsPanel";
import { ScopeSelector } from "@/components/ScopeSelector";
import { ActiveReasonBadge } from "@/components/ActiveReasonBadge";
import { ReasonPromptModal } from "@/components/ReasonPromptModal";
import { LocaleSwitcher } from "@/components/LocaleSwitcher";
import { config } from "@/lib/config";
import { useAuth } from "@/lib/auth/useAuth";
import { useScope } from "@/lib/auth/ScopeContext";
import { useTranslation } from "@/lib/i18n/I18nContext";
import {
  NAV_ENTRIES,
  visibleNavEntries,
  type NavEntry,
} from "@/lib/auth/navConfig";

/**
 * Permission-driven admin shell (SAUI-03).
 *
 * Sidebar contents are derived from the caller's /v1/me.permissions and
 * the currently active scope. No role names are hardcoded anywhere in
 * this file -- if the backend grants a permission, the surface appears;
 * otherwise it does not. Role presets (platform_superadmin, etc.) only
 * influence the *default* active scope, not what the sidebar contains.
 *
 * Surfaces hidden from the sidebar remain unreachable through the
 * navigation, but the corresponding routes are *additionally* protected
 * by <RequirePermission /> so that direct URL access shows the 403 UI
 * rather than a broken page. The two layers are deliberately redundant.
 */
export function AppLayout() {
  const auth = useAuth();
  const { t } = useTranslation();
  const authed = auth.status === "authenticated";
  return (
    <div style={shellStyle}>
      <aside style={sidebarStyle} aria-label={t("shell.nav.aria")}>
        <div style={brandStyle}>
          <span style={brandMarkStyle} aria-hidden="true" />
          <span>{t("shell.brand")}</span>
        </div>
        {authed ? <AuthenticatedSidebarNav /> : <UnauthenticatedSidebarNav />}
        {authed && auth.me !== null ? (
          <div style={userBlockStyle}>
            <div style={userIdStyle}>{auth.me.user.id}</div>
            <div style={userRolesStyle}>
              {auth.me.roles.join(", ") || t("shell.noRoles")}
            </div>
            <button
              type="button"
              onClick={() => void auth.logout()}
              style={logoutBtnStyle}
            >
              {t("shell.signOut")}
            </button>
          </div>
        ) : null}
        {config.isDevelopment ? <DevDiagnosticsPanel /> : null}
        <div style={sidebarFooterStyle}>
          v0.1.0 {config.isDevelopment ? `· ${t("shell.devSuffix")}` : null}
        </div>
      </aside>
      <main style={mainStyle}>
        {authed ? <AuthenticatedTopBar /> : null}
        <AuthGate>
          <Outlet />
        </AuthGate>
      </main>
      {authed ? <ReasonPromptModal /> : null}
    </div>
  );
}

function AuthenticatedSidebarNav() {
  const { permissions } = useAuth();
  const { activeScopeKind } = useScope();
  const { t } = useTranslation();
  const entries = visibleNavEntries(NAV_ENTRIES, permissions, activeScopeKind);
  return (
    <nav style={navStyle} data-testid="primary-nav">
      {entries.map((entry) => (
        <NavItem key={entry.id} entry={entry} />
      ))}
      {entries.length === 0 ? (
        <p style={emptyNavStyle} data-testid="nav-empty" role="status">
          {t("shell.nav.empty")}
        </p>
      ) : null}
    </nav>
  );
}

function UnauthenticatedSidebarNav() {
  const { t } = useTranslation();
  return (
    <nav style={navStyle} data-testid="primary-nav">
      <Link
        to="/login"
        style={navLinkStyle}
        activeProps={{ style: navLinkActiveStyle }}
        data-testid="nav-login"
      >
        {t("shell.signIn")}
      </Link>
    </nav>
  );
}

function NavItem({ entry }: { entry: NavEntry }) {
  const { t, locale } = useTranslation();
  // Resolve label via i18n: explicit labelKey > nav.<id> convention > raw label.
  const key = entry.labelKey ?? `nav.${entry.id}`;
  const translated = t(key);
  // If t() falls back to the raw key (missing translation), use entry.label.
  const label = translated === key ? entry.label : translated;
  // TanStack Router's typed Link narrows `to` based on the inferred
  // current-route context; rendering from a NAV_ENTRIES table requires
  // a string-cast. Runtime safety: each NAV_ENTRIES.to is a path
  // registered in routeTree.ts (enforced by `NavRoutePath`), so the
  // value is always a valid known route.
  return (
    <Link
      to={entry.to as "/"}
      style={navLinkStyle}
      activeProps={{ style: navLinkActiveStyle }}
      data-testid={`nav-${entry.id}`}
      data-nav-id={entry.id}
      data-nav-locale={locale}
      title={entry.purpose}
    >
      {label}
    </Link>
  );
}

function AuthenticatedTopBar() {
  const { activeScope } = useScope();
  const { t } = useTranslation();
  return (
    <header style={topBarStyle} data-testid="shell-topbar">
      <ScopeSelector />
      <ActiveReasonBadge />
      <span style={topBarMetaStyle}>
        {activeScope === null
          ? t("shell.scopeNone")
          : t("shell.scopeActive", { label: activeScope.label })}
      </span>
      <span style={topBarSpacerStyle} />
      <LocaleSwitcher />
    </header>
  );
}

const userBlockStyle: CSSProperties = {
  marginTop: 8,
  padding: "8px 10px",
  background: "#1e293b",
  borderRadius: 4,
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const userIdStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 10,
  color: "#cbd5e1",
  wordBreak: "break-all",
};

const userRolesStyle: CSSProperties = {
  fontSize: 11,
  color: "#94a3b8",
};

const logoutBtnStyle: CSSProperties = {
  marginTop: 4,
  background: "#7f1d1d",
  color: "#fff",
  border: 0,
  padding: "6px 8px",
  borderRadius: 4,
  fontSize: 11,
  cursor: "pointer",
};

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

const emptyNavStyle: CSSProperties = {
  margin: "4px 8px",
  padding: "8px 10px",
  background: "#1e293b",
  color: "#94a3b8",
  fontSize: 11,
  borderRadius: 4,
  fontStyle: "italic",
  lineHeight: 1.4,
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

const topBarStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 12,
  marginBottom: 20,
  paddingBottom: 12,
  borderBottom: "1px solid #e2e8f0",
};

const topBarMetaStyle: CSSProperties = {
  fontSize: 12,
  color: "#64748b",
};

const topBarSpacerStyle: CSSProperties = {
  flex: 1,
};
