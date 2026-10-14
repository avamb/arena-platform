import { useState, type FormEvent } from "react";
import { Link } from "@tanstack/react-router";
import { confirmPasswordReset } from "@/lib/api/http";
import { setPasswordErrorKey } from "@/lib/errors";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { parsePasswordLink } from "@/lib/search";
import { LanguageSelect } from "@/screens/LanguageSelect";

/** Server limits of POST /v1/auth/password-reset/confirm, in bytes. */
const MIN_PASSWORD_BYTES = 8;
const MAX_PASSWORD_BYTES = 72;

/** Checks the two fields the way the server will, before any request. */
export function checkNewPassword(password: string, repeat: string): MessageKey | null {
  if (password.length === 0 || repeat.length === 0) return "password.error.empty";
  const bytes = new TextEncoder().encode(password).length;
  if (bytes < MIN_PASSWORD_BYTES) return "password.error.tooShort";
  if (bytes > MAX_PASSWORD_BYTES) return "password.error.tooLong";
  if (password !== repeat) return "password.error.mismatch";
  return null;
}

/**
 * The page a password letter opens: /reset-password (forgot password) or
 * /accept-invite (an account an operator created, or an invitation to an
 * organization). Both redeem the letter's one-time token; only the words
 * differ. The page needs no sign-in.
 */
export function SetPasswordScreen({ mode }: { mode: "reset" | "invite" }) {
  const { t } = useI18n();
  const [link] = useState(() => parsePasswordLink(window.location.search));
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);

  if (link.token === undefined) {
    return (
      <main id="main" className="auth-page">
        <div className="auth-card" data-testid="password-missing">
          <p className="brand">{t("app.name")}</p>
          <h1>{t("password.missing.title")}</h1>
          <p>{t("password.missing.text")}</p>
          <p>
            <Link to="/forgot-password">{t("password.newLink")}</Link>
          </p>
          <p>
            <Link to="/login">{t("password.signIn")}</Link>
          </p>
        </div>
      </main>
    );
  }
  const token = link.token;

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const problem = checkNewPassword(password, repeat);
    if (problem !== null) {
      setError(problem);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await confirmPasswordReset(token, password);
      setDone(true);
      setPassword("");
      setRepeat("");
    } catch (err) {
      setError(setPasswordErrorKey(err));
    } finally {
      setBusy(false);
    }
  };

  const linkProblem =
    error === "password.error.linkUsed" ||
    error === "password.error.linkExpired" ||
    error === "password.error.linkInvalid";
  const intro =
    mode === "reset"
      ? t("password.reset.intro")
      : link.email !== undefined
        ? t("password.invite.introEmail", { email: link.email })
        : t("password.invite.intro");

  return (
    <main id="main" className="auth-page">
      <div className="auth-card" data-testid={mode === "reset" ? "reset-password" : "accept-invite"}>
        <p className="brand">{t("app.name")}</p>
        <h1>{t(mode === "reset" ? "password.reset.title" : "password.invite.title")}</h1>
        {done ? (
          <>
            <p className="notice notice-ok" role="status" data-testid="password-done">
              {t("password.done")}
            </p>
            <p>
              <Link to="/login" className="btn btn-primary">
                {t("password.signIn")}
              </Link>
            </p>
          </>
        ) : (
          <>
            <p className="muted">{intro}</p>
            <form onSubmit={onSubmit} noValidate>
              {link.email !== undefined && (
                // Visible and read-only: password managers save the new
                // password under this e-mail and skip a hidden field.
                <>
                  <label htmlFor="password-email">{t("login.email")}</label>
                  <input
                    id="password-email"
                    type="email"
                    name="email"
                    autoComplete="username"
                    value={link.email}
                    readOnly
                    data-testid="password-email"
                  />
                </>
              )}
              <label htmlFor="password-new">{t("password.new")}</label>
              <input
                id="password-new"
                name="new-password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                aria-invalid={error !== null && !linkProblem}
                aria-describedby={error !== null ? "password-hint password-error" : "password-hint"}
                required
              />
              <p id="password-hint" className="muted">
                {t("password.hint")}
              </p>
              <label htmlFor="password-repeat">{t("password.repeat")}</label>
              <input
                id="password-repeat"
                name="repeat-password"
                type="password"
                autoComplete="new-password"
                value={repeat}
                onChange={(e) => setRepeat(e.target.value)}
                aria-invalid={error === "password.error.mismatch"}
                aria-describedby={error !== null ? "password-error" : undefined}
                required
              />
              {error !== null && (
                <p id="password-error" className="notice notice-bad" role="alert" data-testid="password-error">
                  {t(error)}
                </p>
              )}
              {linkProblem && (
                <p>
                  <Link to="/forgot-password">{t("password.newLink")}</Link>
                </p>
              )}
              <button type="submit" className="btn btn-primary" disabled={busy} data-testid="password-submit">
                {busy ? t("password.working") : t("password.submit")}
              </button>
            </form>
          </>
        )}
        <p>
          <Link to="/login">{t("forgot.back")}</Link>
        </p>
        <LanguageSelect />
      </div>
    </main>
  );
}
