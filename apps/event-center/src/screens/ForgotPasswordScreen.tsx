import { useState, type FormEvent } from "react";
import { Link } from "@tanstack/react-router";
import { requestPasswordReset } from "@/lib/api/http";
import { errorKey } from "@/lib/errors";
import { useI18n, type MessageKey } from "@/lib/i18n";

export function ForgotPasswordScreen() {
  const { t } = useI18n();
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (email.trim().length === 0) {
      setError("login.error.empty");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await requestPasswordReset(email.trim());
      setSent(true);
    } catch (err) {
      setError(errorKey(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main id="main" className="auth-page">
      <div className="auth-card">
        <p className="brand">{t("app.name")}</p>
        <h1>{t("forgot.title")}</h1>
        {sent ? (
          <p className="notice notice-ok" role="status" data-testid="forgot-sent">
            {t("forgot.sent")}
          </p>
        ) : (
          <>
            <p className="muted">{t("forgot.intro")}</p>
            <form onSubmit={onSubmit} noValidate>
              <label htmlFor="forgot-email">{t("login.email")}</label>
              <input
                id="forgot-email"
                type="email"
                autoComplete="username"
                inputMode="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                aria-describedby={error !== null ? "forgot-error" : undefined}
                required
              />
              {error !== null && (
                <p id="forgot-error" className="notice notice-bad" role="alert">
                  {t(error)}
                </p>
              )}
              <button type="submit" className="btn btn-primary" disabled={busy}>
                {t("forgot.submit")}
              </button>
            </form>
          </>
        )}
        <p>
          <Link to="/login">{t("forgot.back")}</Link>
        </p>
      </div>
    </main>
  );
}
