import { useEffect, useState } from "react";
import { Link, useRouter } from "@tanstack/react-router";
import { useAuth } from "@/lib/auth";
import { linkLoginErrorKey } from "@/lib/errors";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { forgetLinkCode, linkCode, linkNext } from "@/lib/linkCode";
import { LanguageSelect } from "@/screens/LanguageSelect";

/**
 * /login/link — the page the bot's "Open in browser" link opens (spec 37
 * section 4.3). main.tsx has already taken the code out of the address; this
 * page exchanges it for a session and opens the organizations, or, when the
 * link names one (spec 61, section 4.5), the shop screen the bot meant.
 */
export function LinkLoginScreen() {
  const { t } = useI18n();
  const { status, loginWithLink } = useAuth();
  const router = useRouter();
  const [code] = useState(() => linkCode());
  // Read once, before the exchange forgets it. Only a safe shop path is ever kept (see linkCode.ts).
  const [next] = useState(() => linkNext());
  const [to, setTo] = useState<string | null>(null);
  const [error, setError] = useState<MessageKey | null>(code === null ? "linkLogin.error.missing" : null);

  useEffect(() => {
    if (code === null) return;
    let alive = true;
    loginWithLink(code)
      .then(() => {
        forgetLinkCode();
        if (alive) setTo(next ?? "/orgs");
      })
      .catch((err: unknown) => {
        forgetLinkCode();
        if (alive) setError(linkLoginErrorKey(err));
      });
    return () => {
      alive = false;
    };
  }, [code, next, loginWithLink]);

  // Leave only once the app knows it is signed in, so a screen of the organization never sees "signed out" and
  // sends the person back to the sign-in page. replace: this page is not a place to come back to.
  useEffect(() => {
    if (to !== null && status === "signedIn") router.history.replace(to);
  }, [to, status, router]);

  return (
    <main id="main" className="auth-page">
      <div className="auth-card" data-testid="link-login">
        <p className="brand">{t("app.name")}</p>
        <h1>{t("linkLogin.title")}</h1>
        {error === null ? (
          <p role="status">{t("linkLogin.working")}</p>
        ) : (
          <>
            <p className="notice notice-bad" role="alert" data-testid="link-login-error">
              {t(error)}
            </p>
            <p>
              <Link to="/login">{t("linkLogin.toLogin")}</Link>
            </p>
          </>
        )}
        <LanguageSelect />
      </div>
    </main>
  );
}
