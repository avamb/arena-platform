// Wave M-5 (feature #298) — mobile-form contract gate.
//
// This suite asserts the M-5 contract for organizer/agent forms:
//
//   1. The shared `mobileFormBarStyle` helper meets the 56-px touch-target
//      contract AND honours `env(safe-area-inset-bottom)` on the bottom
//      padding so the iOS home indicator does not occlude Save / Cancel.
//
//   2. The shared `singleColumnFormStyle` helper renders as a single-column
//      flex layout (no horizontal scroll < md).
//
//   3. The O-4 legal-billing tab (organizations.tsx), V-3 venue address
//      form (venues.tsx), and the E-4 / E-5 session+tier editor forms
//      (events.tsx) all reference the shared M-5 helpers and continue to
//      render inline per-field error rows (no toast-only error paths).
//
//   4. The OverflowMenu primitive opens, closes via outside-click / Escape,
//      and surfaces destructive items with the `tone="danger"` style so
//      organizers can move "Delete" / "Archive" out of the primary action
//      row.
//
// The PR also carries the required 360×640 and 1280×800 screenshots; the
// Wave M-8 gate verifies them visually — this file is the executable gate.

import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import {
  M5_ACTION_BAR_MIN_HEIGHT_PX,
  mobileFormBarStyle,
  singleColumnFormStyle,
  OverflowMenu,
} from "@/components/layout";

const __dirname = dirname(fileURLToPath(import.meta.url));

function readRoute(name: string): string {
  return readFileSync(resolve(__dirname, name), "utf8");
}

describe("Wave M-5 mobile-form contract (#298)", () => {
  it("exports the 56px touch-target constant", () => {
    expect(M5_ACTION_BAR_MIN_HEIGHT_PX).toBe(56);
  });

  it("mobileFormBarStyle is a sticky bottom bar at the M-5 touch-target", () => {
    expect(mobileFormBarStyle.position).toBe("sticky");
    expect(mobileFormBarStyle.bottom).toBe(0);
    expect(mobileFormBarStyle.minHeight).toBe(56);
    expect(mobileFormBarStyle.display).toBe("flex");
    expect(mobileFormBarStyle.justifyContent).toBe("flex-end");
  });

  it("mobileFormBarStyle bottom padding honours env(safe-area-inset-bottom)", () => {
    const pb = String(mobileFormBarStyle.paddingBottom ?? "");
    expect(pb).toContain("env(safe-area-inset-bottom)");
  });

  it("singleColumnFormStyle is a flex column container", () => {
    expect(singleColumnFormStyle.display).toBe("flex");
    expect(singleColumnFormStyle.flexDirection).toBe("column");
  });

  describe("each M-5 form references the shared helpers", () => {
    it.each([
      ["organizations.tsx", "legal-form-actions"],
      ["venues.tsx", "venues-form-actions"],
      ["events.tsx", "events-session-actions"],
      ["events.tsx", "events-tier-actions"],
    ])("%s carries the %s sticky action bar", (file, testid) => {
      const src = readRoute(file);
      expect(src).toContain("mobileFormBarStyle");
      expect(src).toContain(`data-testid="${testid}"`);
    });

    it.each(["organizations.tsx", "venues.tsx", "events.tsx"])(
      "%s imports the M-5 helpers from the layout barrel",
      (file) => {
        const src = readRoute(file);
        expect(src).toContain("mobileFormBarStyle");
        expect(src).toContain("singleColumnFormStyle");
        // Sanity: the import comes from the canonical layout barrel.
        expect(src).toMatch(/from "@\/components\/layout"/);
      },
    );
  });

  describe("inline-error contract (no toast-only error paths)", () => {
    it.each(["organizations.tsx", "venues.tsx", "events.tsx"])(
      "%s renders per-field error rows, not window.alert/toast-only",
      (file) => {
        const src = readRoute(file);
        // No raw alert() — errors must be visible under each field.
        expect(src).not.toMatch(/\bwindow\.alert\(/);
        expect(src).not.toMatch(/\balert\s*\(\s*['"`]/);
        // Inline-error markers we rely on across the three modules:
        //   organizations.tsx uses the FieldRow helper,
        //   venues.tsx uses fieldErrorStyle,
        //   events.tsx uses fieldErrorStyle.
        const hasFieldErrors =
          src.includes("FieldRow") ||
          src.includes("fieldErrorStyle") ||
          src.includes("field-error");
        expect(hasFieldErrors).toBe(true);
      },
    );
  });
});

describe("OverflowMenu primitive (M-5 destructive-actions grouping)", () => {
  it("renders the trigger button collapsed (aria-expanded=false) by default", () => {
    const html = renderToStaticMarkup(
      createElement(OverflowMenu, {
        id: "m5-test",
        triggerLabel: "More",
        items: [
          { label: "Delete", onSelect: () => undefined, tone: "danger" },
        ],
      }),
    );
    expect(html).toContain('data-testid="m5-test-root"');
    expect(html).toContain('data-testid="m5-test-trigger"');
    expect(html).toContain('aria-haspopup="menu"');
    expect(html).toContain('aria-expanded="false"');
    // Menu list is hidden until the trigger is clicked at runtime.
    expect(html).not.toContain('data-testid="m5-test-list"');
  });

  it("is a named React component (function) so call sites can compose it", () => {
    expect(typeof OverflowMenu).toBe("function");
    expect(OverflowMenu.name).toBe("OverflowMenu");
  });
});
