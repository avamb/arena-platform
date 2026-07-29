import { createRoute, useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect } from "react";
import { Route as RootRoute } from "./__root";

/**
 * Canonical email-link route. Keep the older /password-reset route for users
 * who open it from the sign-in page, while email links use this concise URL.
 */
interface ResetPasswordSearch {
  readonly token?: string;
}

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/reset-password",
  component: ResetPasswordRoute,
  validateSearch: (raw: Record<string, unknown>): ResetPasswordSearch => {
    const token = raw.token;
    return { token: typeof token === "string" && token.length > 0 ? token : undefined };
  },
});

function ResetPasswordRoute() {
  const search = useSearch({ from: Route.id }) as ResetPasswordSearch;
  const navigate = useNavigate();

  useEffect(() => {
    void navigate({ to: "/password-reset", search: { token: search.token }, replace: true });
  }, [navigate, search.token]);

  return null;
}
