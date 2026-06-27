import { useContext } from "react";
import { AuthContext, type AuthContextValue } from "@/lib/auth/AuthContext";

/**
 * Access the current AuthContext. Throws if rendered outside of
 * <AuthProvider /> — that always indicates a wiring mistake, not a
 * runtime branch, so a hard throw is the right behaviour.
 */
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (ctx === null) {
    throw new Error("useAuth must be used inside <AuthProvider>");
  }
  return ctx;
}
