import { useI18n } from "@/lib/i18n";

export const SALES_BOT_URL = "https://t.me/ArenaSoldOutSalesBot";

export function NotificationsScreen() {
  const { t } = useI18n();
  return (
    <section aria-labelledby="notif-title">
      <h1 id="notif-title">{t("notif.title")}</h1>
      <p>{t("notif.text")}</p>
      <p>
        <a className="btn btn-primary" href={SALES_BOT_URL} target="_blank" rel="noopener noreferrer">
          {t("notif.open")}
        </a>
      </p>
    </section>
  );
}
