/**
 * <AuthGate /> blocks the admin shell from rendering authenticated
 * surfaces until /v1/me has either succeeded or definitively failed.
 *
 * Behaviour matrix (SAUI-02 steps 4, 8):
 *   - initializing      -> <LoadingScreen />
 *   - unauthenticated   -> redirect to /login (also: if currently on
 *                          /login, just render children so the form is
 *                          reachable).
 *   - authenticating    -> show LoadingScreen overlay (children rendered
 *                          under it would briefly flash the login form).
 *   - authenticated     -> render children
 *   - me_failed         -> render a hard error recovery screen ("clear
 *                          recovery") with Retry + Sign out actions.
 *                          Admin content is NOT rendered behind this
 *                          screen — the operator must explicitly retry
 *                          or sign out so a malformed /v1/me cannot
 *                          silently strip permissions.
 */
import { useEffect, type ReactNode, type CSSProperties } from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { LoadingScreen } from "@/components/LoadingScreen";
import { useAuth } from "@/lib/auth/useAuth";

interface AuthGateProps {
  readonly children: ReactNode;
}

export function AuthGate({ children }: AuthGateProps) {
  const auth = useAuth();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const onLogin = pathname === "/login";

  useEffect(() => {
    if (auth.status === "unauthenticated" && !onLogin) {
      void navigate({ to: "/login", replace: true });
    }
  }, [auth.status, onLogin, navigate]);

  // Already authenticated and the operator landed on /login -> push them
  // to the workspace.
  useEffect(() => {
    if (auth.status === "authenticated" && onLogin) {
      void navigate({ to: "/", replace: true });
    }
  }, [auth.status, onLogin, navigate]);

  if (auth.status === "initializing") {
    return <LoadingScreen label="Restoring session…" />;
  }

  if (auth.status === "authenticating") {
    return <LoadingScreen label="Signing in…" />;
  }

  if (auth.status === "me_failed") {
    return (
      <div role="alert" style={recoveryStyle}>
        <h1 style={{ margin: 0, fontSize: 20 }}>Unable to load your profile</h1>
        <p style={{ margin: "8px 0", fontSize: 13 }}>
          The admin shell could not load <code>/v1/me</code>. Admin pages are
          blocked until your profile loads cleanly.
        </p>
        {auth.meError ? (
          <pre style={errorBoxStyle}>
            {auth.meError.code}: {auth.meError.message}
          </pre>
        ) : null}
        <div style={{ display: "flex", gap: 8 }}>
          <button type="button" onClick={() => void auth.refreshMe()} style={primaryBtn}>
            Retry
          </button>
          <button type="button" onClick={() => void auth.logout()} style={secondaryBtn}>
            Sign out
          </button>
        </div>
      </div>
    );
  }

  if (auth.status === "unauthenticated" && !onLogin) {
    // Effect above will navigate momentarily; show a loader instead of
    // flashing protected content.
    return <LoadingScreen label="Redirecting to sign-in…" />;
  }

  return <>{children}</>;
}

const recoveryStyle: CSSProperties = {
  fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif",
  padding: 24,
  maxWidth: 640,
  margin: "10vh auto",
  border: "1px solid #b91c1c",
  borderRadius: 6,
  background: "#fff1f2",
  color: "#7f1d1d",
};

const errorBoxStyle: CSSProperties = {
  whiteSpace: "pre-wrap",
  background: "#fee2e2",
  padding: 12,
  borderRadius: 4,
  fontSize: 12,
  margin: "12px 0",
};

const primaryBtn: CSSProperties = {
  padding: "6px 14px",
  border: "1px solid #7f1d1d",
  background: "#7f1d1d",
  color: "#fff",
  borderRadius: 4,
  cursor: "pointer",
  fontSize: 13,
};

const secondaryBtn: CSSProperties = {
  padding: "6px 14px",
  border: "1px solid #7f1d1d",
  background: "#fff",
  color: "#7f1d1d",
  borderRadius: 4,
  cursor: "pointer",
  fontSize: 13,
};
