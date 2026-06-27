import { createRoute } from "@tanstack/react-router";
import { useForm } from "react-hook-form";
import { z } from "zod";
import type { CSSProperties } from "react";
import { Route as RootRoute } from "./__root";

/**
 * Sign-in scaffold.
 *
 * Renders a typed React Hook Form + Zod validated form. This is the
 * scaffold-only version: it validates input shape but does NOT call the
 * backend yet. Real /v1/auth/login wiring lands in SAUI-02.
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
  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginValues>({
    defaultValues: { email: "", password: "" },
  });

  const onSubmit = handleSubmit((raw) => {
    // Validate via Zod here (scaffold path); SAUI-02 will replace this with
    // a real call to POST /v1/auth/login and route on success.
    const parsed = loginSchema.safeParse(raw);
    if (!parsed.success) {
      // eslint-disable-next-line no-console -- operator-visible diagnostic
      console.warn("[admin-web] login validation failed", parsed.error.flatten());
      return;
    }
    // No backend call yet. Surface a status message so the form is testable.
    // eslint-disable-next-line no-console -- operator-visible diagnostic
    console.info("[admin-web] login submitted (scaffold; not yet wired)");
  });

  return (
    <section aria-labelledby="login-heading" style={pageStyle}>
      <h1 id="login-heading" style={headingStyle}>
        Sign in
      </h1>
      <p style={subStyle}>
        Authentication wiring lands in SAUI-02. This scaffold validates input
        but does not call the backend.
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
