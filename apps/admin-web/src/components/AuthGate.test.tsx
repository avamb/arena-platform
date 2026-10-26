import { renderToString } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AuthStatus } from "@/lib/auth/AuthContext";
import { AuthGate } from "@/components/AuthGate";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  pathname: "/login",
  status: "unauthenticated",
  me: null as unknown,
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mocks.navigate,
  useRouterState: ({ select }: { select: (state: { location: { pathname: string } }) => unknown }) =>
    select({ location: { pathname: mocks.pathname } }),
}));

vi.mock("@/lib/auth/useAuth", () => ({
  useAuth: () => ({
    status: mocks.status as AuthStatus,
    me: mocks.me,
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
    mocks.me = null;
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

  // AB-23 (#409): the reset email opens in a browser with no session. The
  // password-reset routes are public — an unauthenticated visitor must see
  // the form, not the "Redirecting to sign-in" loader.
  it.each(["/password-reset", "/reset-password"])(
    "renders %s for unauthenticated visitors instead of bouncing to /login",
    (path) => {
      mocks.pathname = path;
      mocks.status = "unauthenticated";

      const html = renderToString(
        <AuthGate>
          <div>reset form sentinel</div>
        </AuthGate>,
      );

      expect(html).toContain("reset form sentinel");
      expect(html).not.toContain("Redirecting to sign-in");
    },
  );

  it("still bounces unauthenticated visitors off protected routes", () => {
    mocks.pathname = "/organizations";
    mocks.status = "unauthenticated";

    const html = renderToString(
      <AuthGate>
        <div>protected content sentinel</div>
      </AuthGate>,
    );

    expect(html).toContain("Redirecting to sign-in");
    expect(html).not.toContain("protected content sentinel");
  });

  describe("platform staff only", () => {
    it("shows the staff-only screen, not the console, when /v1/me says platform_staff is false", () => {
      mocks.pathname = "/organizations";
      mocks.status = "authenticated";
      mocks.me = { platform_staff: false };

      const html = renderToString(
        <AuthGate>
          <div>protected content sentinel</div>
        </AuthGate>,
      );

      expect(html).not.toContain("protected content sentinel");
      expect(html).toContain("This console is for Arena staff");
      expect(html).toContain("Organizers work in the event center.");
      expect(html).toContain('href="https://events.arenasoldout.com"');
      expect(html).toContain("Sign out");
    });

    it("also blocks the login route for a signed-in organizer and does not bounce them to /", () => {
      mocks.pathname = "/login";
      mocks.status = "authenticated";
      mocks.me = { platform_staff: false };

      const html = renderToString(
        <AuthGate>
          <div>login form sentinel</div>
        </AuthGate>,
      );

      expect(html).not.toContain("login form sentinel");
      expect(html).toContain('data-testid="staff-only-screen"');
    });

    it.each([
      ["staff", { platform_staff: true }],
      ["an API that predates the field", {}],
    ])("renders the console for %s", (_name, me) => {
      mocks.pathname = "/organizations";
      mocks.status = "authenticated";
      mocks.me = me;

      const html = renderToString(
        <AuthGate>
          <div>protected content sentinel</div>
        </AuthGate>,
      );

      expect(html).toContain("protected content sentinel");
      expect(html).not.toContain("staff-only-screen");
    });
  });
});
