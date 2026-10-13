import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useAuth } from "@/lib/auth";
import { loginErrorKey } from "@/lib/errors";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { LanguageSelect } from "@/screens/LanguageSelect";

export function LoginScreen() {
  const { t } = useI18n();
  const { login, status } = useAuth();
  const navigate = useNavigate();
  const search = useSearch({ from: "/login" });
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);
  const target = search.redirect ?? "/orgs";

  useEffect(() => {
    if (status === "signedIn") void navigate({ to: target });
  }, [status, navigate, target]);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (email.trim().length === 0 || password.length === 0) {
      setError("login.error.empty");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await login(email.trim(), password);
    } catch (err) {
      setError(loginErrorKey(err));
      setBusy(false);
    }
  };

  return (
    <main id="main" className="auth-page">
      <div className="auth-card">
        <p className="brand">{t("app.name")}</p>
        <h1>{t("login.title")}</h1>
        {search.expired === true && (
          <p className="notice" role="status">
            {t("login.expired")}
          </p>
        )}
        <p className="muted">{t("login.intro")}</p>
        <form onSubmit={onSubmit} noValidate>
          <label htmlFor="login-email">{t("login.email")}</label>
          <input
            id="login-email"
            name="email"
            type="email"
            autoComplete="username"
            inputMode="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            aria-invalid={error !== null}
            aria-describedby={error !== null ? "login-error" : undefined}
            required
          />
          <label htmlFor="login-password">{t("login.password")}</label>
          <input
            id="login-password"
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-invalid={error !== null}
            aria-describedby={error !== null ? "login-error" : undefined}
            required
          />
          {error !== null && (
            <p id="login-error" className="notice notice-bad" role="alert">
              {t(error)}
            </p>
          )}
          <button type="submit" className="btn btn-primary" disabled={busy} data-testid="login-submit">
            {busy ? t("login.working") : t("login.submit")}
          </button>
        </form>
        <p>
          <Link to="/forgot-password">{t("login.forgot")}</Link>
        </p>
        <LanguageSelect />
      </div>
    </main>
  );
}
