/**
 * Sign-in state and the caller's access.
 *
 * - AuthProvider restores a session from the refresh token on load.
 * - useMe() reads GET /v1/me (the organizations of the person).
 * - useOrgAccess(orgId) reads GET /v1/organizations/{org_id}/me (A1): the
 *   permissions INSIDE that organization. Screens decide by `can()`, never
 *   by the role and never by /v1/me (that is the union of all
 *   organizations).
 */
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { fetchMe, fetchOrgMe } from "@/lib/api/endpoints";
import { ApiError, login as apiLogin, loginWithLink as apiLoginWithLink, logout as apiLogout, session } from "@/lib/api/http";
import { forgetOrderSearches } from "@/lib/orderSearchMemory";
import { stepUp } from "@/lib/stepUp";
import type { MeOrganizationMembership, OrgMeResponse } from "@/lib/api/types";

export type AuthStatus = "loading" | "signedOut" | "signedIn";

interface AuthValue {
  status: AuthStatus;
  login: (email: string, password: string) => Promise<void>;
  /** The one-time code of a bot link (spec 37 §4.3). */
  loginWithLink: (code: string) => Promise<void>;
  logout: () => Promise<void>;
}

const Ctx = createContext<AuthValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<AuthStatus>(() => (session.hasSession() ? "loading" : "signedOut"));

  useEffect(() => {
    let alive = true;
    if (status === "loading") {
      // Leadership first: a duplicated tab must ask the leading tab of its
      // session instead of rotating the shared refresh token itself.
      session
        .start()
        .then(() => session.ensureFresh())
        .then(() => alive && setStatus("signedIn"))
        .catch((err: unknown) => {
          if (!alive) return;
          // A network error keeps the session: the screens show the error
          // and retry. Only a refusal (the session is gone) signs out.
          const offline = err instanceof ApiError && err.status === 0 && session.hasSession();
          setStatus(offline ? "signedIn" : "signedOut");
        });
    }
    const unsubscribe = session.subscribe(() => {
      if (!session.hasSession()) {
        setStatus("signedOut");
        queryClient.clear();
        forgetOrderSearches();
        stepUp.clear();
      }
    });
    return () => {
      alive = false;
      unsubscribe();
    };
    // Runs once: later changes arrive through subscribe().
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    await apiLogin(email, password);
    setStatus("signedIn");
  }, []);

  const loginWithLink = useCallback(async (code: string) => {
    await apiLoginWithLink(code);
    setStatus("signedIn");
  }, []);

  const logout = useCallback(async () => {
    await apiLogout();
    queryClient.clear();
    forgetOrderSearches();
    stepUp.clear();
    setStatus("signedOut");
  }, [queryClient]);

  const value = useMemo(() => ({ status, login, loginWithLink, logout }), [status, login, loginWithLink, logout]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth(): AuthValue {
  const v = useContext(Ctx);
  if (v === null) throw new Error("useAuth outside AuthProvider");
  return v;
}

export function useMe() {
  const { status } = useAuth();
  return useQuery({ queryKey: ["me"], queryFn: fetchMe, enabled: status === "signedIn", staleTime: 60_000 });
}

/** The organizations the person can work in: platform roles and archived organizations are filtered out. */
export function organizationsOf(memberships: readonly MeOrganizationMembership[]): MeOrganizationMembership[] {
  const byOrg = new Map<string, MeOrganizationMembership>();
  const rank = (role: string) => ["org_admin", "organizer", "agent"].indexOf(role);
  for (const m of memberships) {
    if (m.role === "platform_superadmin" || m.role === "platform_operator") continue;
    if (m.archived === true) continue;
    const prev = byOrg.get(m.org_id);
    const better = prev === undefined || (rank(m.role) >= 0 && (rank(prev.role) < 0 || rank(m.role) < rank(prev.role)));
    if (better) byOrg.set(m.org_id, m);
  }
  return [...byOrg.values()].sort((a, b) => a.org_name.localeCompare(b.org_name));
}

/** Organizations a platform superadmin archived: shown as archived, never offered as a place to work. */
export function archivedOrganizationsOf(memberships: readonly MeOrganizationMembership[]): MeOrganizationMembership[] {
  const byOrg = new Map<string, MeOrganizationMembership>();
  for (const m of memberships) {
    if (m.archived === true && !byOrg.has(m.org_id)) byOrg.set(m.org_id, m);
  }
  return [...byOrg.values()].sort((a, b) => a.org_name.localeCompare(b.org_name));
}

export interface OrgAccess {
  data: OrgMeResponse | undefined;
  isLoading: boolean;
  error: unknown;
  can: (permission: string) => boolean;
}

export function useOrgAccess(orgId: string): OrgAccess {
  const { status } = useAuth();
  const q = useQuery({
    queryKey: ["orgMe", orgId],
    queryFn: () => fetchOrgMe(orgId),
    enabled: status === "signedIn" && orgId.length > 0,
    staleTime: 60_000,
  });
  const perms = useMemo(() => new Set(q.data?.permissions ?? []), [q.data]);
  const can = useCallback((p: string) => perms.has(p), [perms]);
  return { data: q.data, isLoading: q.isLoading, error: q.error, can };
}

const LAST_ORG_KEY = "arena.events.last_org";

export function rememberOrg(orgId: string): void {
  try {
    localStorage.setItem(LAST_ORG_KEY, orgId);
  } catch {
    // A convenience only.
  }
}

export function lastOrg(): string | null {
  try {
    return localStorage.getItem(LAST_ORG_KEY);
  } catch {
    return null;
  }
}
