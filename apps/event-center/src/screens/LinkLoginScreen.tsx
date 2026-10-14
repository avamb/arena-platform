import { useEffect, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useAuth } from "@/lib/auth";
import { linkLoginErrorKey } from "@/lib/errors";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { forgetLinkCode, linkCode } from "@/lib/linkCode";
import { LanguageSelect } from "@/screens/LanguageSelect";

/**
 * /login/link — the page the bot's "Open in browser" link opens (spec 37
 * section 4.3). main.tsx has already taken the code out of the address; this
 * page exchanges it for a session and opens the organizations.
 */
export function LinkLoginScreen() {
  const { t } = useI18n();
  const { loginWithLink } = useAuth();
  const navigate = useNavigate();
  const [code] = useState(() => linkCode());
  const [error, setError] = useState<MessageKey | null>(code === null ? "linkLogin.error.missing" : null);

  useEffect(() => {
    if (code === null) return;
    let alive = true;
    loginWithLink(code)
      .then(() => {
        forgetLinkCode();
        if (alive) void navigate({ to: "/orgs", replace: true });
      })
      .catch((err: unknown) => {
        forgetLinkCode();
        if (alive) setError(linkLoginErrorKey(err));
      });
    return () => {
      alive = false;
    };
  }, [code, loginWithLink, navigate]);

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
