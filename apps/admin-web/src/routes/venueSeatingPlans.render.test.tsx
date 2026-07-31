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
import {
  CreatePlanFormView,
  UploadSVGFormView,
  createPlanFormIssues,
  type OrganizationSummary,
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
      issues={[]}
      submitting={false}
      apiError={null}
      onNameChange={NOOP}
      onPlanTypeChange={NOOP}
      onOwnerOrgChange={NOOP}
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
      capacityStanding=""
      pending={false}
      step={null}
      okMessage={null}
      fileError={null}
      issues={[]}
      warnings={[]}
      uploadError={null}
      onCapacityStandingChange={NOOP}
      onFileSelected={NOOP}
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

  it("renders the standing-capacity input carried into the version", () => {
    const html = uploadMarkup({ capacityStanding: "120" });
    expect(html).toContain('data-testid="venues-plan-upload-standing-plan-1"');
    expect(html).toContain('value="120"');
  });

  it("explains that seated capacity is derived, not entered", () => {
    expect(uploadMarkup()).toContain("derived from the SVG");
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
