/**
 * Render tests for the venue seating-plans drawer (AB-25).
 *
 * The admin-web Vitest environment is Node-only (no jsdom), so — following
 * the MobileScopeSheet / layout precedent — the presentational halves of the
 * drawer are rendered with renderToStaticMarkup. Every component exercised
 * here is deliberately state- and query-free so it can be rendered directly.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  CreatePlanFormView,
  GACategoryEditorView,
  UploadSVGFormView,
  VersionHistoryTable,
  VersionPreview,
  createPlanFormIssues,
  emptyGADraft,
  type OrganizationSummary,
  type SeatingPlanVersion,
} from "@/routes/venueSeatingPlans";

const ORGS: readonly OrganizationSummary[] = [
  { id: "org-1", name: "Northern Arena" },
  { id: "org-2", name: "Southern Hall" },
];

const NOOP = (): void => undefined;

function createFormMarkup(
  overrides: Partial<Parameters<typeof CreatePlanFormView>[0]> = {},
): string {
  return renderToStaticMarkup(
    <CreatePlanFormView
      name=""
      planType="assigned_seats"
      ownerOrgID=""
      organizations={ORGS}
      organizationsPending={false}
      gaDrafts={[emptyGADraft]}
      issues={[]}
      submitting={false}
      apiError={null}
      onNameChange={NOOP}
      onPlanTypeChange={NOOP}
      onOwnerOrgChange={NOOP}
      onGADraftsChange={NOOP}
      onSubmit={NOOP}
      {...overrides}
    />,
  );
}

/** Isolates the submit button's opening tag so `disabled` can be asserted. */
function submitButtonTag(html: string): string {
  const start = html.indexOf("<button");
  const end = html.indexOf(">", start);
  return html.slice(start, end + 1);
}

// ---------------------------------------------------------------------------
// AB-25a — create-plan validation display
// ---------------------------------------------------------------------------

describe("CreatePlanFormView validation display (AB-25a)", () => {
  it("keeps the submit button enabled when the form is incomplete", () => {
    // The defect AB-25a fixes: a disabled button that never says why.
    const tag = submitButtonTag(createFormMarkup({ issues: createPlanFormIssues("", "") }));
    expect(tag).toContain('data-testid="venues-plan-create-submit"');
    expect(tag).not.toContain("disabled");
  });

  it("renders one message per unmet requirement", () => {
    const html = createFormMarkup({ issues: createPlanFormIssues("", "") });
    expect(html).toContain("Plan name is required.");
    expect(html).toContain("Owner organization is required.");
    expect(html).toContain('data-testid="venues-plan-create-validation"');
    expect(html).toContain('role="alert"');
  });

  it("attaches each message to the offending field", () => {
    const html = createFormMarkup({ issues: createPlanFormIssues("", "") });
    expect(html).toContain('data-testid="venues-plan-create-name-error"');
    expect(html).toContain('data-testid="venues-plan-create-owner-error"');
    expect(html).toContain('aria-describedby="venue-plan-name-error"');
    expect(html).toContain('aria-describedby="venue-plan-owner-error"');
    expect(html).toContain('aria-invalid="true"');
  });

  it("shows only the remaining requirement once one is satisfied", () => {
    const html = createFormMarkup({
      name: "Main Hall",
      issues: createPlanFormIssues("Main Hall", ""),
    });
    expect(html).not.toContain("Plan name is required.");
    expect(html).toContain("Owner organization is required.");
    expect(html).not.toContain('data-testid="venues-plan-create-name-error"');
  });

  it("renders no validation feedback before the first submit attempt", () => {
    const html = createFormMarkup({ issues: [] });
    expect(html).not.toContain('data-testid="venues-plan-create-validation"');
    expect(html).not.toContain("is required.");
    expect(html).not.toContain('aria-invalid="true"');
  });

  it("disables submit only while a create request is in flight", () => {
    const html = createFormMarkup({
      name: "Main Hall",
      ownerOrgID: "org-1",
      submitting: true,
    });
    expect(html).toContain("Creating…");
    expect(submitButtonTag(html)).toContain("disabled");
  });
});

