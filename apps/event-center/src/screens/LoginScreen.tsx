import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useAuth } from "@/lib/auth";
import { loginErrorKey } from "@/lib/errors";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { LanguageSelect } from "@/screens/LanguageSelect";

export function LoginScreen() {
  const { t } = useI18n();
  const { login, logout, status } = useAuth();
  const navigate = useNavigate();
  const search = useSearch({ from: "/login" });
  const [email, setEmail] = useState(search.email ?? "");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);
  const [signedHere, setSignedHere] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const target = search.redirect ?? "/orgs";
  // A letter's link names the account to sign in with. A session already
  // open in this browser is then never entered silently: the person chooses
  // (2026-11-07, an invitation to Vino&Co opened the inviter's own
  // organization because the inviter was signed in in the same browser).
  const askFirst = search.email !== undefined && !signedHere;

  useEffect(() => {
    if (status === "signedIn" && !askFirst) void navigate({ to: target });
  }, [status, navigate, target, askFirst]);

  if (status === "signedIn" && askFirst && search.email !== undefined) {
    const leave = async () => {
      setLeaving(true);
      try {
        await logout();
      } finally {
        setLeaving(false);
      }
    };
    return (
      <main id="main" className="auth-page">
        <div className="auth-card" data-testid="login-other-session">
          <p className="brand">{t("app.name")}</p>
          <h1>{t("login.title")}</h1>
          <p className="notice" role="status">
            {t("login.otherSession", { email: search.email })}
          </p>
          <p>
            <button
              type="button"
              className="btn btn-primary"
              disabled={leaving}
              onClick={() => void leave()}
              data-testid="login-other-signout"
            >
              {t("login.otherSession.signOut")}
            </button>
          </p>
          <p>
            <button type="button" className="btn btn-link" disabled={leaving} onClick={() => void navigate({ to: target })}>
              {t("login.otherSession.continue")}
            </button>
          </p>
          <LanguageSelect />
        </div>
      </main>
    );
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (email.trim().length === 0 || password.length === 0) {
      setError("login.error.empty");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      setSignedHere(true);
      await login(email.trim(), password);
    } catch (err) {
      setSignedHere(false);
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
