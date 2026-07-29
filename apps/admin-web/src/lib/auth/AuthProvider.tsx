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
import {
  subscribeToPermissionRefresh,
  subscribeToWindowFocus,
} from "@/lib/auth/permissionRefresh";

interface AuthProviderProps {
  readonly children: ReactNode;
}

export function AuthProvider({ children }: AuthProviderProps) {
  const [status, setStatus] = useState<AuthStatus>("initializing");
  const [me, setMe] = useState<MeResponse | null>(null);
  const [meError, setMeError] = useState<AuthContextValue["meError"]>(null);
  const [permissionsRefreshNotice, setPermissionsRefreshNotice] = useState(false);
  const bootstrappedRef = useRef(false);

  const loadMe = useCallback(async (): Promise<boolean> => {
    try {
      const response = await fetchMe();
      setMe(response);
      setMeError(null);
      setStatus("authenticated");
      return true;
    } catch (err) {
      setMe(null);
      if (err instanceof ApiError && err.status === 401) {
        setMeError({ code: err.code, message: err.message });
        setStatus("unauthenticated");
        return false;
      }
      const code = err instanceof ApiError ? err.code : "me.unknown";
      const message = err instanceof Error ? err.message : "Failed to load /v1/me";
      setMeError({ code, message });
      setStatus("me_failed");
      return false;
    }
  }, []);

  // Roles can be changed by another operator while this tab is open. A focus
  // refresh keeps permission-gated navigation current without a new login.
  useEffect(() => {
    if (status !== "authenticated") return undefined;
    return subscribeToWindowFocus(() => {
      void loadMe();
    });
  }, [loadMe, status]);

  // The shared API client emits this only for a 403 from a mutation. Refresh
  // through the normal auth path so every consumer receives the new /v1/me.
  useEffect(() => {
    if (status !== "authenticated") return undefined;
    return subscribeToPermissionRefresh(() => {
      void loadMe().then((refreshed) => {
        if (refreshed) {
          setPermissionsRefreshNotice(true);
        }
      });
    });
  }, [loadMe, status]);

  useEffect(() => {
    if (!permissionsRefreshNotice) return undefined;
    const timeout = window.setTimeout(() => setPermissionsRefreshNotice(false), 6_000);
    return () => window.clearTimeout(timeout);
  }, [permissionsRefreshNotice]);

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

  const refreshMe = useCallback(async (): Promise<void> => {
    await loadMe();
  }, [loadMe]);

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
      refreshMe,
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
      refreshMe,
    ],
  );

  return (
    <AuthContext.Provider value={value}>
      {children}
      {permissionsRefreshNotice ? (
        <div
          role="status"
          aria-live="polite"
          style={{
            position: "fixed",
            right: 16,
            bottom: 16,
            zIndex: 100,
            maxWidth: "min(360px, calc(100vw - 32px))",
            padding: "10px 14px",
            borderRadius: 6,
            background: "#065f46",
            color: "#ffffff",
            boxShadow: "0 8px 24px rgb(15 23 42 / 24%)",
          }}
        >
          Permissions changed — refreshed.
        </div>
      ) : null}
    </AuthContext.Provider>
  );
}
