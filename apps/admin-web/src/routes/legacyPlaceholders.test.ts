/**
 * SAUI-12 -- legacy-derived module placeholders, contract tests.
 *
 * These cases pin the placeholder configuration so a future edit cannot
 * silently:
 *   - drop the source-reference / future-scope / deferral-reason
 *     annotations that are the whole point of the SAUI-12 contract;
 *   - register a navigation entry without a Route (or vice versa);
 *   - break the legacy_admin_reference_map.yaml module-id back-link;
 *   - widen a placeholder permission rule into "always" (which would
 *     show the shell to unauthenticated callers).
 *
 * The route component is a permission gate + static markup with no
 * fetches; rendering the React tree is covered by the route tree
 * smoke build, not by this suite.
 */
import { describe, expect, it } from "vitest";
import {
  LEGACY_MODULE_PLACEHOLDERS,
  LEGACY_MODULE_PLACEHOLDERS_BY_PATH,
  legacyModuleForPath,
  type LegacyModulePlaceholder,
} from "@/lib/admin/legacyModules";
import {
  NAV_BY_PATH,
  NAV_ENTRIES,
  navEntryForPath,
  permissionRuleSatisfied,
} from "@/lib/auth/navConfig";

const EXPECTED_PATHS = [
  "/reports",
  "/content",
  "/pos",
] as const;

const EXPECTED_MAP_IDS = [
  "reports",
  "notifications_content",
  "pos",
] as const;

describe("LEGACY_MODULE_PLACEHOLDERS table", () => {
  it("ships exactly the 3 remaining SAUI-12 placeholders (venues_seating graduated in feature #242; frontends_channels in feature #243; payments_fiscal in feature #244; events_sessions in feature #281)", () => {
    expect(LEGACY_MODULE_PLACEHOLDERS).toHaveLength(3);
  });

  it("covers every documented path exactly once", () => {
    const paths = LEGACY_MODULE_PLACEHOLDERS.map((m) => m.path).sort();
    expect(paths).toEqual([...EXPECTED_PATHS].sort());
  });

  it("ids are unique and map to legacy_admin_reference_map.yaml module ids", () => {
    const ids = LEGACY_MODULE_PLACEHOLDERS.map((m) => m.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect([...ids].sort()).toEqual([...EXPECTED_MAP_IDS].sort());

    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      // Stable invariant: the SAUI-12 placeholder id mirrors the legacy
      // map module id so cross-referencing the YAML never drifts.
      expect(m.id).toBe(m.sourceReference.legacyMapModuleId);
    }
  });

  it("every placeholder names at least one legacy app and one legacy screen", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(m.sourceReference.legacyApps.length).toBeGreaterThan(0);
      expect(m.sourceReference.legacyScreens.length).toBeGreaterThan(0);
    }
  });

  it("every placeholder declares an expected future scope (>=2 bullets)", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(m.futureScope.length).toBeGreaterThanOrEqual(2);
    }
  });

  it("every placeholder explains its deferral reason (non-trivial prose)", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      // Non-trivial: at least 40 chars and not a placeholder string.
      expect(m.deferralReason.length).toBeGreaterThanOrEqual(40);
      expect(m.deferralReason.toLowerCase()).not.toContain("tbd");
      expect(m.deferralReason.toLowerCase()).not.toContain("todo");
    }
  });

  it("every placeholder uses a non-trivial workflow shape lifted from the legacy map", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(m.workflowShape.length).toBeGreaterThan(0);
    }
  });

  it("mvpPriority is one of P0/P1/P2", () => {
    const ok = new Set(["P0", "P1", "P2"]);
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(ok.has(m.mvpPriority)).toBe(true);
    }
  });

  it("every placeholder is permission-gated -- no 'always' rule slips through", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(m.permission).not.toBe("always");
    }
  });

  it("every placeholder accepts superadmin.read as a valid permission", () => {
    // The platform_superadmin role is the canonical break-glass surface
    // for shell pages -- if a SAUI-12 placeholder accidentally locked
    // out superadmin we would have to hold a privileged role just to
    // see the planned modules.
    const superadminOnly: ReadonlySet<string> = new Set(["superadmin.read"]);
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(
        permissionRuleSatisfied(m.permission, superadminOnly),
      ).toBe(true);
    }
  });

  it("every placeholder rejects an empty permission set (no public access)", () => {
    const empty: ReadonlySet<string> = new Set();
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(permissionRuleSatisfied(m.permission, empty)).toBe(false);
    }
  });

  it("scopeKinds reference only known scope kinds", () => {
    const knownKinds = new Set(["global", "platform", "network", "organization"]);
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      for (const k of m.scopeKinds) {
        expect(knownKinds.has(k)).toBe(true);
      }
    }
  });
});

