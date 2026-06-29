import { Link, Outlet } from "@tanstack/react-router";
import { useEffect, useState, type CSSProperties } from "react";
import { AuthGate } from "@/components/AuthGate";
import { DevDiagnosticsPanel } from "@/components/DevDiagnosticsPanel";
import { ScopeSelector } from "@/components/ScopeSelector";
import { ActiveReasonBadge } from "@/components/ActiveReasonBadge";
import { ReasonPromptModal } from "@/components/ReasonPromptModal";
import { LocaleSwitcher } from "@/components/LocaleSwitcher";
import { useIsDesktop } from "@/components/layout/useMediaQuery";
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
 * Permission-driven admin shell (SAUI-03 + Wave M-2).
 *
 * Sidebar contents are derived from the caller's /v1/me.permissions and
 * the currently active scope. No role names are hardcoded anywhere in
 * this file for permission gating -- if the backend grants a permission,
 * the surface appears; otherwise it does not. Role presets only influence
 * the default active scope and (Wave M-2) whether the responsive mobile
 * shell is suppressed for the SuperAdmin preset.
 *
 * Responsive behaviour (Wave M-2):
 *
 *   - At >= md (768 px): two-column desktop shell, persistent sidebar,
 *     persistent top bar (existing behaviour).
 *
 *   - Below md: single-column mobile shell. A sticky header carries the
 *     brand, hamburger button, scope chip, and reason badge. The nav
 *     drawer slides from the left and contains nav entries, the locale
 *     switcher, the user block, and the sign-out button. Bottom padding
 *     respects iOS browser chrome via `env(safe-area-inset-bottom)`.
 *
 *   - SuperAdmin opt-out: when the caller's role list contains
 *     `platform_superadmin`, the desktop layout is kept regardless of
 *     viewport. SuperAdmins routinely operate inside multi-pane forensic
 *     surfaces that do not collapse meaningfully on a phone; forcing the
 *     desktop layout matches the spec for this milestone.
 */
export interface AppLayoutProps {
  /**
   * Force a specific layout. Defaults to a matchMedia-driven choice with
   * the SuperAdmin opt-out applied. Used by tests to render either
   * branch without stubbing matchMedia.
   */
  readonly forceLayout?: "desktop" | "mobile";
}

const SUPERADMIN_ROLE = "platform_superadmin";

