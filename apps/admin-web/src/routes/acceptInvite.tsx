import { createRoute, Link, useSearch } from "@tanstack/react-router";
import { useState, type CSSProperties, type FormEvent } from "react";
import { z } from "zod";
import { config } from "@/lib/config";
import { Route as RootRoute } from "./__root";
import { mobileFormStyles } from "./login";

/**
 * Accept-invite route (Wave M-3, feature #296).
 *
 * When an admin creates a new operator (see /v1/admin/users), the backend
 * issues a single-use reset token and surfaces a reset URL of the form
 * `${baseURL}/v1/auth/password-reset/confirm?token=...`. The admin-web
 * sends operators to /accept-invite?token=... which renders a dedicated
 * "welcome / pick your password" UX (different copy from the regular
 * /password-reset confirm screen) but reuses the same backend endpoint.
 *
 * Mobile fitness (M-3):
 *   - >= 44 CSS px inputs / submit; fontSize 16 prevents iOS auto-zoom.
 *   - 360 px-safe (width: 100%, maxWidth: 360, box-sizing: border-box).
 *   - autoComplete="new-password" so password managers can suggest +
 *     save credentials in a single tap.
 *   - Error alert is position: sticky top: 0 so the on-screen keyboard
 *     cannot hide it.
 */

const inviteSchema = z.object({
  password: z
    .string()
    .min(8, { message: "Password must be at least 8 characters." })
    .max(72, { message: "Password must be 72 characters or fewer." }),
});

interface AcceptInviteSearch {
  readonly token?: string;
  readonly email?: string;
}

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/accept-invite",
  component: AcceptInviteRoute,
  validateSearch: (raw: Record<string, unknown>): AcceptInviteSearch => {
    const t = raw.token;
    const e = raw.email;
    return {
      token: typeof t === "string" && t.length > 0 ? t : undefined,
      email: typeof e === "string" && e.length > 0 ? e : undefined,
    };
  },
});

function AcceptInviteRoute() {
  const search = useSearch({ from: Route.id }) as AcceptInviteSearch;
  if (search.token === undefined) {
    return (
      <section
        aria-labelledby="invite-missing-heading"
        style={mobileFormStyles.pageStyle}
        data-testid="invite-missing-token"
      >
        <h1 id="invite-missing-heading" style={headingStyle}>
          Invitation link incomplete
        </h1>
        <p style={subStyle}>
          The invitation link is missing its token. Please use the exact link
          from your invitation email, or ask an administrator to re-send it.
        </p>
        <p style={hintStyle}>
          <Link to="/login" style={mobileFormStyles.linkStyle}>
            Go to sign in
          </Link>
        </p>
      </section>
    );
  }
  return <InviteForm token={search.token} email={search.email} />;
}

function InviteForm(props: { readonly token: string; readonly email: string | undefined }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const onSubmit = async (ev: FormEvent<HTMLFormElement>) => {
    ev.preventDefault();
    setError(null);
    const parsed = inviteSchema.safeParse({ password });
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
      aria-labelledby="invite-heading"
      style={mobileFormStyles.pageStyle}
      data-testid="accept-invite"
    >
      <h1 id="invite-heading" style={headingStyle}>
        Welcome to Arena
      </h1>
      <p style={subStyle}>
        {props.email !== undefined
          ? `Set a password for ${props.email} to activate your operator account.`
          : "Set a password to activate your operator account."}
      </p>
      {error !== null ? (
        <div role="alert" aria-live="assertive" style={mobileFormStyles.alertStyle} data-testid="invite-error">
          {error}
        </div>
      ) : null}
      {done ? (
        <div role="status" aria-live="polite" style={successStyle} data-testid="invite-done">
          Your account is ready. You can now{" "}
          <Link to="/login" style={mobileFormStyles.linkStyle}>
            sign in
          </Link>
          .
        </div>
      ) : (
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          {props.email !== undefined ? (
            <label style={labelStyle}>
              <span>Email</span>
              <input
                type="email"
                inputMode="email"
                autoComplete="username"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                value={props.email}
                readOnly
                style={mobileFormStyles.inputStyle}
                data-testid="invite-email"
              />
            </label>
          ) : null}
          <label style={labelStyle}>
            <span>Choose a password</span>
            <input
              type="password"
              inputMode="text"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              style={mobileFormStyles.inputStyle}
              data-testid="invite-password"
              minLength={8}
              maxLength={72}
            />
          </label>
          <button
            type="submit"
            disabled={submitting}
            style={mobileFormStyles.submitStyle}
            data-testid="invite-submit"
          >
            {submitting ? "Activating…" : "Activate account"}
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
  inviteSchema,
};