// ---------------------------------------------------------------------------
// AB-25b — upload SVG + create version control
// ---------------------------------------------------------------------------

function uploadMarkup(
  overrides: Partial<Parameters<typeof UploadSVGFormView>[0]> = {},
): string {
  return renderToStaticMarkup(
    <UploadSVGFormView
      planID="plan-1"
      pending={false}
      step={null}
      okMessage={null}
      fileError={null}
      issues={[]}
      warnings={[]}
      uploadError={null}
      onFileSelected={NOOP}
      gaHint={true}
      {...overrides}
    />,
  );
}

describe("UploadSVGFormView (AB-25b)", () => {
  it("accepts SVG files only and states the size ceiling", () => {
    const html = uploadMarkup();
    expect(html).toContain('accept="image/svg+xml,.svg"');
    expect(html).toContain('data-testid="venues-plan-upload-input-plan-1"');
    expect(html).toContain("2.00 MiB");
  });

  it("shows the #GA authoring hint instead of a capacity input (AB-40)", () => {
    const html = uploadMarkup();
    expect(html).toContain('data-testid="venues-plan-upload-ga-plan-1"');
    expect(html).toContain("#GA");
    expect(html).not.toContain("GA capacity (optional)");
  });

  it("explains that capacities are derived, not entered", () => {
    expect(uploadMarkup()).toContain("derives seated");
  });

  it("shows the two-step progress label while uploading", () => {
    const html = uploadMarkup({ pending: true, step: "Creating version (2/2)…" });
    expect(html).toContain("Creating version (2/2)…");
    expect(html).toContain("disabled");
  });

  it("reports a client-side file rejection", () => {
    const html = uploadMarkup({
      fileError: "Unsupported file type image/png. Allowed: svg.",
    });
    expect(html).toContain('data-testid="venues-plan-upload-file-error-plan-1"');
    expect(html).toContain("Unsupported file type image/png");
    expect(html).toContain('role="alert"');
  });

  it("lists per-element importer errors returned by the 422 envelope", () => {
    const html = uploadMarkup({
      issues: [
        { code: "seat.missing_number", element: "circle#s12", detail: "no data-number" },
      ],
    });
    expect(html).toContain('data-testid="venues-plan-upload-errors-plan-1"');
    expect(html).toContain("seat.missing_number");
    expect(html).toContain("circle#s12");
    expect(html).toContain("no data-number");
  });

  it("renders importer warnings separately from errors", () => {
    const html = uploadMarkup({
      warnings: [{ code: "legend.missing", detail: "no Legend group found" }],
    });
    expect(html).toContain('data-testid="venues-plan-upload-warnings-plan-1"');
    expect(html).toContain("legend.missing");
    expect(html).not.toContain('data-testid="venues-plan-upload-errors-plan-1"');
  });

  it("confirms the created version number and that it became current", () => {
    const html = uploadMarkup({
      okMessage: "Uploaded as version 3 (450 seats). Version 3 is now current.",
    });
    expect(html).toContain('data-testid="venues-plan-upload-ok-plan-1"');
    expect(html).toContain("version 3");
    expect(html).toContain("now current");
  });
});

// ---------------------------------------------------------------------------
// AB-25c — version history table + preview
// ---------------------------------------------------------------------------

function version(
  overrides: Partial<SeatingPlanVersion> & { id: string; version_number: number },
): SeatingPlanVersion {
  return {
    seating_plan_id: "plan-1",
    geometry: {},
    geometry_checksum: "abc123",
    svg_asset_media_id: null,
    capacity_seated: 100,
    capacity_standing: 0,
    locked_at: null,
    created_at: "2026-07-30T10:00:00Z",
    ...overrides,
  };
}

const VERSIONS: readonly SeatingPlanVersion[] = [
  version({
    id: "v3",
    version_number: 3,
    capacity_seated: 450,
    capacity_standing: 120,
    created_at: "2026-07-30T10:00:00Z",
  }),
  version({
    id: "v2",
    version_number: 2,
    capacity_seated: 300,
    locked_at: "2026-07-29T09:00:00Z",
    created_at: "2026-07-29T08:00:00Z",
  }),
  version({
    id: "v1",
    version_number: 1,
    capacity_seated: 100,
    created_at: "2026-07-28T08:00:00Z",
  }),
];

