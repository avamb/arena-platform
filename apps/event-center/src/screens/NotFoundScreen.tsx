import { Link } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";

export function NotFoundScreen() {
  const { t } = useI18n();
  return (
    <main id="main" className="auth-page">
      <div className="auth-card">
        <h1>{t("notfound.title")}</h1>
        <p>{t("common.error.notFound")}</p>
        <p>
          <Link to="/orgs">{t("notfound.back")}</Link>
        </p>
      </div>
    </main>
  );
}
