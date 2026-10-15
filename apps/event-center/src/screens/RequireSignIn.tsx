import { useEffect, useRef, type ReactNode } from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useAuth } from "@/lib/auth";
import { Loading } from "@/components/ui";

/**
 * Shows the children to a signed-in person. Anyone else goes to the
 * sign-in screen and comes back to the same address afterwards. When a
 * session ends while the person works, the sign-in screen says so.
 *
 * The redirect runs once from an effect: a <Navigate> whose search object
 * is rebuilt on every render navigated again and again.
 */
export function RequireSignIn({ children }: { children: ReactNode }) {
  const { status } = useAuth();
  const navigate = useNavigate();
  const href = useRouterState({ select: (s) => s.location.href });
  const wasSignedIn = useRef(false);
  const redirected = useRef(false);
  if (status === "signedIn") {
    wasSignedIn.current = true;
    redirected.current = false;
  }

  useEffect(() => {
    if (status !== "signedOut" || redirected.current) return;
    redirected.current = true;
    void navigate({
      to: "/login",
      search: { redirect: href, expired: wasSignedIn.current ? true : undefined },
      replace: true,
    });
  }, [status, href, navigate]);

  if (status !== "signedIn") {
    return (
      <main id="main" className="auth-page">
        <Loading />
      </main>
    );
  }
  return <>{children}</>;
}
