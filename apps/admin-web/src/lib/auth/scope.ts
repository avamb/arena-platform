/**
 * Scope parsing and active-scope selection (SAUI-03).
 *
 * /v1/me.available_scopes is a deterministic, lexicographically-ordered
 * array of strings from the backend. Values currently produced:
 *
 *   "global"               -- admin / platform_superadmin only
 *   "platform"             -- platform_operator
 *   "network:<uuid>"       -- one per active network_users row
 *   "organization:<uuid>"  -- one per active membership
 *
 * This module turns those raw strings into typed Scope objects and
 * picks a sensible default scope per role preset. The backend remains
 * authoritative for authorization; the active scope only affects which
 * subset of UI surfaces is rendered.
 */
import type { ScopeKind } from "@/lib/auth/navConfig";
import type { MeAssignedNetwork, MeOrganizationMembership } from "@/lib/api/types";

export interface Scope {
  /** Raw scope string as returned by /v1/me.available_scopes */
  readonly raw: string;
  readonly kind: ScopeKind;
  /** UUID for "network:<uuid>" / "organization:<uuid>"; null otherwise. */
  readonly id: string | null;
  /** Operator-visible label (resolved against me.assigned_networks etc). */
  readonly label: string;
}

const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function shortenUuid(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id;
}

/**
 * Parse a raw scope string. Returns null on unrecognised input so we
 * never render garbage in the selector.
 */
export function parseScope(
  raw: string,
  meta: {
    readonly networks?: readonly MeAssignedNetwork[];
    readonly memberships?: readonly MeOrganizationMembership[];
  } = {},
): Scope | null {
  if (raw === "global") {
    return { raw, kind: "global", id: null, label: "Global (all tenants)" };
  }
  if (raw === "platform") {
    return { raw, kind: "platform", id: null, label: "Platform operations" };
  }
  if (raw.startsWith("network:")) {
    const id = raw.slice("network:".length);
    if (!UUID_RE.test(id)) {
      return null;
    }
    const network = meta.networks?.find((n) => n.id === id);
    const label =
      network !== undefined
        ? `Network: ${network.name}`
        : `Network: ${shortenUuid(id)}`;
    return { raw, kind: "network", id, label };
  }
  if (raw.startsWith("organization:")) {
    const id = raw.slice("organization:".length);
    if (!UUID_RE.test(id)) {
      return null;
    }
    const membership = meta.memberships?.find((m) => m.org_id === id);
    const label =
      membership !== undefined
        ? `Organization: ${shortenUuid(id)} (${membership.role})`
        : `Organization: ${shortenUuid(id)}`;
    return { raw, kind: "organization", id, label };
  }
  return null;
}

export function parseScopes(
  raws: readonly string[],
  meta: {
    readonly networks?: readonly MeAssignedNetwork[];
    readonly memberships?: readonly MeOrganizationMembership[];
  } = {},
): readonly Scope[] {
  const out: Scope[] = [];
  for (const raw of raws) {
    const s = parseScope(raw, meta);
    if (s !== null) {
      out.push(s);
    }
  }
  return out;
}

/**
 * Pick a default scope for the caller. Heuristic:
 *
 *   - platform_superadmin (has "global")            -> "global"
 *   - platform_operator    (has "platform")         -> "platform"
 *   - network_operator     (only network:* scopes)  -> first network:*
 *   - organization member  (only organization:*)    -> first organization:*
 *   - empty                                          -> null
 *
 * The user can change scope via the selector; this only determines the
 * INITIAL value.
 */
export function defaultScope(scopes: readonly Scope[]): Scope | null {
  if (scopes.length === 0) {
    return null;
  }
  const global = scopes.find((s) => s.kind === "global");
  if (global !== undefined) {
    return global;
  }
  const platform = scopes.find((s) => s.kind === "platform");
  if (platform !== undefined) {
    return platform;
  }
  const network = scopes.find((s) => s.kind === "network");
  if (network !== undefined) {
    return network;
  }
  const org = scopes.find((s) => s.kind === "organization");
  if (org !== undefined) {
    return org;
  }
  return scopes[0] ?? null;
}

const ACTIVE_SCOPE_KEY = "arena.admin.activeScope";

/** Read the previously-selected active scope from sessionStorage. */
export function readPersistedScope(): string | null {
  try {
    return sessionStorage.getItem(ACTIVE_SCOPE_KEY);
  } catch {
    return null;
  }
}

/** Persist the active scope (best-effort; silently ignored if blocked). */
export function persistScope(raw: string | null): void {
  try {
    if (raw === null) {
      sessionStorage.removeItem(ACTIVE_SCOPE_KEY);
    } else {
      sessionStorage.setItem(ACTIVE_SCOPE_KEY, raw);
    }
  } catch {
    // Storage unavailable (private mode / SSR); not fatal.
  }
}

/**
 * Resolve the initial active scope given the available scopes and any
 * previously-persisted choice. The persisted scope wins only when it
 * still appears in the available set.
 */
export function resolveInitialScope(
  scopes: readonly Scope[],
  persistedRaw: string | null,
): Scope | null {
  if (persistedRaw !== null) {
    const match = scopes.find((s) => s.raw === persistedRaw);
    if (match !== undefined) {
      return match;
    }
  }
  return defaultScope(scopes);
}
