import { createRoute, Link, useSearch } from "@tanstack/react-router";
import { useState, type CSSProperties, type FormEvent } from "react";
import { z } from "zod";
import { config } from "@/lib/config";
import { Route as RootRoute } from "./__root";
import { mobileFormStyles } from "./login";

/**
 * Password-reset route (Wave M-3, feature #296).
 *
 * Two phases on a single page:
 *   1. ?token absent  -> "Request reset" form posts to
 *      POST /v1/auth/password-reset/request and shows a 202 confirmation
 *      regardless of whether the email is known (anti-enumeration).
 *   2. ?token=<hex>   -> "Set new password" form posts to
 *      POST /v1/auth/password-reset/confirm with the token + new password.
 *
 * Mobile fitness (M-3):
 *   - Reuses the same minHeight 44 inputs / submit + 360 px-safe layout
 *     from /login (see mobileFormStyles).
 *   - inputMode="email" on the request form; type="password" with
 *     autoComplete="new-password" on the confirm form so password
 *     managers can suggest a strong password.
 *   - The error alert is position: sticky top: 0 so the on-screen keyboard
 *     cannot hide it.
 */

const requestSchema = z.object({
  email: z.string().trim().toLowerCase().email({ message: "Enter a valid email." }),
});

const confirmSchema = z.object({
  password: z
    .string()
    .min(8, { message: "Password must be at least 8 characters." })
    .max(72, { message: "Password must be 72 characters or fewer." }),
});

interface PasswordResetSearch {
  readonly token?: string;
}

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/password-reset",
  component: PasswordResetRoute,
  validateSearch: (raw: Record<string, unknown>): PasswordResetSearch => {
    const t = raw.token;
    return { token: typeof t === "string" && t.length > 0 ? t : undefined };
  },
});

function PasswordResetRoute() {
  const search = useSearch({ from: Route.id }) as PasswordResetSearch;
  return search.token === undefined ? (
    <RequestForm />
  ) : (
    <ConfirmForm token={search.token} />
  );
}

function RequestForm() {
  const [email, setEmail] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (ev: FormEvent<HTMLFormElement>) => {
    ev.preventDefault();
    setError(null);
    const parsed = requestSchema.safeParse({ email });
    if (!parsed.success) {
      setError(parsed.error.issues[0]?.message ?? "Invalid email.");
      return;
    }
    setSubmitting(true);
    try {
      const res = await fetch(`${config.apiBaseUrl}/v1/auth/password-reset/request`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ email: parsed.data.email }),
        credentials: "omit",
      });
      if (!res.ok && res.status !== 202) {
        setError(`reset.request_failed: HTTP ${res.status}`);
        return;
      }
      setDone(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Network request failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <section
      aria-labelledby="reset-request-heading"
      style={mobileFormStyles.pageStyle}
      data-testid="password-reset-request"
    >
      <h1 id="reset-request-heading" style={headingStyle}>
        Reset your password
      </h1>
      <p style={subStyle}>
        Enter the email tied to your operator account. If we recognise it,
        we&apos;ll send a one-time reset link.
      </p>
      {error !== null ? (
        <div role="alert" aria-live="assertive" style={mobileFormStyles.alertStyle} data-testid="reset-error">
          {error}
        </div>
      ) : null}
      {done ? (
        <div
          role="status"
          aria-live="polite"
          style={successStyle}
          data-testid="reset-request-done"
        >
          If that email is registered, a reset link is on its way. Check your
          inbox (and your server log in development).
        </div>
      ) : (
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <label style={labelStyle}>
            <span>Email</span>
            <input
              type="email"
              inputMode="email"
              autoComplete="username"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              style={mobileFormStyles.inputStyle}
              data-testid="reset-email"
            />
          </label>
          <button
            type="submit"
            disabled={submitting}
            style={mobileFormStyles.submitStyle}
            data-testid="reset-submit"
          >
            {submitting ? "Sending…" : "Send reset link"}
          </button>
        </form>
      )}
      <p style={hintStyle}>
        <Link to="/login" style={mobileFormStyles.linkStyle}>
          Back to sign in
        </Link>
      </p>
    </section>
  );
}

function ConfirmForm(props: { readonly token: string }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (ev: FormEvent<HTMLFormElement>) => {
    ev.preventDefault();
    setError(null);
    const parsed = confirmSchema.safeParse({ password });
    if (!parsed.success) {
      setError(parsed.error.issues[0]?.message ?? "Invalid password.");
      return;
    }
    setSubmitting(true);
    try {
      const res = await fetch(`${config.apiBaseUrl}/v1/auth/password-reset/confirm`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ token: props.token, new_password: parsed.data.password }),
        credentials: "omit",
      });
      if (!res.ok) {
        const body = (await safeJson(res)) as { error?: { code?: string; message?: string } } | null;
        const code = body?.error?.code ?? `http.${res.status}`;
        const msg = body?.error?.message ?? `HTTP ${res.status}`;
        setError(`${code}: ${msg}`);
        return;
      }
      setDone(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Network request failed");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <section
      aria-labelledby="reset-confirm-heading"
      style={mobileFormStyles.pageStyle}
      data-testid="password-reset-confirm"
    >
      <h1 id="reset-confirm-heading" style={headingStyle}>
        Choose a new password
      </h1>
      <p style={subStyle}>
        Pick a strong password (8–72 characters). The reset link can only be
        used once.
      </p>
      {error !== null ? (
        <div role="alert" aria-live="assertive" style={mobileFormStyles.alertStyle} data-testid="reset-error">
          {error}
        </div>
      ) : null}
      {done ? (
        <div role="status" aria-live="polite" style={successStyle} data-testid="reset-confirm-done">
          Your password has been updated. You can now{" "}
          <Link to="/login" style={mobileFormStyles.linkStyle}>
            sign in
          </Link>
          .
        </div>
      ) : (
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <label style={labelStyle}>
            <span>New password</span>
            <input
              type="password"
              inputMode="text"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              style={mobileFormStyles.inputStyle}
              data-testid="reset-password"
              minLength={8}
              maxLength={72}
            />
          </label>
          <button
            type="submit"
            disabled={submitting}
            style={mobileFormStyles.submitStyle}
            data-testid="reset-submit"
          >
            {submitting ? "Updating…" : "Update password"}
          </button>
        </form>
      )}
    </section>
  );
}

async function safeJson(res: Response): Promise<unknown> {
  try {
    return await res.json();
  } catch {
    return null;
  }
}

const headingStyle: CSSProperties = { margin: 0, fontSize: 22, fontWeight: 600 };
const subStyle: CSSProperties = { margin: 0, fontSize: 14, color: "#475569" };
const formStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  width: "100%",
};
const labelStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  fontSize: 13,
  fontWeight: 500,
  color: "#0f172a",
  width: "100%",
};
const hintStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#475569",
  textAlign: "center",
};
const successStyle: CSSProperties = {
  fontSize: 14,
  background: "#ecfdf5",
  border: "1px solid #bbf7d0",
  color: "#065f46",
  padding: "10px 12px",
  borderRadius: 6,
  width: "100%",
  boxSizing: "border-box",
};

// Exported pure helpers for unit tests (Wave M-3, feature #296).
export const __test = {
  requestSchema,
  confirmSchema,
};