function historyMarkup(
  overrides: Partial<Parameters<typeof VersionHistoryTable>[0]> = {},
): string {
  return renderToStaticMarkup(
    <VersionHistoryTable
      versions={VERSIONS}
      currentVersionID="v3"
      selectedVersionID="v3"
      onSelect={NOOP}
      hasGA={true}
      {...overrides}
    />,
  );
}

describe("VersionHistoryTable (AB-25c)", () => {
  it("renders one selectable row per version", () => {
    const html = historyMarkup();
    expect(html).toContain('data-testid="venues-plan-version-row-v3"');
    expect(html).toContain('data-testid="venues-plan-version-row-v2"');
    expect(html).toContain('data-testid="venues-plan-version-row-v1"');
  });

  it("orders rows by version_number descending regardless of transport order", () => {
    // Deliberately shuffled input: the table must not trust the array order.
    const html = historyMarkup({
      versions: [VERSIONS[1]!, VERSIONS[2]!, VERSIONS[0]!],
    });
    const idxV3 = html.indexOf("venues-plan-version-row-v3");
    const idxV2 = html.indexOf("venues-plan-version-row-v2");
    const idxV1 = html.indexOf("venues-plan-version-row-v1");
    expect(idxV3).toBeLessThan(idxV2);
    expect(idxV2).toBeLessThan(idxV1);
  });

  it("renders both capacities and the creation timestamp", () => {
    const html = historyMarkup();
    expect(html).toContain("450");
    expect(html).toContain("120");
    expect(html).toContain("2026-07-30 10:00 UTC");
    expect(html).toContain("2026-07-28 08:00 UTC");
    // The machine-readable stamp rides along on <time>. HTML attribute names
    // are case-insensitive and React emits the JSX spelling here, so match
    // without pinning the casing.
    expect(html.toLowerCase()).toContain('datetime="2026-07-30t10:00:00z"');
  });

  it("marks the plan's current version", () => {
    const html = historyMarkup();
    expect(html).toContain('data-testid="venues-plan-version-current-v3"');
    expect(html).toContain("current");
    expect(html).not.toContain('data-testid="venues-plan-version-current-v1"');
  });

  it("marks locked versions only", () => {
    const html = historyMarkup();
    expect(html).toContain('data-testid="venues-plan-version-locked-v2"');
    expect(html).toContain("locked");
    expect(html).not.toContain('data-testid="venues-plan-version-locked-v3"');
    expect(html).not.toContain('data-testid="venues-plan-version-locked-v1"');
  });

  it("marks the row currently targeted by the preview", () => {
    const html = historyMarkup({ selectedVersionID: "v2" });
    expect(html).toContain('data-testid="venues-plan-version-selected-v2"');
    expect(html).toContain('aria-pressed="true"');
    expect(html).not.toContain('data-testid="venues-plan-version-selected-v3"');
  });

  it("labels each selector for screen readers", () => {
    expect(historyMarkup()).toContain('aria-label="Preview version 3"');
  });

  it("explains the empty state instead of rendering an empty table", () => {
    const html = historyMarkup({
      versions: [],
      currentVersionID: null,
      selectedVersionID: null,
    });
    expect(html).toContain('data-testid="venues-plan-versions-empty"');
    expect(html).toContain("Upload an SVG");
    expect(html).not.toContain("<table");
  });
});

