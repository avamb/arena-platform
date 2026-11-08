import { createContext, useContext, useEffect } from "react";
import { Link, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { organizationsOf, rememberOrg, useAuth, useMe, useOrgAccess, type OrgAccess } from "@/lib/auth";
import { ApiError } from "@/lib/api/http";
import { wizardGate } from "@/lib/features";
import { useI18n } from "@/lib/i18n";
import { useEmbedded } from "@/lib/seatManager/embed";
import { shopModeFor } from "@/lib/shopModule";
import { ErrorBox, ErrorScope, Loading } from "@/components/ui";
import { RequireSignIn } from "@/screens/RequireSignIn";
import { roleKey } from "@/screens/OrgsScreen";
import { A2hsNote, TabBar, showNewEventButton } from "@/components/AppNav";
import { applyBranding, resetBranding, type Branding } from "@/lib/theme";

export interface OrgContextValue {
  orgId: string;
  orgName: string;
  access: OrgAccess;
}

export const OrgCtx = createContext<OrgContextValue | null>(null);

/** The organization the current screen works in, and the caller's permissions there. */
export function useOrg(): OrgContextValue {
  const v = useContext(OrgCtx);
  if (v === null) throw new Error("useOrg outside OrgLayout");
  return v;
}

function OrgShell() {
  const { t } = useI18n();
  const { orgId } = useParams({ from: "/o/$orgId" });
  const { logout } = useAuth();
  const access = useOrgAccess(orgId);
  const me = useMe();
  const embed = useEmbedded();
  const showNewEvent = showNewEventButton(useRouterState({ select: (st) => st.location.pathname }));
  const orgCount = me.data !== undefined ? organizationsOf(me.data.organization_memberships).length : 0;
  // Spec 61: the shop is a paid module. No module (and no orders left from one) means no menu item at all.
  const shopMode = shopModeFor(me.data?.organization_memberships, orgId);

  useEffect(() => {
    if (access.data !== undefined) rememberOrg(orgId);
  }, [access.data, orgId]);

  // The organization's theme (spec 62 §12.1): its branding over the platform
  // palette while the person works in it, the platform palette again after.
  const branding: Branding | undefined = access.data?.branding;
  useEffect(() => {
    if (access.data === undefined) return;
    applyBranding(branding);
    return () => resetBranding();
  }, [access.data, branding]);

  if (access.isLoading) {
    return (
      <main id="main" className="page">
        <Loading />
      </main>
    );
  }
  if (access.data === undefined) {
    const forbidden = access.error instanceof ApiError && access.error.status === 403;
    return (
      <main id="main" className="page">
        {forbidden ? (
          <p className="notice notice-bad" role="alert" data-testid="not-member">
            {t("org.notMember")}
          </p>
        ) : (
          <ErrorBox error={access.error} onRetry={() => window.location.reload()} />
        )}
        {forbidden && (
          <p>
            <Link to="/orgs">{t("nav.switchOrg")}</Link>
          </p>
        )}
      </main>
    );
  }
  // A superadmin works in admin-web, even where they are also a member:
  // the web never sends X-Admin-Reason, so every route would answer 400
  // (spec 1.4, review finding L1).
  if (access.data.platform_superadmin) {
    return (
      <main id="main" className="page">
        <p className="notice" role="status" data-testid="superadmin-note">
          {t("orgs.superadmin")}
        </p>
      </main>
    );
  }

  const orgName = access.data.org_name;
  return (
    <OrgCtx.Provider value={{ orgId, orgName, access }}>
      <ErrorScope.Provider value={{ orgId, orgName }}>
        {!embed && (
          <header className="topbar">
            <div className="topbar-org">
              <span className="brand">{t("app.name")}</span>
              <span className="org-name" data-testid="org-name">
                {orgName}
              </span>
              <span className="muted">{t(roleKey(access.data.role))}</span>
            </div>
            {/* 6-1a: only in a build with VITE_FEATURE_WIZARD=1, and only for who can import a bundle. */}
            {wizardGate(access.can) === "ok" && showNewEvent && (
              <Link
                to="/o/$orgId/events/new"
                params={{ orgId }}
                className="btn btn-primary topbar-new"
                activeProps={{ "aria-current": "page" }}
                data-testid="nav-new-event"
              >
                {t("nav.newEvent")}
              </Link>
            )}
          </header>
        )}
        {!embed && (
          <TabBar orgId={orgId} access={access} shopMode={shopMode} orgCount={orgCount} onLogout={() => void logout()} />
        )}
        {!embed && <A2hsNote />}
        <main id="main" className={embed ? "page page-embed" : "page page-shell"}>
          <Outlet />
        </main>
      </ErrorScope.Provider>
    </OrgCtx.Provider>
  );
}

export function OrgLayout() {
  return (
    <RequireSignIn>
      <OrgShell />
    </RequireSignIn>
  );
}
