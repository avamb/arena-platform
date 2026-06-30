import { createRoute, Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useForm } from "react-hook-form";
import { useEffect, useState, type CSSProperties } from "react";
import { z } from "zod";
import { ApiError } from "@/lib/api/client";
import { useAuth } from "@/lib/auth/useAuth";
import { Route as RootRoute } from "./__root";

/**
 * Sign-in route — wired to POST /v1/auth/login via AuthProvider.login().
 *
 * SAUI-02 wiring:
 *   - Zod validates input shape client-side before hitting the network.
 *   - AuthProvider performs login + /v1/me, then sets status="authenticated".
 *   - On success we navigate to "/"; AuthGate also enforces this transition
 *     so even if the navigate() call is delayed, the form will not be the
 *     final view for an authenticated user.
 *   - ApiError surfaces server-side error codes (auth.invalid_credentials,
 *     etc.) with their human messages. Network failures surface as
 *     "network.failure".
 *
 * Mobile (Wave M-3, feature #296):
 *   - Inputs / submit button render at >= 44 CSS px height, preventing iOS
 *     auto-zoom (font-size 16) and meeting WCAG 2.5.5 touch-target guidance.
 *   - Form scales to 360 CSS px width without horizontal scroll
 *     (max-width: 100%, box-sizing: border-box, no fixed pixel widths above
 *     the viewport).
 *   - Native virtual-keyboard hints: inputMode="email" + autoComplete
 *     hints prompt the right keyboard / autofill on iOS and Android.
 *   - Error toasts render at the TOP of the form with position: sticky so
 *     they remain visible when the on-screen keyboard pushes the layout up.
 */
const loginSchema = z.object({
  email: z.string().trim().toLowerCase().email({ message: "Enter a valid email." }),
  password: z.string().min(1, { message: "Password is required." }),
});

type LoginValues = z.infer<typeof loginSchema>;

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/login",
  component: LoginRoute,
});

function LoginRoute() {
  const auth = useAuth();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [redirectPending, setRedirectPending] = useState(false);
  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginValues>({
    defaultValues: { email: "", password: "" },
  });

  useEffect(() => {
    if (!redirectPending) {
      return;
    }
    if (auth.status === "authenticated" && pathname === "/login") {
      setRedirectPending(false);
      void navigate({ to: "/", replace: true });
      return;
    }
    if (auth.status === "unauthenticated" || auth.status === "me_failed") {
      setRedirectPending(false);
    }
  }, [auth.status, navigate, pathname, redirectPending]);

  const onSubmit = handleSubmit(async (raw) => {
    setSubmitError(null);
    const parsed = loginSchema.safeParse(raw);
    if (!parsed.success) {
      return;
    }
    try {
      setRedirectPending(true);
      await auth.login(parsed.data.email, parsed.data.password);
    } catch (err) {
      setRedirectPending(false);
      if (err instanceof ApiError) {
        setSubmitError(`${err.code}: ${err.message}`);
      } else if (err instanceof Error) {
        setSubmitError(err.message);
      } else {
        setSubmitError("Sign-in failed");
      }
    }
  });

  return (
    <section aria-labelledby="login-heading" style={pageStyle} data-testid="login-page">
      <h1 id="login-heading" style={headingStyle}>
        Sign in
      </h1>
      <p style={subStyle}>
        Sign in with your operator credentials. Sessions are scoped to this
        browser tab.
      </p>
      {submitError !== null ? (
        <div role="alert" aria-live="assertive" style={alertStyle} data-testid="login-error">
          {submitError}
        </div>
      ) : null}
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
            aria-invalid={errors.email !== undefined}
            {...register("email")}
            style={inputStyle}
          />
          {errors.email ? <span style={errorStyle}>{errors.email.message}</span> : null}
        </label>
        <label style={labelStyle}>
          <span>Password</span>
          <input
            type="password"
            inputMode="text"
            autoComplete="current-password"
            aria-invalid={errors.password !== undefined}
            {...register("password")}
            style={inputStyle}
          />
          {errors.password ? <span style={errorStyle}>{errors.password.message}</span> : null}
        </label>
        <button type="submit" disabled={isSubmitting || redirectPending} style={submitStyle}>
          {isSubmitting || redirectPending ? "Signing in…" : "Sign in"}
        </button>
        <p style={hintStyle}>
          <Link to="/password-reset" style={linkStyle}>
            Forgot your password?
          </Link>
        </p>
      </form>
    </section>
  );
}

// Shared mobile-friendly form styles for /login, /password-reset, /accept-invite.
// width: 100% + maxWidth + box-sizing keeps the page within 360 CSS px without
// horizontal scroll; minHeight 44 keeps every interactive control at the WCAG
// 2.5.5 touch-target minimum; fontSize 16 prevents the iOS auto-zoom behavior
// that would otherwise force an annoying viewport jump on focus.
const pageStyle: CSSProperties = {
  width: "100%",
  maxWidth: 360,
  margin: "0 auto",
  padding: "16px",
  boxSizing: "border-box",
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

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
const inputStyle: CSSProperties = {
  border: "1px solid #cbd5e1",
  borderRadius: 6,
  padding: "10px 12px",
  minHeight: 44,
  fontSize: 16,
  background: "#ffffff",
  color: "#0f172a",
  width: "100%",
  boxSizing: "border-box",
};
const errorStyle: CSSProperties = { fontSize: 12, color: "#b91c1c", fontWeight: 400 };
const alertStyle: CSSProperties = {
  position: "sticky",
  top: 0,
  zIndex: 1,
  fontSize: 14,
  background: "#fee2e2",
  border: "1px solid #fecaca",
  color: "#7f1d1d",
  padding: "10px 12px",
  borderRadius: 6,
  width: "100%",
  boxSizing: "border-box",
};
const submitStyle: CSSProperties = {
  marginTop: 4,
  background: "#0f172a",
  color: "#f8fafc",
  border: 0,
  borderRadius: 6,
  padding: "10px 14px",
  minHeight: 44,
  fontSize: 16,
  fontWeight: 600,
  cursor: "pointer",
  width: "100%",
};
const hintStyle: CSSProperties = { margin: 0, fontSize: 13, color: "#475569", textAlign: "center" };
const linkStyle: CSSProperties = {
  color: "#0f172a",
  textDecoration: "underline",
  display: "inline-block",
  padding: "8px 4px",
  minHeight: 44,
  boxSizing: "border-box",
};

// Exported for tests (Wave M-3, feature #296) so the mobile-fitness
// assertions can verify the exact CSS values without re-rendering the route.
export const mobileFormStyles = {
  pageStyle,
  inputStyle,
  submitStyle,
  alertStyle,
  linkStyle,
} as const;
