import { renderToString } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { CONSOLE_STAFF_ONLY_CODE } from "@/lib/api/channel";

vi.mock("@tanstack/react-router", () => ({
  createRoute: () => ({}),
  Link: ({ children }: { children: unknown }) => <a href="/x">{children as string}</a>,
  useNavigate: () => vi.fn(),
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) =>
    select({ location: { pathname: "/login" } }),
}));
vi.mock("./__root", () => ({ Route: {} }));
vi.mock("@/lib/auth/useAuth", () => ({
  useAuth: () => ({ status: "unauthenticated", me: null, login: async () => undefined }),
}));

const { __test } = await import("./login");

describe("login page for organizers", () => {
  it("hints organizers at the event center", () => {
    const html = renderToString(<__test.LoginRoute />);
    expect(html).toContain('data-testid="login-organizer-hint"');
    expect(html).toContain("https://events.arenasoldout.com");
    expect(html).toContain("for Arena staff");
  });

  it("shows the staff-only message, not a raw code, for a refused sign-in", () => {
    const refused = new ApiError(403, { code: CONSOLE_STAFF_ONLY_CODE, message: "server text" });
    expect(__test.classifyLoginError(refused)).toEqual({ kind: "staff_only" });
  });

  it("keeps other failures as text", () => {
    const bad = new ApiError(401, { code: "auth.invalid_credentials", message: "bad password" });
    expect(__test.classifyLoginError(bad)).toEqual({
      kind: "text",
      text: "auth.invalid_credentials: bad password",
    });
    expect(__test.classifyLoginError(new Error("boom"))).toEqual({ kind: "text", text: "boom" });
    expect(__test.classifyLoginError("x")).toEqual({ kind: "text", text: "Sign-in failed" });
  });
});
