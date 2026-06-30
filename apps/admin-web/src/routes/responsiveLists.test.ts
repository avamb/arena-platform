/**
 * Wave M-4 (feature #297) -- shape assertions for the responsive list
 * conversion. Confirms each touched route file imports the shared layout
 * primitives via the barrel and (for the 5 support consoles) wires the
 * ResponsiveDrawer + useIsDesktop pair behind a Filters affordance.
 *
 * The assertions are file-shape grep checks (no DOM rendering) so the
 * suite stays compatible with the existing node-environment vitest
 * config; this mirrors the pattern in src/smoke/saui14_smoke.test.ts.
 */
import { describe, expect, it } from "vitest";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));

const SUPPORT_FILES = [
  "orders.tsx",
  "tickets.tsx",
  "refunds.tsx",
  "channels.tsx",
  "payments.tsx",
] as const;

const ADMIN_FILES = [
  "organizations.tsx",
  "venues.tsx",
  "events.tsx",
] as const;

const ALL_FILES = [...SUPPORT_FILES, ...ADMIN_FILES] as const;

async function readRoute(name: string): Promise<string> {
  return await readFile(resolve(HERE, name), "utf8");
}

describe("Wave M-4 responsive list conversion (#297)", () => {
  it("each modified route file parses (exports a Route via @tanstack/react-router)", async () => {
    for (const f of ALL_FILES) {
      const src = await readRoute(f);
      expect(
        src,
        `${f} must export a Route via createRoute()`,
      ).toMatch(/export\s+const\s+Route\s*=\s*createRoute/);
    }
  });

  it("each modified route imports from the layout barrel", async () => {
    for (const f of ALL_FILES) {
      const src = await readRoute(f);
      expect(
        src.includes('from "@/components/layout"'),
        `${f} must import from "@/components/layout"`,
      ).toBe(true);
      expect(
        src.includes("ResponsiveTable"),
        `${f} must reference ResponsiveTable`,
      ).toBe(true);
    }
  });

  it("each support-console route wires ResponsiveDrawer + useIsDesktop", async () => {
    for (const f of SUPPORT_FILES) {
      const src = await readRoute(f);
      expect(
        src.includes("ResponsiveDrawer"),
        `${f} must reference ResponsiveDrawer for the mobile filter sheet`,
      ).toBe(true);
      expect(
        src.includes("useIsDesktop"),
        `${f} must call useIsDesktop to choose between inline toolbar and the filter sheet`,
      ).toBe(true);
    }
  });

  it("modified routes contain no globalThis.devStore / mockDb / mockData / fakeData patterns (code, not comments)", async () => {
    const forbidden = [
      /\bglobalThis\.devStore\b/,
      /\bmockDb\b/i,
      /\bmockData\b/,
      /\bfakeData\b/,
    ];
    for (const f of ALL_FILES) {
      const src = await readRoute(f);
      // Strip line and block comments so doc-comments stating that a
      // module deliberately AVOIDS mockDb / devStore (a common header in
      // this codebase) do not false-fire the guard. We are checking
      // executable code, not prose.
      const stripped = src
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .split(/\r?\n/)
        .map((line) => {
          const idx = line.indexOf("//");
          return idx === -1 ? line : line.slice(0, idx);
        })
        .join("\n");
      for (const re of forbidden) {
        expect(
          re.test(stripped),
          `${f} unexpectedly contains ${re} in executable code`,
        ).toBe(false);
      }
    }
  });
});
