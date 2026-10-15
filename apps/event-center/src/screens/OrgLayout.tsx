import { createContext, useContext, useEffect } from "react";
import { Link, Outlet, useParams } from "@tanstack/react-router";
import { organizationsOf, rememberOrg, useAuth, useMe, useOrgAccess, type OrgAccess } from "@/lib/auth";
import { ApiError } from "@/lib/api/http";
import { useI18n } from "@/lib/i18n";
import { ErrorBox, ErrorScope, Loading } from "@/components/ui";
import { RequireSignIn } from "@/screens/RequireSignIn";
import { LanguageSelect } from "@/screens/LanguageSelect";
import { roleKey } from "@/screens/OrgsScreen";

export interface OrgContextValue {
  orgId: string;
  orgName: string;
  access: OrgAccess;
}

const OrgCtx = createContext<OrgContextValue | null>(null);

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
  const orgCount = me.data !== undefined ? organizationsOf(me.data.organization_memberships).length : 0;

  useEffect(() => {
    if (access.data !== undefined) rememberOrg(orgId);
  }, [access.data, orgId]);

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
        <header className="topbar">
          <div className="topbar-org">
            <span className="brand">{t("app.name")}</span>
            <span className="org-name" data-testid="org-name">
              {orgName}
            </span>
            <span className="muted">{t(roleKey(access.data.role))}</span>
          </div>
          <nav aria-label={t("nav.main")} className="topnav">
            <Link to="/o/$orgId/events" params={{ orgId }} activeProps={{ "aria-current": "page", className: "active" }}>
              {t("nav.events")}
            </Link>
            {access.can("order.read") && (
              <Link to="/o/$orgId/orders" params={{ orgId }} activeProps={{ "aria-current": "page", className: "active" }}>
                {t("nav.orders")}
              </Link>
            )}
            {access.can("complimentary.read") && (
              <Link
                to="/o/$orgId/invitations"
                params={{ orgId }}
                activeProps={{ "aria-current": "page", className: "active" }}
              >
                {t("nav.invitations")}
            {access.can("promoter.read") && (
              <Link to="/o/$orgId/promoters" params={{ orgId }} activeProps={{ "aria-current": "page", className: "active" }}>
                {t("nav.promoters")}
              </Link>
            )}
            {access.can("promo.read") && (
              <Link to="/o/$orgId/promo" params={{ orgId }} search={{}} activeProps={{ "aria-current": "page", className: "active" }} data-testid="nav-promo">
                {t("nav.promo")}
              </Link>
            )}
            <Link
              to="/o/$orgId/notifications"
              params={{ orgId }}
              activeProps={{ "aria-current": "page", className: "active" }}
            >
              {t("nav.notifications")}
            </Link>
            {access.can("membership.read") && (
              <Link to="/o/$orgId/team" params={{ orgId }} activeProps={{ "aria-current": "page", className: "active" }}>
                {t("nav.team")}
              </Link>
            )}
            {orgCount > 1 && <Link to="/orgs">{t("nav.switchOrg")}</Link>}
          </nav>
          <div className="topbar-tools">
            <LanguageSelect />
            <button type="button" className="btn btn-link" onClick={() => void logout()} data-testid="logout">
              {t("nav.logout")}
            </button>
          </div>
        </header>
        <main id="main" className="page">
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
