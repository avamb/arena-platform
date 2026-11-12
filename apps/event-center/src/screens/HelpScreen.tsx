/**
 * "Help": what each section of the menu is for, one line each, and how to
 * get more help. The list is the same one the Menu sheet shows this person
 * (`navGroups`), so a section the role cannot use is not described. There is
 * no support address in the catalog, so none is shown: the event center bot
 * in Telegram has the same sections, and the "Connect Telegram" block is here.
 */
import { ConnectTelegram } from "@/components/ConnectTelegram";
import { organizationsOf, useMe } from "@/lib/auth";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { navGroups, type NavGroupId } from "@/lib/navMenu";
import { shopModeFor } from "@/lib/shopModule";
import { useOrg } from "@/screens/OrgLayout";

const LINES: Partial<Record<NavGroupId, MessageKey>> = {
  new: "helpPage.new",
  events: "helpPage.events",
  orders: "helpPage.orders",
  promotion: "helpPage.promotion",
  entrance: "helpPage.entrance",
  shop: "helpPage.shop",
  settings: "helpPage.settings",
};

export function HelpScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const me = useMe();
  const orgCount = me.data !== undefined ? organizationsOf(me.data.organization_memberships).length : 0;
  const groups = navGroups(access, shopModeFor(me.data?.organization_memberships, orgId), orgCount);
  return (
    <section aria-labelledby="help-title" data-testid="help">
      <h1 id="help-title">{t("helpPage.title")}</h1>
      <p>{t("helpPage.intro")}</p>
      <dl className="help-sections" data-testid="help-sections">
        {groups.map((g) => {
          const line = LINES[g.id];
          if (line === undefined) return null;
          return (
            <div key={g.id} data-testid={`help-section-${g.id}`}>
              <dt>{t(g.titleKey)}</dt>
              <dd>{t(line)}</dd>
            </div>
          );
        })}
      </dl>
      <section aria-labelledby="help-support-title" data-testid="help-support">
        <h2 id="help-support-title">{t("helpPage.support.title")}</h2>
        <p>{t("helpPage.support.text")}</p>
        <ConnectTelegram orgId={orgId} variant="card" />
      </section>
    </section>
  );
}
