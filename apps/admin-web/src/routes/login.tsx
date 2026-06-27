import { createRoute, useNavigate } from "@tanstack/react-router";
import { useForm } from "react-hook-form";
import { useState, type CSSProperties } from "react";
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
  const [submitError, setSubmitError] = useState<string | null>(null);
  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginValues>({
    defaultValues: { email: "", password: "" },
  });

  const onSubmit = handleSubmit(async (raw) => {
    setSubmitError(null);
    const parsed = loginSchema.safeParse(raw);
    if (!parsed.success) {
      return;
    }
    try {
      await auth.login(parsed.data.email, parsed.data.password);
      await navigate({ to: "/", replace: true });
    } catch (err) {
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
    <section aria-labelledby="login-heading" style={pageStyle}>
      <h1 id="login-heading" style={headingStyle}>
        Sign in
      </h1>
      <p style={subStyle}>
        Sign in with your operator credentials. Sessions are scoped to this
        browser tab.
      </p>
      <form onSubmit={onSubmit} style={formStyle} noValidate>
        <label style={labelStyle}>
          <span>Email</span>
          <input
            type="email"
            autoComplete="username"
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
            autoComplete="current-password"
            aria-invalid={errors.password !== undefined}
            {...register("password")}
            style={inputStyle}
          />
          {errors.password ? <span style={errorStyle}>{errors.password.message}</span> : null}
        </label>
        {submitError !== null ? (
          <div role="alert" style={alertStyle}>
            {submitError}
          </div>
        ) : null}
        <button type="submit" disabled={isSubmitting} style={submitStyle}>
          {isSubmitting ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </section>
  );
}

const pageStyle: CSSProperties = {
  maxWidth: 360,
  margin: "0 auto",
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const headingStyle: CSSProperties = { margin: 0, fontSize: 22, fontWeight: 600 };
const subStyle: CSSProperties = { margin: 0, fontSize: 13, color: "#475569" };
const formStyle: CSSProperties = { display: "flex", flexDirection: "column", gap: 12 };
const labelStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  fontSize: 12,
  fontWeight: 500,
  color: "#0f172a",
};
const inputStyle: CSSProperties = {
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  padding: "8px 10px",
  fontSize: 13,
  background: "#ffffff",
  color: "#0f172a",
};
const errorStyle: CSSProperties = { fontSize: 11, color: "#b91c1c", fontWeight: 400 };
const alertStyle: CSSProperties = {
  fontSize: 12,
  background: "#fee2e2",
  border: "1px solid #fecaca",
  color: "#7f1d1d",
  padding: "8px 10px",
  borderRadius: 4,
};
const submitStyle: CSSProperties = {
  marginTop: 4,
  background: "#0f172a",
  color: "#f8fafc",
  border: 0,
  borderRadius: 4,
  padding: "9px 14px",
  fontSize: 13,
  fontWeight: 600,
  cursor: "pointer",
};
