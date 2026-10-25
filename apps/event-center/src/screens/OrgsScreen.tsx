import { Link, Navigate } from "@tanstack/react-router";
import type { MeOrganizationMembership } from "@/lib/api/types";
import { archivedOrganizationsOf, lastOrg, organizationsOf, useAuth, useMe } from "@/lib/auth";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { Empty, ErrorBox, Loading } from "@/components/ui";
import { RequireSignIn } from "@/screens/RequireSignIn";
import { LanguageSelect } from "@/screens/LanguageSelect";

export function roleKey(role: string): MessageKey {
  if (role === "org_admin" || role === "organizer" || role === "agent") return `role.${role}`;
  return "role.none";
}

/** Archived organizations are named, never linked: there is nothing to open in them. */
export function ArchivedNote({ archived, only }: { archived: readonly MeOrganizationMembership[]; only?: boolean }) {
  const { t } = useI18n();
  if (archived.length === 0) return null;
  const list = archived.map((m) => m.org_name).join(", ");
  return (
    <p className="notice" role="status" data-testid="archived-orgs">
      {t(only === true ? "orgs.all_archived" : "orgs.archived", { list })}
    </p>
  );
}

function OrgsList() {
  const { t } = useI18n();
  const { logout } = useAuth();
  const me = useMe();
  if (me.isLoading) return <Loading />;
  if (me.error !== null || me.data === undefined) return <ErrorBox error={me.error} onRetry={() => void me.refetch()} />;

  if (me.data.roles.includes("platform_superadmin")) {
    return (
      <p className="notice" role="status" data-testid="superadmin-note">
        {t("orgs.superadmin")}
      </p>
    );
  }
  const orgs = organizationsOf(me.data.organization_memberships);
  const archived = archivedOrganizationsOf(me.data.organization_memberships);
  if (orgs.length === 1) {
    return <Navigate to="/o/$orgId/events" params={{ orgId: orgs[0].org_id }} replace />;
  }
  if (orgs.length === 0 && archived.length > 0) return <ArchivedNote archived={archived} only />;
  if (orgs.length === 0) return <Empty>{t("orgs.empty")}</Empty>;
  const last = lastOrg();
  const sorted = [...orgs].sort((a, b) => (a.org_id === last ? -1 : b.org_id === last ? 1 : 0));
  return (
    <>
      <ul className="cards" data-testid="org-list">
        {sorted.map((m) => (
          <li key={m.org_id} className="card">
            <Link to="/o/$orgId/events" params={{ orgId: m.org_id }} className="card-link">
              <span className="card-title">{m.org_name}</span>
              <span className="muted">{t("orgs.role", { role: t(roleKey(m.role)) })}</span>
            </Link>
          </li>
        ))}
      </ul>
      <ArchivedNote archived={archived} />
      <button type="button" className="btn btn-link" onClick={() => void logout()}>
        {t("nav.logout")}
      </button>
    </>
  );
}

export function OrgsScreen() {
  const { t } = useI18n();
  return (
    <RequireSignIn>
      <main id="main" className="auth-page">
        <div className="auth-card wide">
          <p className="brand">{t("app.name")}</p>
          <h1>{t("orgs.title")}</h1>
          <OrgsList />
          <LanguageSelect />
        </div>
      </main>
    </RequireSignIn>
  );
}