describe("VersionPreview (AB-25c)", () => {
  it("renders the stored SVG asset as an <img> when a signed URL is available", () => {
    const html = renderToStaticMarkup(
      <VersionPreview
        version={version({ id: "v3", version_number: 3, svg_asset_media_id: "media-1" })}
        signedURL="http://test.invalid/v1/media-files/media-1?sig=x"
        loading={false}
      />,
    );
    expect(html).toContain('data-testid="venues-plan-preview-svg-img"');
    expect(html).toContain("http://test.invalid/v1/media-files/media-1?sig=x");
    expect(html).toContain("<img");
  });

  it("falls back to the client-side geometry renderer with no stored asset", () => {
    const html = renderToStaticMarkup(
      <VersionPreview
        version={version({
          id: "v1",
          version_number: 1,
          geometry: {
            canvas: { width: 100, height: 80 },
            categories: [{ index: 0, name: "Stalls", color: "#ff0000" }],
            sections: [
              {
                key: "A",
                name: "A",
                rows: [
                  {
                    key: "1",
                    name: "1",
                    seats: [
                      {
                        key: "A|1|1",
                        number: "1",
                        x: 10,
                        y: 10,
                        radius: 4,
                        category_index: 0,
                      },
                    ],
                  },
                ],
              },
            ],
          },
        })}
        signedURL={null}
        loading={false}
      />,
    );
    expect(html).toContain('data-testid="venues-plan-preview-svg"');
    expect(html).toContain("<circle");
    expect(html).not.toContain('data-testid="venues-plan-preview-svg-img"');
  });

  it("shows a loading state while the signed URL is being fetched", () => {
    const html = renderToStaticMarkup(
      <VersionPreview
        version={version({ id: "v3", version_number: 3, svg_asset_media_id: "media-1" })}
        signedURL={null}
        loading
      />,
    );
    expect(html).toContain("Loading SVG asset…");
    expect(html).not.toContain('data-testid="venues-plan-preview-svg-img"');
  });

  it("degrades to geometry and says why when the asset URL cannot be signed", () => {
    const html = renderToStaticMarkup(
      <VersionPreview
        version={version({ id: "v3", version_number: 3, svg_asset_media_id: "media-1" })}
        signedURL={null}
        loading={false}
        error={new ApiError(404, { code: "media.not_found", message: "gone" })}
      />,
    );
    expect(html).toContain('data-testid="venues-plan-preview-asset-error"');
    expect(html).toContain("media.not_found");
    expect(html).toContain('data-testid="venues-plan-preview-svg"');
  });

  it("labels which version is being previewed and its capacities", () => {
    const html = renderToStaticMarkup(
      <VersionPreview
        version={version({
          id: "v2",
          version_number: 2,
          capacity_seated: 300,
          capacity_standing: 40,
        })}
        signedURL={null}
        loading={false}
        hasGA={true}
      />,
    );
    expect(html).toContain("Version 2");
    expect(html).toContain("300");
    expect(html).toContain("40");
  });
});

// ---------------------------------------------------------------------------
// AB-40 C1 — type-aware create form + GA category editor
// ---------------------------------------------------------------------------

describe("CreatePlanFormView type reshape (AB-40 C1)", () => {
  it("shows the per-type hint", () => {
    expect(createFormMarkup()).toContain("venues-plan-create-type-hint");
  });

  it("renders the GA category editor only for general_admission", () => {
    const ga = createFormMarkup({ planType: "general_admission" });
    expect(ga).toContain('data-testid="venues-plan-create-ga-editor"');
    expect(ga).toContain("no file needed");
    const assigned = createFormMarkup({ planType: "assigned_seats" });
    expect(assigned).not.toContain('data-testid="venues-plan-create-ga-editor"');
  });
});

describe("GACategoryEditorView (AB-40 C1)", () => {
  it("renders No./Category/Seats/Starting price columns with row inputs", () => {
    const html = renderToStaticMarkup(
      <GACategoryEditorView
        drafts={[
          { name: "R1", capacity: "10", price: "1000" },
          { name: "R2", capacity: "20", price: "" },
        ]}
        onChange={NOOP}
      />,
    );
    expect(html).toContain("Starting price");
    expect(html).toContain('data-testid="venues-plan-create-ga-name-0"');
    expect(html).toContain('data-testid="venues-plan-create-ga-capacity-1"');
    expect(html).toContain('value="R1"');
    expect(html).toContain('value="20"');
    expect(html).toContain('data-testid="venues-plan-create-ga-add"');
  });

  it("disables removing the last remaining row", () => {
    const html = renderToStaticMarkup(
      <GACategoryEditorView drafts={[emptyGADraft]} onChange={NOOP} />,
    );
    const btn = html.slice(html.indexOf("venues-plan-create-ga-remove-0") - 200);
    expect(btn).toContain("disabled");
  });
});
