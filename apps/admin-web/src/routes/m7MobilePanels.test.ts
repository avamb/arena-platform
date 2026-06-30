/**
 * Wave M-7 (#300) executable gate.
 *
 * Pins the contract for two mobile-responsive panels:
 *   - Webhook subscribers list (S-3, originally feature #294)
 *   - Scan-events read view on the ticket drawer (S-4, feature #295)
 *
 * Both surfaces must render through `<ResponsiveTable>` (stacked cards
 * below md, real <table> at >= md). The webhook secret reveal/copy
 * affordance pins a 44 px tap target so the panel is usable on a
 * phone during an on-call incident.
 *
 * These are file-shape grep checks; the rendered behaviour of
 * ResponsiveTable itself is covered by layout.test.tsx and the helper
 * tests in webhooks.test.ts / tickets.test.ts.
 */
import { describe, expect, it } from "vitest";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { M7_TAP_TARGET_PX, copyToClipboard } from "./webhooks";

const HERE = dirname(fileURLToPath(import.meta.url));

async function readRoute(name: string): Promise<string> {
  return await readFile(resolve(HERE, name), "utf8");
}

describe("Wave M-7 mobile panels (#300) — ResponsiveTable conversion", () => {
  it("publishes the 44 px tap-target constant the M-7 contract pins", () => {
    expect(M7_TAP_TARGET_PX).toBe(44);
  });

  it("webhooks.tsx imports ResponsiveTable from the layout barrel and uses it for the subscribers list", async () => {
    const src = await readRoute("webhooks.tsx");
    expect(src.includes('from "@/components/layout"')).toBe(true);
    expect(src.includes("ResponsiveTable")).toBe(true);
    // The list must be rendered via ResponsiveTable<WebhookSubscriberSummary>
    // (the type parameter pins the row shape — drift here means the table
    // was rebuilt by hand and lost the responsive card layout).
    expect(
      /ResponsiveTable<WebhookSubscriberSummary>/.test(src),
      "webhooks.tsx must render the subscribers list as ResponsiveTable<WebhookSubscriberSummary>",
    ).toBe(true);
    // Tests-id on the table preserved so consumers of webhooks-table do
    // not break after the conversion.
    expect(src.includes('id="webhooks-table"')).toBe(true);
  });

  it("tickets.tsx scan-events view is rendered through ResponsiveTable", async () => {
    const src = await readRoute("tickets.tsx");
    expect(src.includes('from "@/components/layout"')).toBe(true);
    expect(
      /ResponsiveTable<AdminScanEvent>/.test(src),
      "tickets.tsx must render the scan-events history as ResponsiveTable<AdminScanEvent>",
    ).toBe(true);
    // The id stays stable so any downstream automation keeps working.
    expect(src.includes('id="tickets-scans-table"')).toBe(true);
  });

  it("webhook signing secret renders as a type=password input with a reveal toggle", async () => {
    const src = await readRoute("webhooks.tsx");
    // password-toggle pattern: revealed state switches the input's type
    // attribute between "password" and "text". We assert the literal
    // expression to make sure nobody quietly downgrades the field to a
    // bare <code> block again.
    expect(
      /type=\{revealed \? "text" : "password"\}/.test(src),
      "webhooks.tsx secret field must toggle between password and text",
    ).toBe(true);
    expect(src.includes('data-testid="webhooks-secret-reveal"')).toBe(true);
    expect(src.includes('aria-pressed={revealed}')).toBe(true);
    // The input must stay read-only (we never want the operator to edit
    // the rendered secret in place).
    expect(/readOnly/.test(src)).toBe(true);
  });

  it("reveal toggle + copy button are pinned to the 44 px M-7 tap-target", async () => {
    const src = await readRoute("webhooks.tsx");
    // The two icon-style buttons share tapTargetIconButtonStyle which
    // sets BOTH minHeight and minWidth from the exported constant.
    expect(src.includes("tapTargetIconButtonStyle")).toBe(true);
    expect(/minHeight:\s*M7_TAP_TARGET_PX/.test(src)).toBe(true);
    expect(/minWidth:\s*M7_TAP_TARGET_PX/.test(src)).toBe(true);
    // Copy button exposes a tap-friendly aria-label + testid.
    expect(src.includes('data-testid="webhooks-secret-copy"')).toBe(true);
    expect(
      src.includes('aria-label="Copy signing secret to clipboard"'),
    ).toBe(true);
  });

  it("copyToClipboard prefers navigator.clipboard when available", async () => {
    const calls: string[] = [];
    const originalNavigator = globalThis.navigator;
    try {
      Object.defineProperty(globalThis, "navigator", {
        configurable: true,
        value: {
          clipboard: {
            writeText: async (value: string) => {
              calls.push(value);
            },
          },
        },
      });
      const ok = await copyToClipboard("hunter2");
      expect(ok).toBe(true);
      expect(calls).toEqual(["hunter2"]);
    } finally {
      Object.defineProperty(globalThis, "navigator", {
        configurable: true,
        value: originalNavigator,
      });
    }
  });

  it("copyToClipboard reports failure when no clipboard path is available", async () => {
    const originalNavigator = globalThis.navigator;
    const originalDocument = (globalThis as { document?: unknown }).document;
    try {
      Object.defineProperty(globalThis, "navigator", {
        configurable: true,
        value: { clipboard: undefined },
      });
      Object.defineProperty(globalThis, "document", {
        configurable: true,
        value: undefined,
      });
      const ok = await copyToClipboard("anything");
      expect(ok).toBe(false);
    } finally {
      Object.defineProperty(globalThis, "navigator", {
        configurable: true,
        value: originalNavigator,
      });
      Object.defineProperty(globalThis, "document", {
        configurable: true,
        value: originalDocument,
      });
    }
  });

  it("neither converted file reintroduces mock-store patterns", async () => {
    const forbidden = [
      /\bglobalThis\.devStore\b/,
      /\bmockDb\b/i,
      /\bmockData\b/,
      /\bfakeData\b/,
    ];
    for (const f of ["webhooks.tsx", "tickets.tsx"] as const) {
      const src = await readRoute(f);
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
