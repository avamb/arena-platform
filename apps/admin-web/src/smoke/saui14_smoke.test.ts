/**
 * SAUI-14 -- End-to-end smoke and real-data guardrails (vitest layer).
 *
 * In YOLO mode the browser harness is not available, so the end-to-end
 * intent of SAUI-14 is enforced at the unit/integration layer:
 *
 *   1. PRODUCTION-CODE GREP: assert that the production sources under
 *      apps/admin-web/src (excluding tests/fixtures) contain none of
 *      the forbidden mock/fake/devStore/TODO-real/STUB/MOCK patterns.
 *      This is the same guard the shell script
 *      `scripts/admin-smoke-guardrails.mjs` enforces; we duplicate it
 *      at the vitest layer so a developer running `npm test` on the
 *      admin app catches the regression without remembering the
 *      separate script.
 *
 *   2. PERMISSION-GATE COVERAGE: every NAV_ENTRY whose permission is
 *      not "always" MUST be registered in the route tree AND its
 *      component MUST be wrapped in <RequirePermission /> with the
 *      matching nav entry. This is the direct-URL-access guard --
 *      without it, a stale tab whose sidebar hid an entry could still
 *      reach the page by typing the URL.
 *
 *   3. REASON-PROMPT COVERAGE: the cross-tenant superadmin endpoint
 *      families (/v1/admin/organizations|orders|tickets|refunds and
 *      /v1/admin/impersonate) and the SAUI-09 mutation-only families
 *      (/v1/operator-networks, /v1/admin/networks) are gated by
 *      requiresAdminReason(). Smoke asserts both positive and negative
 *      cases so the reason gate cannot silently drop coverage.
 *
 *   4. ROUTE COVERAGE: every NAV_ENTRY's `to` resolves to a registered
 *      route in routeTree (no silent 404 for a sidebar link).
 *
 * The suite is intentionally logic-level (no DOM rendering) so it runs
 * in the existing node-environment vitest config without adding
 * jsdom/Playwright dependencies. Browser-level assertions remain a
 * follow-up when the Playwright harness is re-enabled.
 */
import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { NAV_ENTRIES, type NavEntry } from "@/lib/auth/navConfig";
import { requiresAdminReason } from "@/lib/api/reason";

// ---------------------------------------------------------------------------
// 1. Production-code forbidden-pattern grep
// ---------------------------------------------------------------------------

const SMOKE_FILE = fileURLToPath(import.meta.url);
const SRC_ROOT = join(dirname(SMOKE_FILE), "..");

interface ForbiddenPattern {
  readonly id: string;
  readonly re: RegExp;
}

/**
 * Patterns mirrored from scripts/admin-smoke-guardrails.mjs. Keep both
 * lists in sync. The SAUI-14 spec calls out exactly this family.
 */
const FORBIDDEN_PATTERNS: readonly ForbiddenPattern[] = [
  { id: "globalThis", re: /\bglobalThis\b/ },
  { id: "devStore", re: /\bdevStore\b/ },
  { id: "dev-store", re: /dev-store/ },
  { id: "mockDb", re: /\bmockDb\b/i },
  { id: "mockData", re: /\bmockData\b/ },
  { id: "fakeData", re: /\bfakeData\b/ },
  { id: "sampleData", re: /\bsampleData\b/ },
  { id: "dummyData", re: /\bdummyData\b/ },
  { id: "TODO-real", re: /TODO[^\n]*\breal\b/i },
  { id: "TODO-database", re: /TODO[^\n]*\bdatabase\b/i },
  { id: "STUB", re: /\bSTUB\b/ },
  { id: "MOCK", re: /\bMOCK\b/ },
];

function isExempt(absPath: string): boolean {
  const rel = relative(SRC_ROOT, absPath).replace(/\\/g, "/");
  if (rel.startsWith("..")) return true;
  if (/\.test\.(ts|tsx|js|jsx)$/.test(rel)) return true;
  if (/\.spec\.(ts|tsx|js|jsx)$/.test(rel)) return true;
  if (rel === "test-setup.ts") return true;
  if (rel.includes("/__tests__/")) return true;
  if (rel.includes("/__fixtures__/")) return true;
  if (rel.includes("/__mocks__/")) return true;
  if (rel.startsWith("smoke/")) return true;
  return false;
}

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === "dist") continue;
    const abs = join(dir, entry);
    const st = statSync(abs);
    if (st.isDirectory()) {
      walk(abs, out);
    } else if (/\.(ts|tsx|js|jsx|mjs|cjs)$/.test(entry)) {
      out.push(abs);
    }
  }
  return out;
}

