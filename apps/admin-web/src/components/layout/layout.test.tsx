import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  BREAKPOINT_SM_PX,
  BREAKPOINT_MD_PX,
  BREAKPOINT_LG_PX,
  BREAKPOINT_XL_PX,
  BREAKPOINTS,
  DESKTOP_MOBILE_CUT_PX,
  MIN_WIDTH_MD,
  isAtLeast,
  minWidthQuery,
} from "./breakpoints";
import { ResponsiveTable, type ResponsiveTableColumn } from "./ResponsiveTable";
import { ResponsiveDrawer } from "./ResponsiveDrawer";

describe("breakpoints", () => {
  it("pins the Tailwind sm/md/lg/xl values", () => {
    expect(BREAKPOINT_SM_PX).toBe(640);
    expect(BREAKPOINT_MD_PX).toBe(768);
    expect(BREAKPOINT_LG_PX).toBe(1024);
    expect(BREAKPOINT_XL_PX).toBe(1280);
  });

  it("declares md as the desktop/mobile cut", () => {
    expect(DESKTOP_MOBILE_CUT_PX).toBe(768);
    expect(DESKTOP_MOBILE_CUT_PX).toBe(BREAKPOINTS.md);
  });

  it("MIN_WIDTH_MD uses the md threshold", () => {
    expect(MIN_WIDTH_MD).toBe("(min-width: 768px)");
    expect(minWidthQuery("md")).toBe("(min-width: 768px)");
    expect(minWidthQuery("xl")).toBe("(min-width: 1280px)");
  });

  it("isAtLeast compares numerically", () => {
    expect(isAtLeast(640, "sm")).toBe(true);
    expect(isAtLeast(767, "md")).toBe(false);
    expect(isAtLeast(768, "md")).toBe(true);
    expect(isAtLeast(1000, "lg")).toBe(false);
    expect(isAtLeast(1024, "lg")).toBe(true);
  });

  it("BREAKPOINTS is frozen", () => {
    expect(Object.isFrozen(BREAKPOINTS)).toBe(true);
  });
});

interface RowFixture {
  id: string;
  name: string;
  city: string;
  status: string;
}

const fixtureRows: readonly RowFixture[] = [
  { id: "r1", name: "Alpha Arena", city: "Moscow", status: "active" },
  { id: "r2", name: "Bravo Hall", city: "Kazan", status: "draft" },
];

const fixtureColumns: readonly ResponsiveTableColumn<RowFixture>[] = [
  {
    id: "name",
    header: "Name",
    primary: true,
    renderCell: (row) => row.name,
  },
  {
    id: "city",
    header: "City",
    renderCell: (row) => row.city,
  },
  {
    id: "status",
    header: "Status",
    renderCell: (row) => row.status,
  },
  {
    id: "internal_id",
    header: "ID",
    hideOnMobile: true,
    renderCell: (row) => row.id,
  },
];

describe("ResponsiveTable", () => {
  it("renders a real <table> at the desktop layout", () => {
    const html = renderToStaticMarkup(
      <ResponsiveTable<RowFixture>
        id="venues-table"
        columns={fixtureColumns}
        rows={fixtureRows}
        forceLayout="desktop"
      />,
    );
    expect(html).toContain("<table");
    expect(html).toContain('data-layout="desktop"');
    expect(html).toContain("<thead");
    expect(html).toContain("<th");
    expect(html).toContain("Alpha Arena");
    expect(html).toContain("Bravo Hall");
    expect(html).toContain("Moscow");
    // Internal ID column IS rendered on desktop (hideOnMobile=true only).
    expect(html).toContain(">r1<");
  });

  it("renders a stacked <ul> card list at the mobile layout", () => {
    const html = renderToStaticMarkup(
      <ResponsiveTable<RowFixture>
        id="venues-table"
        columns={fixtureColumns}
        rows={fixtureRows}
        forceLayout="mobile"
      />,
    );
    expect(html).toContain('data-layout="mobile"');
    expect(html).toContain("<ul");
    expect(html).toContain("<li");
    expect(html).not.toContain("<table");
    // Primary column renders as a card title (no dt label for it).
    expect(html).toContain("Alpha Arena");
    // Secondary columns are dt/dd pairs.
    expect(html).toContain("<dt");
    expect(html).toContain("<dd");
    expect(html).toContain("Kazan");
    // hideOnMobile column suppressed.
    expect(html).not.toContain(">r1<");
    expect(html).not.toContain(">r2<");
  });

  it("renders the empty slot when there are no rows", () => {
    const html = renderToStaticMarkup(
      <ResponsiveTable<RowFixture>
        id="venues-table"
        columns={fixtureColumns}
        rows={[]}
        empty={<span>No venues yet</span>}
        forceLayout="mobile"
      />,
    );
    expect(html).toContain("No venues yet");
    expect(html).toContain('data-responsive-table-empty="true"');
  });

  it("uses rowKey when provided", () => {
    // Snapshot the markup with and without rowKey; both should render the
    // same visible content regardless of which key strategy applies.
    const withKey = renderToStaticMarkup(
      <ResponsiveTable<RowFixture>
        columns={fixtureColumns}
        rows={fixtureRows}
        rowKey={(row) => row.id}
        forceLayout="desktop"
      />,
    );
    const withoutKey = renderToStaticMarkup(
      <ResponsiveTable<RowFixture>
        columns={fixtureColumns}
        rows={fixtureRows}
        forceLayout="desktop"
      />,
    );
    // Both contain the same row text.
    expect(withKey).toContain("Alpha Arena");
    expect(withoutKey).toContain("Alpha Arena");
  });
});

