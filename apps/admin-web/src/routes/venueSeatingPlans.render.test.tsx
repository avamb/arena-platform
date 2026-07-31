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
