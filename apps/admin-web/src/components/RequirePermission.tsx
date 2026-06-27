import type { ReactNode } from "react";
import { useAuth } from "@/lib/auth/useAuth";
import { useScope } from "@/lib/auth/ScopeContext";
import {
  permissionRuleSatisfied,
  scopeRuleSatisfied,
  type NavEntry,
} from "@/lib/auth/navConfig";
import { Forbidden } from "@/components/Forbidden";

/**
 * Route-level permission gate (SAUI-03).
 *
 * Renders children if the caller satisfies the entry's permission rule
 * AND scope filter; otherwise renders the explicit 403 surface. This is
 * the ONLY component that should gate a route. Sidebar filtering hides
 * entries from view but cannot be relied upon to block direct URL
 * navigation -- this guard is the backstop.
 */
export interface RequirePermissionProps {
  readonly entry: NavEntry;
  readonly children: ReactNode;
}

export function RequirePermission({ entry, children }: RequirePermissionProps) {
  const { permissions } = useAuth();
  const { activeScopeKind, activeScope } = useScope();

  const okPerm = permissionRuleSatisfied(entry.permission, permissions);
  const okScope = scopeRuleSatisfied(entry, activeScopeKind);

  if (okPerm && okScope) {
    return <>{children}</>;
  }
  return (
    <Forbidden
      route={entry.to}
      purpose={entry.purpose}
      required={entry.permission}
      heldPermissions={permissions}
      activeScopeLabel={activeScope?.label ?? null}
    />
  );
}