export function AppLayout({ forceLayout }: AppLayoutProps = {}) {
  const auth = useAuth();
  const { t } = useTranslation();
  const authed = auth.status === "authenticated";

  const isSuperadmin =
    auth.me?.roles.some((r) => r === SUPERADMIN_ROLE) ?? false;
  const isDesktopAuto = useIsDesktop(true);
  const effectiveLayout: "desktop" | "mobile" =
    forceLayout ?? (isSuperadmin || isDesktopAuto ? "desktop" : "mobile");

  if (effectiveLayout === "desktop") {
    return (
      <div style={shellStyle} data-shell-layout="desktop">
        <aside style={sidebarStyle} aria-label={t("shell.nav.aria")}>
          <div style={brandStyle}>
            <span style={brandMarkStyle} aria-hidden="true" />
            <span>{t("shell.brand")}</span>
          </div>
          {authed ? <AuthenticatedSidebarNav /> : <UnauthenticatedSidebarNav />}
          {authed && auth.me !== null ? (
            <UserBlock
              userId={auth.me.user.id}
              roles={auth.me.roles}
              onLogout={() => void auth.logout()}
            />
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

  // Mobile shell -- single column with a sticky header + drawer nav.
  return (
    <div style={mobileShellStyle} data-shell-layout="mobile">
      <MobileHeader authed={authed} />
      <main style={mobileMainStyle}>
        <AuthGate>
          <Outlet />
        </AuthGate>
      </main>
      {authed ? <ReasonPromptModal /> : null}
    </div>
  );
}

function MobileHeader({ authed }: { readonly authed: boolean }) {
  const { t } = useTranslation();
  const [navOpen, setNavOpen] = useState(false);

  // Close the drawer on Escape for keyboard users.
  useEffect(() => {
    if (!navOpen) return undefined;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") {
        setNavOpen(false);
      }
    };
    if (typeof window !== "undefined") {
      window.addEventListener("keydown", onKey);
    }
    return () => {
      if (typeof window !== "undefined") {
        window.removeEventListener("keydown", onKey);
      }
    };
  }, [navOpen]);

  return (
    <>
      <header style={mobileHeaderStyle} data-testid="shell-mobile-header">
        <button
          type="button"
          onClick={() => setNavOpen(true)}
          style={hamburgerBtnStyle}
          aria-label={t("shell.nav.open")}
          aria-haspopup="dialog"
          aria-expanded={navOpen}
          data-testid="shell-mobile-hamburger"
        >
          <span aria-hidden="true" style={hamburgerIconStyle}>
            <span style={hamburgerLineStyle} />
            <span style={hamburgerLineStyle} />
            <span style={hamburgerLineStyle} />
          </span>
        </button>
        <span style={mobileBrandStyle}>
          <span style={brandMarkStyle} aria-hidden="true" />
          <span>{t("shell.brand")}</span>
        </span>
        <span style={mobileHeaderSpacerStyle} />
        {authed ? (
          <span style={mobileHeaderActionsStyle}>
            <ActiveReasonBadge />
            <ScopeSelector forceLayout="mobile" />
          </span>
        ) : null}
      </header>
      {navOpen ? (
        <MobileNavDrawer open={navOpen} onClose={() => setNavOpen(false)} />
      ) : null}
    </>
  );
}

function MobileNavDrawer({
  open,
  onClose,
}: {
  readonly open: boolean;
  readonly onClose: () => void;
}) {
  const { t } = useTranslation();
  const auth = useAuth();
  const authed = auth.status === "authenticated";

  if (!open) return null;
  return (
    <section
      role="dialog"
      aria-modal="true"
      aria-label={t("shell.nav.aria")}
      style={mobileDrawerStyle}
      data-testid="shell-mobile-nav-drawer"
    >
      <header style={mobileDrawerHeaderStyle}>
        <button
          type="button"
          onClick={onClose}
          style={drawerBackBtnStyle}
          aria-label={t("shell.nav.close")}
          data-testid="shell-mobile-nav-close"
        >
          ← <span>{t("shell.nav.close")}</span>
        </button>
        <div style={drawerBrandStyle}>
          <span style={brandMarkStyle} aria-hidden="true" />
          <span>{t("shell.brand")}</span>
        </div>
      </header>
      <div style={drawerBodyStyle}>
        {authed ? (
          <AuthenticatedSidebarNav onNavigate={onClose} />
        ) : (
          <UnauthenticatedSidebarNav onNavigate={onClose} />
        )}
        <div style={drawerLocaleRowStyle}>
          <LocaleSwitcher />
        </div>
        {authed && auth.me !== null ? (
          <UserBlock
            userId={auth.me.user.id}
            roles={auth.me.roles}
            onLogout={() => {
              onClose();
              void auth.logout();
            }}
          />
        ) : null}
        {config.isDevelopment ? <DevDiagnosticsPanel /> : null}
        <div style={sidebarFooterStyle}>
          v0.1.0 {config.isDevelopment ? `· ${t("shell.devSuffix")}` : null}
        </div>
      </div>
    </section>
  );
}

function UserBlock({
  userId,
  roles,
  onLogout,
}: {
  readonly userId: string;
  readonly roles: readonly string[];
  readonly onLogout: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div style={userBlockStyle}>
      <div style={userIdStyle}>{userId}</div>
      <div style={userRolesStyle}>
        {roles.join(", ") || t("shell.noRoles")}
      </div>
      <button type="button" onClick={onLogout} style={logoutBtnStyle}>
        {t("shell.signOut")}
      </button>
    </div>
  );
}

function AuthenticatedSidebarNav({
  onNavigate,
}: {
  readonly onNavigate?: () => void;
} = {}) {
  const { permissions } = useAuth();
  const { activeScopeKind } = useScope();
  const { t } = useTranslation();
  const entries = visibleNavEntries(NAV_ENTRIES, permissions, activeScopeKind);
  return (
    <nav style={navStyle} data-testid="primary-nav">
      {entries.map((entry) => (
        <NavItem key={entry.id} entry={entry} onNavigate={onNavigate} />
      ))}
      {entries.length === 0 ? (
        <p style={emptyNavStyle} data-testid="nav-empty" role="status">
          {t("shell.nav.empty")}
        </p>
      ) : null}
    </nav>
  );
}

function UnauthenticatedSidebarNav({
  onNavigate,
}: {
  readonly onNavigate?: () => void;
} = {}) {
  const { t } = useTranslation();
  return (
    <nav style={navStyle} data-testid="primary-nav">
      <Link
        to="/login"
        style={navLinkStyle}
        activeProps={{ style: navLinkActiveStyle }}
        data-testid="nav-login"
        onClick={onNavigate}
      >
        {t("shell.signIn")}
      </Link>
    </nav>
  );
}

function NavItem({
  entry,
  onNavigate,
}: {
  readonly entry: NavEntry;
  readonly onNavigate?: () => void;
}) {
  const { t, locale } = useTranslation();
  // Resolve label via i18n: explicit labelKey > nav.<id> convention > raw label.
  const key = entry.labelKey ?? `nav.${entry.id}`;
  const translated = t(key);
  // If t() falls back to the raw key (missing translation), use entry.label.
  const label = translated === key ? entry.label : translated;
  return (
    <Link
      to={entry.to as "/"}
      style={navLinkStyle}
      activeProps={{ style: navLinkActiveStyle }}
      data-testid={`nav-${entry.id}`}
      data-nav-id={entry.id}
      data-nav-locale={locale}
      title={entry.purpose}
      onClick={onNavigate}
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
      <ScopeSelector forceLayout="desktop" />
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

// --- styles ---------------------------------------------------------------

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
  padding: "8px 10px",
  borderRadius: 4,
  fontSize: 12,
  cursor: "pointer",
  minHeight: 36,
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
  padding: "10px 12px",
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

// --- mobile-only styles ---------------------------------------------------

const mobileShellStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  minHeight: "100vh",
  background: "#f8fafc",
  color: "#0f172a",
  fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif",
  // iOS Safari browser chrome can occlude the bottom edge; pad accordingly.
  paddingBottom: "env(safe-area-inset-bottom)",
};

const mobileHeaderStyle: CSSProperties = {
  position: "sticky",
  top: 0,
  zIndex: 30,
  display: "flex",
  alignItems: "center",
  gap: 8,
  padding: "10px 12px",
  // Respect iOS notch.
  paddingTop: "calc(10px + env(safe-area-inset-top))",
  background: "#0f172a",
  color: "#f8fafc",
  borderBottom: "1px solid #1e293b",
  minHeight: 56,
};

const mobileMainStyle: CSSProperties = {
  flex: 1,
  padding: 16,
  overflowY: "auto",
};

const mobileBrandStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 8,
  fontWeight: 600,
  fontSize: 14,
};

const mobileHeaderSpacerStyle: CSSProperties = {
  flex: 1,
};

const mobileHeaderActionsStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 8,
};

const hamburgerBtnStyle: CSSProperties = {
  background: "transparent",
  color: "#f8fafc",
  border: "1px solid #1e293b",
  borderRadius: 6,
  padding: 8,
  // 44 x 44 minimum hit target (platform convention).
  minWidth: 44,
  minHeight: 44,
  cursor: "pointer",
  display: "inline-flex",
  alignItems: "center",
  justifyContent: "center",
};

const hamburgerIconStyle: CSSProperties = {
  display: "inline-flex",
  flexDirection: "column",
  gap: 4,
  width: 18,
};

const hamburgerLineStyle: CSSProperties = {
  display: "block",
  height: 2,
  background: "#f8fafc",
  borderRadius: 1,
  width: "100%",
};

const mobileDrawerStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  zIndex: 50,
  background: "#0f172a",
  color: "#e2e8f0",
  display: "flex",
  flexDirection: "column",
  paddingBottom: "env(safe-area-inset-bottom)",
};

const mobileDrawerHeaderStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: "12px 16px",
  paddingTop: "calc(12px + env(safe-area-inset-top))",
  borderBottom: "1px solid #1e293b",
};

const drawerBackBtnStyle: CSSProperties = {
  background: "transparent",
  border: 0,
  padding: "4px 0",
  color: "#bae6fd",
  fontSize: 14,
  fontWeight: 500,
  cursor: "pointer",
  alignSelf: "flex-start",
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
  minHeight: 36,
};

const drawerBrandStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 8,
  fontWeight: 600,
  fontSize: 14,
  color: "#e2e8f0",
};

const drawerBodyStyle: CSSProperties = {
  flex: 1,
  overflowY: "auto",
  display: "flex",
  flexDirection: "column",
  gap: 12,
  padding: "12px 12px 24px",
};

const drawerLocaleRowStyle: CSSProperties = {
  padding: "8px 10px",
  background: "#1e293b",
  borderRadius: 4,
  display: "flex",
  alignItems: "center",
  gap: 8,
};

