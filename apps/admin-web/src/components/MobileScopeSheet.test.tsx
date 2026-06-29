/**
 * Tests for MobileScopeSheet (Wave M-2).
 *
 * The sheet is rendered via renderToStaticMarkup to keep the test surface
 * fast and independent of a DOM. Behavioural concerns covered:
 *
 *   - filterScopes is a pure substring matcher (label / raw / id);
 *   - closed sheets render no DOM;
 *   - open sheets render the radiogroup, the search input, and one
 *     option per scope;
 *   - the active scope renders with aria-checked="true";
 *   - empty-state copy is emitted only when the filter has zero matches.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { MobileScopeSheet, filterScopes } from "./MobileScopeSheet";
import type { Scope } from "@/lib/auth/scope";

const FIXTURES: readonly Scope[] = [
  { raw: "global", kind: "global", id: null, label: "Global (all tenants)" },
  { raw: "platform", kind: "platform", id: null, label: "Platform operations" },
  {
    raw: "network:11111111-1111-1111-1111-111111111111",
    kind: "network",
    id: "11111111-1111-1111-1111-111111111111",
    label: "Network: Northern Arena",
  },
  {
    raw: "organization:22222222-2222-2222-2222-222222222222",
    kind: "organization",
    id: "22222222-2222-2222-2222-222222222222",
    label: "Organization: 22222222… (organizer)",
  },
];

describe("filterScopes", () => {
  it("returns the original array on empty query", () => {
    expect(filterScopes(FIXTURES, "")).toBe(FIXTURES);
    expect(filterScopes(FIXTURES, "   ")).toBe(FIXTURES);
  });

  it("matches against the label, case-insensitively", () => {
    const out = filterScopes(FIXTURES, "northern");
    expect(out).toHaveLength(1);
    expect(out[0]?.raw).toBe("network:11111111-1111-1111-1111-111111111111");
  });

  it("matches against the raw scope string", () => {
    const out = filterScopes(FIXTURES, "organization:");
    expect(out).toHaveLength(1);
    expect(out[0]?.kind).toBe("organization");
  });

  it("matches against the id substring", () => {
    const out = filterScopes(FIXTURES, "11111111");
    expect(out).toHaveLength(1);
    expect(out[0]?.kind).toBe("network");
  });

  it("returns an empty array on no match", () => {
    expect(filterScopes(FIXTURES, "no-such-scope-anywhere")).toHaveLength(0);
  });
});

describe("MobileScopeSheet", () => {
  it("renders nothing when closed", () => {
    const html = renderToStaticMarkup(
      <MobileScopeSheet
        open={false}
        onClose={() => undefined}
        scopes={FIXTURES}
        activeRaw={null}
        onSelect={() => undefined}
      />,
    );
    expect(html).toBe("");
  });

  it("renders a full-screen radiogroup when open", () => {
    const html = renderToStaticMarkup(
      <MobileScopeSheet
        open={true}
        onClose={() => undefined}
        scopes={FIXTURES}
        activeRaw="global"
        onSelect={() => undefined}
      />,
    );
    expect(html).toContain('data-layout="mobile"');
    expect(html).toContain('role="dialog"');
    expect(html).toContain('role="radiogroup"');
    expect(html).toContain('type="search"');
    // One radio per scope.
    expect(html.match(/role="radio"/g) ?? []).toHaveLength(FIXTURES.length);
    // Active scope marked.
    expect(html).toContain('aria-checked="true"');
    // Back button present.
    expect(html).toContain("←");
  });

  it("renders the chip label for each scope", () => {
    const html = renderToStaticMarkup(
      <MobileScopeSheet
        open={true}
        onClose={() => undefined}
        scopes={FIXTURES}
        activeRaw={null}
        onSelect={() => undefined}
      />,
    );
    expect(html).toContain("Global (all tenants)");
    expect(html).toContain("Platform operations");
    expect(html).toContain("Northern Arena");
  });

  it("respects customisation props", () => {
    const html = renderToStaticMarkup(
      <MobileScopeSheet
        open={true}
        onClose={() => undefined}
        scopes={FIXTURES}
        activeRaw={null}
        onSelect={() => undefined}
        title="Choose context"
        backLabel="Done"
        searchPlaceholder="Filter contexts"
      />,
    );
    expect(html).toContain("Choose context");
    expect(html).toContain('aria-label="Done"');
    expect(html).toContain('placeholder="Filter contexts"');
    expect(html).toContain('aria-label="Filter contexts"');
  });

  it("renders an empty state when no scopes are available", () => {
    // Render with all scopes -- no empty branch on initial paint because
    // the search field is blank. The empty branch is exercised when the
    // operator types a query and filterScopes returns []. Cover that
    // pure path via filterScopes() instead -- the static markup test
    // here exists to pin the visible structure.
    const html = renderToStaticMarkup(
      <MobileScopeSheet
        open={true}
        onClose={() => undefined}
        scopes={[]}
        activeRaw={null}
        onSelect={() => undefined}
      />,
    );
    // With zero scopes and an empty query, filterScopes returns []. The
    // empty state copy is rendered.
    expect(html).toContain("No scopes match");
  });

  it("renders unique stable testids per option", () => {
    const html = renderToStaticMarkup(
      <MobileScopeSheet
        open={true}
        onClose={() => undefined}
        scopes={FIXTURES}
        activeRaw="global"
        onSelect={() => undefined}
        id="scope-picker"
      />,
    );
    expect(html).toContain('data-testid="scope-picker-option-global"');
    expect(html).toContain('data-testid="scope-picker-option-platform"');
    expect(html).toContain('data-testid="scope-picker-search"');
    expect(html).toContain('data-testid="scope-picker-back"');
  });
});