function stripComments(rawLine: string): string {
  const trimmed = rawLine.trim();
  if (trimmed.startsWith("//")) return "";
  if (trimmed.startsWith("/*")) return "";
  if (trimmed.startsWith("*")) return "";
  const idx = rawLine.indexOf("//");
  return idx === -1 ? rawLine : rawLine.slice(0, idx);
}

interface Hit {
  readonly file: string;
  readonly line: number;
  readonly pattern: string;
  readonly snippet: string;
}

function scanForbidden(): Hit[] {
  const hits: Hit[] = [];
  const files = walk(SRC_ROOT);
  for (const file of files) {
    if (isExempt(file)) continue;
    const content = readFileSync(file, "utf8");
    const lines = content.split(/\r?\n/);
    for (let i = 0; i < lines.length; i++) {
      const line = stripComments(lines[i]);
      if (line === "") continue;
      for (const pat of FORBIDDEN_PATTERNS) {
        if (pat.re.test(line)) {
          hits.push({
            file: relative(SRC_ROOT, file).replace(/\\/g, "/"),
            line: i + 1,
            pattern: pat.id,
            snippet: lines[i].trim().slice(0, 200),
          });
        }
      }
    }
  }
  return hits;
}

describe("SAUI-14 production-code forbidden-pattern grep", () => {
  it("admin-web/src contains no globalThis / devStore / mock* / fake* / sample* / dummy* / TODO-real / TODO-database / STUB / MOCK hits in production code", () => {
    const hits = scanForbidden();
    if (hits.length > 0) {
      const report = hits
        .map(
          (h) =>
            `  ${h.file}:${h.line}  [${h.pattern}]\n    > ${h.snippet}`,
        )
        .join("\n");
      throw new Error(
        `Forbidden mock/fake patterns leaked into production code:\n${report}\n` +
          `Either replace with real backend wiring / honest backend-gap tiles, or move the file under __tests__/ if it is genuinely a test fixture.`,
      );
    }
    expect(hits).toEqual([]);
  });

  it("covers every pattern named in the SAUI-14 description", () => {
    // The SAUI-14 spec text enumerates these tokens explicitly. We pin
    // the list so a future edit cannot silently drop a guard.
    const required = [
      "mockData",
      "fakeData",
      "sampleData",
      "dummyData",
      "globalThis",
      "devStore",
      "TODO-real",
      "STUB",
      "MOCK",
    ];
    const ids = new Set(FORBIDDEN_PATTERNS.map((p) => p.id));
    for (const r of required) {
      expect(ids.has(r), `missing forbidden-pattern guard: ${r}`).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// 2. Permission-gate coverage -- every guarded NAV_ENTRY has a registered
//    route, AND the route component wraps its body in <RequirePermission />.
// ---------------------------------------------------------------------------

/**
 * Authoritative registry: maps every guarded NavEntry path to the route
 * module's identifier (as imported in routeTree.ts) and the source file
 * declaring its createRoute() call.
 *
 * We use an explicit registry instead of source-code regex so that
 *   - shorthand `path,` keyed createRoute() calls (e.g. legacyPlaceholders)
 *     are correctly accounted for; and
 *   - unrelated `path: "/..."` string literals inside route bodies
 *     (e.g. API call paths or observability gap descriptions) cannot
 *     pollute the route set.
 */
const ROUTE_REGISTRY: Readonly<
  Record<string, { readonly routeId: string; readonly file: string }>
> = {
  "/networks": { routeId: "NetworksRoute", file: "networks.tsx" },
  "/users": { routeId: "UsersRoute", file: "users.tsx" },
  "/organizations": { routeId: "OrganizationsRoute", file: "organizations.tsx" },
  "/orders": { routeId: "OrdersRoute", file: "orders.tsx" },
  "/tickets": { routeId: "TicketsRoute", file: "tickets.tsx" },
  "/refunds": { routeId: "RefundsRoute", file: "refunds.tsx" },
  "/audit": { routeId: "AuditRoute", file: "audit.tsx" },
  "/observability": { routeId: "ObservabilityRoute", file: "observability.tsx" },
  "/events": { routeId: "EventsRoute", file: "legacyPlaceholders.tsx" },
  "/venues": { routeId: "VenuesRoute", file: "legacyPlaceholders.tsx" },
  "/channels": { routeId: "ChannelsRoute", file: "legacyPlaceholders.tsx" },
  "/payments": { routeId: "PaymentsRoute", file: "legacyPlaceholders.tsx" },
  "/reports": { routeId: "ReportsRoute", file: "legacyPlaceholders.tsx" },
  "/content": { routeId: "ContentRoute", file: "legacyPlaceholders.tsx" },
  "/pos": { routeId: "PosRoute", file: "legacyPlaceholders.tsx" },
  "/geo": { routeId: "GeoRoute", file: "guarded.tsx" },
};

/**
 * Detail / public route identifiers registered in routeTree.addChildren()
 * that do NOT appear as guarded NavEntries. These inherit their parent's
 * permission gate (detail routes) or are intentionally unauthenticated
 * (index / login).
 */
const NON_NAV_ROUTE_IDS: ReadonlySet<string> = new Set([
  "IndexRoute",
  "LoginRoute",
  "NetworkDetailRoute",
]);

/**
 * Pull every identifier referenced inside RootRoute.addChildren([...]).
 */
function registeredRouteIds(): Set<string> {
  const treeSrc = readFileSync(join(SRC_ROOT, "routeTree.ts"), "utf8");
  const childrenMatch = treeSrc.match(/addChildren\(\s*\[([\s\S]*?)\]\)/);
  const ids = new Set<string>();
  if (childrenMatch !== null) {
    for (const tok of childrenMatch[1].split(/[\s,]+/)) {
      const t = tok.trim();
      if (t !== "") ids.add(t);
    }
  }
  return ids;
}

describe("SAUI-14 permission-gate coverage", () => {
  const guarded = NAV_ENTRIES.filter(
    (e): e is NavEntry => e.permission !== "always",
  );

  it("every guarded NAV_ENTRY has a route registered in ROUTE_REGISTRY (no silent 404 for sidebar links)", () => {
    const missing: string[] = [];
    for (const entry of guarded) {
      if (ROUTE_REGISTRY[entry.to] === undefined) missing.push(entry.to);
    }
    expect(
      missing,
      `nav entries without an entry in ROUTE_REGISTRY: ${missing.join(", ")}`,
    ).toEqual([]);
  });

  it("routeTree.addChildren references every guarded route module (no orphan Route exports)", () => {
    const registered = registeredRouteIds();
    const expected = Object.values(ROUTE_REGISTRY).map((v) => v.routeId);
    const missing = expected.filter((id) => !registered.has(id));
    expect(
      missing,
      `route module Route exports not added to routeTree: ${missing.join(", ")}`,
    ).toEqual([]);
  });

  it("every guarded route component imports RequirePermission (direct-URL backstop)", () => {
    const routesDir = join(SRC_ROOT, "routes");
    const failures: string[] = [];
    const seenFiles = new Set<string>();
    for (const entry of guarded) {
      const reg = ROUTE_REGISTRY[entry.to];
      if (reg === undefined) {
        failures.push(
          `no ROUTE_REGISTRY entry for ${entry.to} -- extend ROUTE_REGISTRY in this smoke test when adding new guarded routes.`,
        );
        continue;
      }
      const fileName = reg.file;
      const abs = join(routesDir, fileName);
      if (seenFiles.has(abs)) continue;
      seenFiles.add(abs);
      const src = readFileSync(abs, "utf8");
      // Accept either a direct import of RequirePermission OR a use of
      // LegacyModulePlaceholder (which wraps RequirePermission internally
      // -- SAUI-12 backstop).
      const usesRequirePermission =
        /\bRequirePermission\b/.test(src) ||
        /\bLegacyModulePlaceholder\b/.test(src);
      if (!usesRequirePermission) {
        failures.push(
          `${fileName} (entry ${entry.id} -> ${entry.to}) does not reference RequirePermission or LegacyModulePlaceholder`,
        );
      }
    }
    expect(failures, failures.join("\n")).toEqual([]);
  });

  it("LegacyModulePlaceholder itself wraps its body in RequirePermission", () => {
    const src = readFileSync(
      join(SRC_ROOT, "components", "LegacyModulePlaceholder.tsx"),
      "utf8",
    );
    expect(src).toMatch(/\bRequirePermission\b/);
  });
});

// ---------------------------------------------------------------------------
// 3. Reason-prompt coverage
// ---------------------------------------------------------------------------

describe("SAUI-14 superadmin reason-prompt coverage", () => {
  it.each([
    "/v1/admin/organizations",
    "/v1/admin/organizations/0193f01a-0001-7000-8000-000000000001",
    "/v1/admin/orders",
    "/v1/admin/orders?org_id=abc&limit=20",
    "/v1/admin/tickets",
    "/v1/admin/refunds",
    "/v1/admin/impersonate",
  ])("requires X-Admin-Reason on cross-tenant GET %s", (path) => {
    expect(requiresAdminReason(path)).toBe(true);
    expect(requiresAdminReason(path, "GET")).toBe(true);
  });

  it.each([
    ["/v1/operator-networks", "POST"],
    ["/v1/operator-networks/0193f01a-0001-7000-8000-000000000099", "PATCH"],
    ["/v1/admin/users", "POST"],
    ["/v1/admin/networks", "POST"],
    ["/v1/admin/networks/0193f01a-0001-7000-8000-000000000099/users", "DELETE"],
  ] as const)(
    "requires X-Admin-Reason on mutation %s %s",
    (path, method) => {
      expect(requiresAdminReason(path, method)).toBe(true);
    },
  );

  it.each([
    ["/v1/operator-networks", "GET"],
    ["/v1/operator-networks/0193f01a-0001-7000-8000-000000000099", "GET"],
    ["/v1/admin/networks", "GET"],
  ] as const)(
    "does NOT prompt on read-only %s %s (operator can browse)",
    (path, method) => {
      expect(requiresAdminReason(path, method)).toBe(false);
    },
  );

  it.each([
    "/v1/me",
    "/v1/auth/login",
    "/v1/auth/refresh",
    "/v1/healthz",
  ])("does NOT prompt on neutral path %s", (path) => {
    expect(requiresAdminReason(path)).toBe(false);
    expect(requiresAdminReason(path, "GET")).toBe(false);
    expect(requiresAdminReason(path, "POST")).toBe(false);
  });

  // Network audit log subpath is a read-only resource. The SAUI-09 gate
  // applies only on mutation methods; GET browsing of audit logs is
  // unprompted so operators can review activity without filling out a
  // reason form. (POST/PATCH/DELETE on this read-only path are not
  // valid endpoints and are intentionally NOT asserted here.)
  it("does NOT prompt on the read-only network audit subpath", () => {
    const path =
      "/v1/operator-networks/0193f01a-0001-7000-8000-000000000099/audit";
    expect(requiresAdminReason(path)).toBe(false);
    expect(requiresAdminReason(path, "GET")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 4. Route-tree integrity smoke -- catch the "registered a route in the
//    tree but forgot the nav entry" failure where the page silently lacks
//    a permission gate because no NavEntry exists for it.
// ---------------------------------------------------------------------------

describe("SAUI-14 route-tree / nav-config parity", () => {
  it("every routeTree child resolves to a guarded NavEntry, a public route, or an inherited-gate detail route", () => {
    const navRouteIds = new Set<string>(
      Object.values(ROUTE_REGISTRY).map((v) => v.routeId),
    );
    const orphans: string[] = [];
    for (const id of registeredRouteIds()) {
      if (NON_NAV_ROUTE_IDS.has(id)) continue;
      if (navRouteIds.has(id)) continue;
      orphans.push(id);
    }
    expect(
      orphans,
      `route identifiers in routeTree.addChildren without a NavEntry: ${orphans.join(", ")}. ` +
        `Either add a NavEntry + ROUTE_REGISTRY mapping, or list the id in NON_NAV_ROUTE_IDS if it inherits its parent's gate.`,
    ).toEqual([]);
  });
});