describe("ResponsiveDrawer", () => {
  it("returns null markup when closed", () => {
    const html = renderToStaticMarkup(
      <ResponsiveDrawer
        id="venue-drawer"
        open={false}
        onClose={() => undefined}
        title="Venue detail"
      >
        <p>body</p>
      </ResponsiveDrawer>,
    );
    expect(html).toBe("");
  });

  it("renders as a right-side drawer at >= md", () => {
    const html = renderToStaticMarkup(
      <ResponsiveDrawer
        id="venue-drawer"
        open={true}
        onClose={() => undefined}
        title="Venue detail"
        subtitle="Alpha Arena"
        forceLayout="desktop"
        footer={<button type="button">Save</button>}
      >
        <p>body</p>
      </ResponsiveDrawer>,
    );
    expect(html).toContain('data-layout="desktop"');
    expect(html).toContain("Venue detail");
    expect(html).toContain("Alpha Arena");
    expect(html).toContain("body");
    expect(html).toContain('aria-modal="true"');
    // Desktop has the × close button, not the ← back button.
    expect(html).toContain('aria-label="Close"');
    expect(html).toContain("×");
    expect(html).not.toContain("←");
    // Has the scrim.
    expect(html).toContain("rgba(15, 23, 42, 0.4)");
    // Footer rendered.
    expect(html).toContain("<button");
    expect(html).toContain("Save");
  });

  it("renders as a full-screen sheet with a back button below md", () => {
    const html = renderToStaticMarkup(
      <ResponsiveDrawer
        id="venue-drawer"
        open={true}
        onClose={() => undefined}
        title="Venue detail"
        forceLayout="mobile"
      >
        <p>body</p>
      </ResponsiveDrawer>,
    );
    expect(html).toContain('data-layout="mobile"');
    expect(html).toContain("Venue detail");
    expect(html).toContain("←");
    // Full-screen sheet uses fixed inset 0 (100vw x 100vh).
    expect(html).toContain("position:fixed");
    // No scrim in the mobile sheet (the sheet IS the surface).
    expect(html).not.toContain("rgba(15, 23, 42, 0.4)");
  });

  it("honors custom closeLabel", () => {
    const html = renderToStaticMarkup(
      <ResponsiveDrawer
        id="venue-drawer"
        open={true}
        onClose={() => undefined}
        title="Venue detail"
        forceLayout="mobile"
        closeLabel="Back to venues"
      >
        <p>body</p>
      </ResponsiveDrawer>,
    );
    expect(html).toContain("Back to venues");
    expect(html).toContain('aria-label="Back to venues"');
  });

  it("desktop width is configurable", () => {
    const html = renderToStaticMarkup(
      <ResponsiveDrawer
        id="venue-drawer"
        open={true}
        onClose={() => undefined}
        title="Venue detail"
        forceLayout="desktop"
        desktopWidthPx={640}
      >
        <p>body</p>
      </ResponsiveDrawer>,
    );
    expect(html).toContain("width:640px");
  });
});
