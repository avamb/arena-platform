import { Outlet } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";

/** The page frame: skip link, then the screen. Headers live in OrgLayout. */
export function RootLayout() {
  const { t } = useI18n();
  return (
    <>
      <a className="skip-link" href="#main">
        {t("nav.skip")}
      </a>
      <Outlet />
    </>
  );
}
