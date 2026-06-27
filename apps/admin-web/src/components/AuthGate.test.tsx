import { renderToString } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AuthStatus } from "@/lib/auth/AuthContext";
import { AuthGate } from "@/components/AuthGate";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  pathname: "/login",
  status: "unauthenticated",
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mocks.navigate,
  useRouterState: ({ select }: { select: (state: { location: { pathname: string } }) => unknown }) =>
    select({ location: { pathname: mocks.pathname } }),
}));

vi.mock("@/lib/auth/useAuth", () => ({
  useAuth: () => ({
    status: mocks.status as AuthStatus,
    me: null,
    meError: null,
    permissions: new Set<string>(),
    availableScopes: [],
    hasPermission: () => false,
    hasScope: () => false,
    login: async () => undefined,
    logout: async () => undefined,
    refreshMe: async () => undefined,
  }),
}));

describe("AuthGate", () => {
  beforeEach(() => {
    mocks.navigate.mockClear();
    mocks.pathname = "/login";
    mocks.status = "unauthenticated";
  });

  it("keeps login children mounted while authentication is in flight", () => {
    mocks.pathname = "/login";
    mocks.status = "authenticating";

    const html = renderToString(
      <AuthGate>
        <div>login form sentinel</div>
      </AuthGate>,
    );

    expect(html).toContain("login form sentinel");
    expect(html).not.toContain("Signing in");
  });

  it("still blocks protected children while authentication is in flight", () => {
    mocks.pathname = "/networks";
    mocks.status = "authenticating";

    const html = renderToString(
      <AuthGate>
        <div>protected content sentinel</div>
      </AuthGate>,
    );

    expect(html).toContain("Signing in");
    expect(html).not.toContain("protected content sentinel");
  });
});