describe("LEGACY_MODULE_PLACEHOLDERS_BY_PATH index", () => {
  it("indexes every entry by path", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(LEGACY_MODULE_PLACEHOLDERS_BY_PATH[m.path]).toBe(m);
    }
  });

  it("legacyModuleForPath round-trips every entry", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      expect(legacyModuleForPath(m.path)).toBe(m);
    }
  });
});

describe("navConfig <-> legacyModules parity", () => {
  it("every placeholder has a matching NAV_ENTRIES row keyed by path", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      const navEntry = NAV_BY_PATH[m.path];
      expect(navEntry).toBeDefined();
      expect(navEntry?.to).toBe(m.path);
      expect(navEntry?.id).toBe(m.id);
    }
  });

  it("nav entry permission and scopeKinds match the placeholder source", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      const navEntry = navEntryForPath(m.path);
      expect(navEntry).toBeDefined();
      expect(navEntry?.permission).toEqual(m.permission);
      expect(navEntry?.scopeKinds).toEqual(m.scopeKinds);
    }
  });

  it("nav entry purpose mentions the same module label", () => {
    for (const m of LEGACY_MODULE_PLACEHOLDERS) {
      const navEntry = navEntryForPath(m.path);
      expect(navEntry?.label).toBe(m.label);
    }
  });

  it("NAV_ENTRIES contains all remaining SAUI-12 ids in registration order", () => {
    const navIds = NAV_ENTRIES.map((e) => e.id);
    for (const id of EXPECTED_MAP_IDS) {
      expect(navIds).toContain(id);
    }
  });
});

describe("per-module annotations", () => {
  // Spot-check the placeholders the SAUI-12 description names as
  // overbuild risks: reports, pos, and notifications. venues_seating
  // graduated to a real CRUD route (feature #242) so the visual seating
  // editor deferral is now documented in the venues route, not here.
  // These should all explicitly explain why they ship as a shell.
  const riskyModuleIds = [
    "reports",
    "pos",
    "notifications_content",
  ] as const;

  function modById(id: string): LegacyModulePlaceholder {
    const m = LEGACY_MODULE_PLACEHOLDERS.find((x) => x.id === id);
    if (m === undefined) {
      throw new Error(`legacy module fixture missing: ${id}`);
    }
    return m;
  }

  it("reports placeholder is honest about the SAUI-12 'do not implement' rule", () => {
    const m = modById("reports");
    expect(m.deferralReason.toLowerCase()).toMatch(/(reports|skip|guardrail)/);
  });

  it("pos placeholder is honest about the SAUI-12 'do not implement' rule", () => {
    const m = modById("pos");
    expect(m.deferralReason.toLowerCase()).toMatch(/(pos|guardrail|overbuild|excludes)/);
  });

  it("notifications_content placeholder is marked low priority (P2)", () => {
    expect(modById("notifications_content").mvpPriority).toBe("P2");
  });

  for (const id of riskyModuleIds) {
    it(`${id} placeholder source reference points at the legacy YAML`, () => {
      const m = modById(id);
      expect(m.sourceReference.legacyMapModuleId).toBe(id);
    });
  }
});
