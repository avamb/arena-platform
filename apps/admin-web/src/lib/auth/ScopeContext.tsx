/**
 * <ScopeProvider /> exposes the user's parsed scopes and the currently
 * active scope. It re-derives whenever /v1/me changes, and persists the
 * selection in sessionStorage so a full reload preserves operator
 * intent.
 *
 * Sourced from useAuth(): permissions/scopes are the backend source of
 * truth. This provider only manages "which scope is currently active in
 * the UI" -- the backend re-checks every request regardless.
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useAuth } from "@/lib/auth/useAuth";
import {
  parseScopes,
  persistScope,
  readPersistedScope,
  resolveInitialScope,
  type Scope,
} from "@/lib/auth/scope";
import type { ScopeKind } from "@/lib/auth/navConfig";

export interface ScopeContextValue {
  readonly availableScopes: readonly Scope[];
  readonly activeScope: Scope | null;
  readonly activeScopeKind: ScopeKind | null;
  setActiveScope(raw: string): void;
}

const ScopeContext = createContext<ScopeContextValue | null>(null);

interface ScopeProviderProps {
  readonly children: ReactNode;
}

export function ScopeProvider({ children }: ScopeProviderProps) {
  const { me, availableScopes: rawScopes } = useAuth();

  const availableScopes = useMemo<readonly Scope[]>(() => {
    return parseScopes(rawScopes, {
      networks: me?.assigned_networks,
      memberships: me?.organization_memberships,
    });
  }, [rawScopes, me]);

  // Active scope: persisted choice if it still matches available;
  // otherwise the role-appropriate default.
  const [activeRaw, setActiveRaw] = useState<string | null>(() => {
    const persisted = readPersistedScope();
    const initial = resolveInitialScope(availableScopes, persisted);
    return initial?.raw ?? null;
  });

  // Re-resolve when the available scopes change (login, refreshMe,
  // logout). Avoids stuck-on-stale-scope after a role change.
  useEffect(() => {
    const persisted = readPersistedScope();
    const next = resolveInitialScope(availableScopes, persisted);
    setActiveRaw(next?.raw ?? null);
  }, [availableScopes]);

  const activeScope = useMemo<Scope | null>(() => {
    if (activeRaw === null) {
      return null;
    }
    return availableScopes.find((s) => s.raw === activeRaw) ?? null;
  }, [activeRaw, availableScopes]);

  const setActiveScope = useCallback(
    (raw: string) => {
      if (availableScopes.find((s) => s.raw === raw) === undefined) {
        // Defensive: refuse to set a scope the backend did not grant.
        // eslint-disable-next-line no-console -- operator-visible diagnostic
        console.warn("[admin-web] ignoring scope not in available_scopes", raw);
        return;
      }
      setActiveRaw(raw);
      persistScope(raw);
    },
    [availableScopes],
  );

  const value = useMemo<ScopeContextValue>(
    () => ({
      availableScopes,
      activeScope,
      activeScopeKind: activeScope?.kind ?? null,
      setActiveScope,
    }),
    [availableScopes, activeScope, setActiveScope],
  );

  return <ScopeContext.Provider value={value}>{children}</ScopeContext.Provider>;
}

export function useScope(): ScopeContextValue {
  const ctx = useContext(ScopeContext);
  if (ctx === null) {
    throw new Error("useScope must be used inside <ScopeProvider>");
  }
  return ctx;
}
