import { GuestListSubscription } from "@/components/GuestListSubscription";
import { useI18n } from "@/lib/i18n";
import { ScreenHelp } from "@/components/ScreenHelp";
import { useOrg } from "@/screens/OrgLayout";

export const SALES_BOT_URL = "https://t.me/ArenaSoldOutSalesBot";

export function NotificationsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  return (
    <section aria-labelledby="notif-title">
      <div className="screen-head">
        <h1 id="notif-title">{t("notif.title")}</h1>
        <ScreenHelp screenId="notifications" />
      </div>
      <p>{t("notif.text")}</p>
      <p>
        <a className="btn btn-primary" href={SALES_BOT_URL} target="_blank" rel="noopener noreferrer">
          {t("notif.open")}
        </a>
      </p>
      {/* The guest list comes from the event center bot or by e-mail (guest_list.go). */}
      {access.can("order.read") && <GuestListSubscription orgId={orgId} />}
    </section>
  );
}
