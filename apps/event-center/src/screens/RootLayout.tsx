import { useEffect } from "react";
import { Outlet, useRouter } from "@tanstack/react-router";
import { followHistory } from "@/lib/tgBack";
import { bindBackButton } from "@/lib/tgEdit";
import { wasEnteredThroughTelegram } from "@/lib/tgEnter";
import { useI18n } from "@/lib/i18n";
import { useFocusOnRouteChange } from "@/lib/routeFocus";

/** The page frame: skip link, then the screen. Headers live in OrgLayout. */
export function RootLayout() {
  const { t } = useI18n();
  useFocusOnRouteChange();
  const router = useRouter();
  // Entered through /tg (the Telegram mini app): the Telegram back button walks the history.
  useEffect(
    () => followHistory(router.history, { active: wasEnteredThroughTelegram, bind: (h) => bindBackButton(h) }),
    [router],
  );
  return (
    <>
      <a className="skip-link" href="#main">
        {t("nav.skip")}
      </a>
      <Outlet />
    </>
  );
}
