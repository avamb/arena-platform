/**
 * React context shape for authenticated session state.
 *
 * The context object itself is split from the provider so that fast
 * refresh works correctly (a file exporting both a Context and a
 * component would invalidate HMR boundaries).
 */
import { createContext } from "react";
import type { MeResponse } from "@/lib/api/types";

export type AuthStatus =
  | "initializing"
  | "unauthenticated"
  | "authenticating"
  | "authenticated"
  | "me_failed";

export interface AuthContextValue {
  status: AuthStatus;
  me: MeResponse | null;
  /** Network/parse error from the most recent /v1/me call, if any. */
  meError: { code: string; message: string } | null;
  /** Convenience derived fields */
  permissions: ReadonlySet<string>;
  availableScopes: readonly string[];
  hasPermission: (perm: string) => boolean;
  hasScope: (scope: string) => boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refreshMe: () => Promise<void>;
}

export const AuthContext = createContext<AuthContextValue | null>(null);
