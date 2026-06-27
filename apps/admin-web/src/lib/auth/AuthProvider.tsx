/**
 * <AuthProvider /> wires the typed API client + token store into the
 * React tree and exposes the AuthContext consumed by useAuth().
 *
 * Responsibilities (SAUI-02):
 *   1. On mount, attempt to bootstrap a session if a refresh token is
 *      present in sessionStorage. Status transitions:
 *        initializing -> authenticated      (refresh + /v1/me both ok)
 *        initializing -> unauthenticated    (no refresh token, or refresh failed)
 *        initializing -> me_failed          (refresh ok but /v1/me errored)
 *   2. Expose login(email, password) that performs POST /v1/auth/login
 *      then immediately loads /v1/me.
 *   3. Expose logout() that revokes the refresh token server-side then
 *      clears local state.
 *   4. Memoise permissions / availableScopes so consumers can do cheap
 *      Set/array lookups without re-deriving on every render.
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ApiError, fetchMe, login as apiLogin, logout as apiLogout, refresh } from "@/lib/api/client";
import { getRefreshToken } from "@/lib/api/tokenStore";
import type { MeResponse } from "@/lib/api/types";
import { AuthContext, type AuthContextValue, type AuthStatus } from "@/lib/auth/AuthContext";

interface AuthProviderProps {
  readonly children: ReactNode;
}

export function AuthProvider({ children }: AuthProviderProps) {
  const [status, setStatus] = useState<AuthStatus>("initializing");
  const [me, setMe] = useState<MeResponse | null>(null);
  const [meError, setMeError] = useState<AuthContextValue["meError"]>(null);
  const bootstrappedRef = useRef(false);

  const loadMe = useCallback(async (): Promise<void> => {
    try {
      const response = await fetchMe();
      setMe(response);
      setMeError(null);
      setStatus("authenticated");
    } catch (err) {
      setMe(null);
      if (err instanceof ApiError && err.status === 401) {
        setMeError({ code: err.code, message: err.message });
        setStatus("unauthenticated");
        return;
      }
      const code = err instanceof ApiError ? err.code : "me.unknown";
      const message = err instanceof Error ? err.message : "Failed to load /v1/me";
      setMeError({ code, message });
      setStatus("me_failed");
    }
  }, []);

  // One-shot bootstrap: try to resume an existing session via the
  // refresh token. Subsequent component remounts MUST NOT re-run this
  // (would cause a spurious refresh storm).
  useEffect(() => {
    if (bootstrappedRef.current) {
      return;
    }
    bootstrappedRef.current = true;
    let cancelled = false;
    const run = async (): Promise<void> => {
      if (getRefreshToken() === null) {
        if (!cancelled) {
          setStatus("unauthenticated");
        }
        return;
      }
      try {
        await refresh();
      } catch {
        if (!cancelled) {
          setStatus("unauthenticated");
        }
        return;
      }
      if (cancelled) {
        return;
      }
      await loadMe();
    };
    void run();
    return () => {
      cancelled = true;
    };
  }, [loadMe]);

  const login = useCallback(
    async (email: string, password: string): Promise<void> => {
      setStatus("authenticating");
      try {
        await apiLogin({ email, password });
      } catch (err) {
        setStatus("unauthenticated");
        throw err;
      }
      await loadMe();
    },
    [loadMe],
  );

  const logout = useCallback(async (): Promise<void> => {
    await apiLogout();
    setMe(null);
    setMeError(null);
    setStatus("unauthenticated");
  }, []);

  const permissions = useMemo<ReadonlySet<string>>(() => {
    if (me === null) {
      return new Set<string>();
    }
    return new Set(me.permissions);
  }, [me]);

  const availableScopes = useMemo<readonly string[]>(() => {
    if (me === null) {
      return [];
    }
    return me.available_scopes;
  }, [me]);

  const hasPermission = useCallback(
    (perm: string): boolean => permissions.has(perm),
    [permissions],
  );
  const hasScope = useCallback(
    (scope: string): boolean => availableScopes.includes(scope),
    [availableScopes],
  );

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      me,
      meError,
      permissions,
      availableScopes,
      hasPermission,
      hasScope,
      login,
      logout,
      refreshMe: loadMe,
    }),
    [
      status,
      me,
      meError,
      permissions,
      availableScopes,
      hasPermission,
      hasScope,
      login,
      logout,
      loadMe,
    ],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
