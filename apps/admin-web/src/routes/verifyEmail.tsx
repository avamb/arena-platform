import { createRoute, Link, useSearch } from "@tanstack/react-router";
import { useEffect, useState, type CSSProperties } from "react";
import { config } from "@/lib/config";
import { Route as RootRoute } from "./__root";
import { mobileFormStyles } from "./login";

interface VerifyEmailSearch {
  readonly token?: string;
}

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/verify-email",
  component: VerifyEmailRoute,
  validateSearch: (raw: Record<string, unknown>): VerifyEmailSearch => {
    const token = raw.token;
    return { token: typeof token === "string" && token.length > 0 ? token : undefined };
  },
});

type Status = "missing" | "verifying" | "verified" | "failed";

function VerifyEmailRoute() {
  const search = useSearch({ from: Route.id }) as VerifyEmailSearch;
  const [status, setStatus] = useState<Status>(search.token === undefined ? "missing" : "verifying");

  useEffect(() => {
    if (search.token === undefined) return;
    const controller = new AbortController();
    void fetch(`${config.apiBaseUrl}/v1/auth/verify?token=${encodeURIComponent(search.token)}`, {
      method: "GET",
      headers: { Accept: "application/json" },
      credentials: "omit",
      signal: controller.signal,
    })
      .then((res) => setStatus(res.ok ? "verified" : "failed"))
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === "AbortError")) setStatus("failed");
      });
    return () => controller.abort();
  }, [search.token]);

  const message = status === "missing"
    ? "This verification link is missing its token. Please use the exact link from your email."
    : status === "verifying"
      ? "Verifying your email address…"
      : status === "verified"
        ? "Your email address is verified. You can now sign in."
        : "This verification link is invalid, expired, or has already been used.";

  return (
    <section aria-labelledby="verify-email-heading" style={mobileFormStyles.pageStyle} data-testid="verify-email">
      <h1 id="verify-email-heading" style={headingStyle}>Verify your email</h1>
      <p role={status === "failed" || status === "missing" ? "alert" : "status"} aria-live="polite" style={messageStyle}>
        {message}
      </p>
      {status !== "verifying" ? <Link to="/login" style={mobileFormStyles.linkStyle}>Go to sign in</Link> : null}
    </section>
  );
}

const headingStyle: CSSProperties = { margin: 0, fontSize: 22, fontWeight: 600 };
const messageStyle: CSSProperties = { margin: 0, fontSize: 14, color: "#475569" };
