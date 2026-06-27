/**
 * SAUI-03 -- scope parsing & default-scope selection tests.
 *
 * The scope module turns the raw /v1/me.available_scopes strings into
 * typed Scope objects and decides which one is "initially active" per
 * role preset. These tests pin the role-preset defaults from the spec:
 *
 *   - platform_superadmin (has "global")          -> "global"
 *   - platform_operator   (has "platform" only)   -> "platform"
 *   - network_operator    (has only network:*)    -> first network:*
 *   - org member          (has only organization:*) -> first organization:*
 */
import { afterEach, beforeAll, describe, expect, it } from "vitest";

// vitest defaults to a node environment; install a minimal in-memory
// sessionStorage shim so persistScope/readPersistedScope are exercisable
// without pulling in jsdom (which is not a project dep).
beforeAll(() => {
  if (typeof globalThis.sessionStorage === "undefined") {
    const store = new Map<string, string>();
    const shim: Storage = {
      get length() {
        return store.size;
      },
      clear: () => store.clear(),
      getItem: (k) => (store.has(k) ? (store.get(k) as string) : null),
      setItem: (k, v) => {
        store.set(k, String(v));
      },
      removeItem: (k) => {
        store.delete(k);
      },
      key: (i) => Array.from(store.keys())[i] ?? null,
    };
    Object.defineProperty(globalThis, "sessionStorage", {
      value: shim,
      configurable: true,
    });
  }
});
import {
  defaultScope,
  parseScope,
  parseScopes,
  persistScope,
  readPersistedScope,
  resolveInitialScope,
} from "@/lib/auth/scope";

afterEach(() => {
  try {
    sessionStorage.clear();
  } catch {
    /* noop */
  }
});

const NETWORK_UUID_A = "0193f01a-0001-7000-8000-000000000001";
const NETWORK_UUID_B = "0193f01a-0001-7000-8000-000000000002";
const ORG_UUID = "0193f01a-0001-7000-8000-00000000aaaa";

describe("parseScope", () => {
  it("parses the well-known top-level scopes", () => {
    expect(parseScope("global")).toEqual(
      expect.objectContaining({ kind: "global", id: null }),
    );
    expect(parseScope("platform")).toEqual(
      expect.objectContaining({ kind: "platform", id: null }),
    );
  });

  it("parses network scopes and labels them by network name when known", () => {
    const out = parseScope(`network:${NETWORK_UUID_A}`, {
      networks: [
        { id: NETWORK_UUID_A, name: "Northwind", slug: "northwind", status: "active" },
      ],
    });
    expect(out).not.toBeNull();
    expect(out?.kind).toBe("network");
    expect(out?.id).toBe(NETWORK_UUID_A);
    expect(out?.label).toMatch(/Northwind/);
  });

  it("falls back to a truncated UUID label when network metadata is missing", () => {
    const out = parseScope(`network:${NETWORK_UUID_B}`);
    expect(out?.label).toMatch(NETWORK_UUID_B.slice(0, 8));
  });

  it("parses organization scopes with membership context", () => {
    const out = parseScope(`organization:${ORG_UUID}`, {
      memberships: [
        {
          id: "m1",
          org_id: ORG_UUID,
          role: "organizer",
          status: "active",
          joined_at: "2026-06-27T00:00:00Z",
        },
      ],
    });
    expect(out?.kind).toBe("organization");
    expect(out?.label).toMatch(/organizer/);
  });

  it("rejects malformed UUIDs", () => {
    expect(parseScope("network:not-a-uuid")).toBeNull();
    expect(parseScope("organization:nope")).toBeNull();
  });

  it("returns null for entirely unknown scope shapes", () => {
    expect(parseScope("frobnicator:42")).toBeNull();
    expect(parseScope("")).toBeNull();
  });
});

describe("parseScopes", () => {
  it("preserves backend ordering and drops unparseable entries", () => {
    const out = parseScopes(["global", "garbage:1", `network:${NETWORK_UUID_A}`]);
    expect(out.map((s) => s.kind)).toEqual(["global", "network"]);
  });

  it("returns [] for an empty input", () => {
    expect(parseScopes([])).toEqual([]);
  });
});

describe("defaultScope (per role preset)", () => {
  it("platform_superadmin -> global", () => {
    const scopes = parseScopes(["global", `network:${NETWORK_UUID_A}`]);
    expect(defaultScope(scopes)?.kind).toBe("global");
  });

  it("platform_operator -> platform", () => {
    const scopes = parseScopes(["platform"]);
    expect(defaultScope(scopes)?.kind).toBe("platform");
  });

  it("network_operator -> first network", () => {
    const scopes = parseScopes([
      `network:${NETWORK_UUID_A}`,
      `network:${NETWORK_UUID_B}`,
    ]);
    expect(defaultScope(scopes)?.id).toBe(NETWORK_UUID_A);
  });

  it("org-only member -> first organization", () => {
    const scopes = parseScopes([`organization:${ORG_UUID}`]);
    expect(defaultScope(scopes)?.kind).toBe("organization");
  });

  it("no-permission user (empty scopes) -> null", () => {
    expect(defaultScope([])).toBeNull();
  });
});

describe("resolveInitialScope (persisted choice wins iff still available)", () => {
  it("returns the persisted scope when present and still available", () => {
    const scopes = parseScopes(["global", `network:${NETWORK_UUID_A}`]);
    const out = resolveInitialScope(scopes, `network:${NETWORK_UUID_A}`);
    expect(out?.raw).toBe(`network:${NETWORK_UUID_A}`);
  });

  it("falls back to default when the persisted scope is no longer granted", () => {
    const scopes = parseScopes(["global"]);
    const out = resolveInitialScope(scopes, `network:${NETWORK_UUID_A}`);
    expect(out?.kind).toBe("global");
  });

  it("returns null when there are no available scopes at all", () => {
    expect(resolveInitialScope([], `network:${NETWORK_UUID_A}`)).toBeNull();
  });
});

describe("persistScope + readPersistedScope round-trip", () => {
  it("persists and reads a scope", () => {
    persistScope(`network:${NETWORK_UUID_A}`);
    expect(readPersistedScope()).toBe(`network:${NETWORK_UUID_A}`);
  });

  it("clears the persisted scope when passed null", () => {
    persistScope(`network:${NETWORK_UUID_A}`);
    persistScope(null);
    expect(readPersistedScope()).toBeNull();
  });
});
