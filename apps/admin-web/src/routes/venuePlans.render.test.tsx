/**
 * Render tests for the Seating plans screens (spec 64). The Vitest environment is
 * Node-only, so the router- and query-free parts render with renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { createElement, type ReactNode } from "react";
import { IntakeQueueTable, PlansTabs, QueueFilters, availableTabs, claimedByText, waitingFor } from "@/routes/venuePlans";
import { IntakeCardBody, validateDraftFile, validateRejectReason } from "@/routes/venuePlanDetail";
import { PlanPreviewBox, VerificationTable, validateNeedsFixNote } from "@/routes/venuePlanVerification";
import type { VenuePlanIntake, VenuePlanIntakeListItem, VerificationRow } from "@/lib/api/venuePlans";

const NOW = Date.parse("2026-11-12T12:00:00Z");
const I1 = "11111111-1111-7111-8111-111111111111";
const I2 = "22222222-2222-7222-8222-222222222222";
const ME = "0193f01a-0002-7000-8000-000000000002";
const OTHER = "0193f01a-0003-7000-8000-000000000003";
const SRC = "33333333-3333-7333-8333-333333333333";

function item(patch: Partial<VenuePlanIntakeListItem> = {}): VenuePlanIntakeListItem {
  return {
    id: I1,
    org_id: "aaaaaaaa-1111-7111-8111-111111111111",
    venue_id: "bbbbbbbb-1111-7111-8111-111111111111",
    venue_name: "Grand Theatre",
    state: "operator",
    channel: "telegram_bot",
    escalation_reason: "no_generator",
    operator_claimed: false,
    current_draft: null,
    plan_id: null,
    created_at: "2026-11-12T08:40:00Z",
    updated_at: "2026-11-12T08:40:00Z",
    org_name: "Acme Events",
    venue_city: "Prague",
    operator_id: null,
    operator_claimed_at: null,
    ...patch,
  };
}

const link = (id: string, children: ReactNode) => createElement("a", { href: `/venue-plans/${id}` }, children);

function intake(patch: Partial<VenuePlanIntake> = {}): VenuePlanIntake {
  return {
    id: I1,
    org_id: "aaaaaaaa-1111-7111-8111-111111111111",
    venue_id: "bbbbbbbb-1111-7111-8111-111111111111",
    venue_name: "Grand Theatre",
    state: "operator",
    channel: "telegram_bot",
    escalation_reason: "organizer_request",
    escalation_text: "The stage is on the left",
    return_note: null,
    reject_reason: null,
    operator_claimed: true,
    current_draft: 2,
    plan_id: null,
    plan_version: null,
    created_at: "2026-11-12T08:40:00Z",
    updated_at: "2026-11-12T09:00:00Z",
    closed_at: null,
    sources: [
      { id: SRC, kind: "pdf", file_name: "plan.pdf", byte_size: 2048, pages: 3, width: null, height: null, needs_conversion: false, download_path: "/x", png_copy_path: null },
    ],
    draft: {
      n: 2,
      author: "operator_svg",
      note: "Fixed rows",
      summary: { seats: 480, rows: 20, sections: 3, plan_type: "assigned_seats", warnings: ["legend_missing"] },
      svg_path: "/v1/admin/plan-intakes/x/drafts/2/svg",
      questions: [],
      created_at: "2026-11-12T09:00:00Z",
    },
    drafts: [
      { n: 1, author: "source_svg", created_at: "2026-11-12T08:40:00Z" },
      { n: 2, author: "operator_svg", created_at: "2026-11-12T09:00:00Z" },
    ],
    review_rounds: 2,
    org_name: "Acme Events",
    operator_id: ME,
    operator_claimed_at: "2026-11-12T08:50:00Z",
    corrections: [
      { id: "c1", kind: "organizer_text", draft: 1, text: "Move the stage", status: "pending", created_at: "2026-11-12T08:55:00Z" },
    ],
    ...patch,
  };
}

function body(i: VenuePlanIntake, callerId: string | null = ME, extra: Partial<Parameters<typeof IntakeCardBody>[0]> = {}): string {
  const noop = () => undefined;
  return renderToStaticMarkup(
    <IntakeCardBody
      intake={i}
      callerId={callerId}
      now={NOW}
      draftImageUrl={null}
      draftImageLoading={false}
      draftImageError={null}
      shownDraft={i.current_draft}
      busy={null}
      onShowDraft={noop}
      onClaim={noop}
      onTakeOver={noop}
      onDownload={noop}
      onUpload={noop}
      onReturn={noop}
      onAskApprove={noop}
      onAskReject={noop}
      {...extra}
    />,
  );
}

describe("tabs and filters", () => {
  it("shows each tab only with its permission", () => {
    expect(availableTabs(new Set(["venue_plan_intake.curate"]))).toEqual(["queue"]);
    expect(availableTabs(new Set(["seating_plan.verify"]))).toEqual(["verification"]);
    expect(availableTabs(new Set(["venue_plan_intake.curate", "seating_plan.verify"]))).toEqual(["queue", "verification"]);
    expect(availableTabs(new Set())).toEqual([]);
  });

  it("renders both tab names and marks the active one", () => {
    const html = renderToStaticMarkup(<PlansTabs tabs={["queue", "verification"]} active="verification" onSelect={() => undefined} />);
    expect(html).toContain("Queue");
    expect(html).toContain("Plans to verify");
    expect(html.match(/aria-selected="true"/g)).toHaveLength(1);
    expect(html).toMatch(/aria-selected="true"[^>]*data-testid="venue-plans-tab-verification"/);
  });

  it("renders the state and unclaimed filters", () => {
    const html = renderToStaticMarkup(
      <QueueFilters state="operator" unclaimed={true} onState={() => undefined} onUnclaimed={() => undefined} onReload={() => undefined} />,
    );
    expect(html).toContain("With an administrator");
    expect(html).toMatch(/data-testid="venue-plans-filter-unclaimed"[^>]*checked|checked[^>]*data-testid="venue-plans-filter-unclaimed"/);
  });
});

describe("IntakeQueueTable", () => {
  it("renders organization, venue, state, waiting time and claimed by", () => {
    const rows = [item(), item({ id: I2, operator_claimed: true, operator_id: ME, org_name: "Beta" }), item({ id: "x3", state: "approved", operator_claimed: true, operator_id: OTHER })];
    const html = renderToStaticMarkup(<IntakeQueueTable items={rows} renderCardLink={link} callerId={ME} now={NOW} forceLayout="desktop" />);
    expect(html).toContain("Acme Events");
    expect(html).toContain("Grand Theatre");
    expect(html).toContain("Prague");
    expect(html).toContain("With an administrator");
    expect(html).toMatch(new RegExp(`data-testid="venue-plans-waiting-${I1}">3 h 20 min<`));
    expect(html).toContain(">You<");
    expect(html).toContain(`href="/venue-plans/${I1}"`);
    expect(html).toMatch(/data-testid="venue-plans-waiting-x3">—</);
    expect(html).toContain("Administrator 0193f01a");
  });

  it("renders an empty state and the mobile layout", () => {
    expect(renderToStaticMarkup(<IntakeQueueTable items={[]} renderCardLink={link} callerId={ME} forceLayout="desktop" />)).toContain("No requests for these filters.");
    expect(renderToStaticMarkup(<IntakeQueueTable items={[item()]} renderCardLink={link} callerId={ME} now={NOW} forceLayout="mobile" />)).toContain('data-layout="mobile"');
  });

  it("computes waiting and claimed text", () => {
    expect(waitingFor({ state: "operator", created_at: "2026-11-12T11:00:00Z" }, NOW)).toBe("1 h");
    expect(waitingFor({ state: "rejected", created_at: "2026-11-12T11:00:00Z" }, NOW)).toBe("—");
    expect(claimedByText({ operator_claimed: false, operator_id: null }, ME)).toBe("—");
  });
});

describe("intake card", () => {
  it("shows the facts, files, texts, drafts and the counts of the draft", () => {
    const html = body(intake());
    expect(html).toContain("Acme Events · Grand Theatre");
    expect(html).toContain("plan.pdf");
    expect(html).toContain("2.0 KB");
    expect(html).toContain("3 pages");
    expect(html).toContain("The stage is on the left");
    expect(html).toContain("Move the stage");
    expect(html).toContain("Draft 2 (current)");
    expect(html).toMatch(/<strong>480<\/strong> seats/);
    expect(html).toMatch(/<strong>20<\/strong> rows/);
    expect(html).toMatch(/<strong>3<\/strong> sections/);
    expect(html).toContain("legend_missing");
    expect(html).toContain("Upload an SVG draft");
    expect(html).toContain("Send back to the organizer");
    expect(html).toContain("You, since");
  });

  it("shows the draft as an img from the blob URL and never as markup", () => {
    const html = body(intake(), ME, { draftImageUrl: "blob:http://x/abc" });
    expect(html).toContain('<img src="blob:http://x/abc"');
    expect(html).not.toContain("<svg");
  });

  it("enables the actions for the holder", () => {
    const html = body(intake());
    for (const id of ["venue-plan-approve", "venue-plan-reject", "venue-plan-upload-submit", "venue-plan-return-submit"]) {
      expect(html).toMatch(new RegExp(`<button[^>]*data-testid="${id}"`));
      expect(html).not.toMatch(new RegExp(`disabled=""[^>]*data-testid="${id}"`));
    }
  });

  it("turns the actions off with a reason when another administrator holds the intake", () => {
    const html = body(intake({ operator_id: OTHER }), ME);
    expect(html).toMatch(/disabled=""[^>]*data-testid="venue-plan-approve"/);
    expect(html).toContain("A different administrator holds this request. Use Take control to continue.");
    expect(html).toContain('data-testid="venue-plan-takeover"');
    expect(html).not.toContain('data-testid="venue-plan-claim"');
  });

  it("offers Claim when nobody holds the intake", () => {
    const html = body(intake({ operator_claimed: false, operator_id: null, operator_claimed_at: null }), ME);
    expect(html).toContain('data-testid="venue-plan-claim"');
    expect(html).toContain("No administrator holds this request. Press Claim first.");
    expect(html).toMatch(/disabled=""[^>]*data-testid="venue-plan-approve"/);
  });

  it("turns everything off for a final intake", () => {
    const html = body(intake({ state: "approved", plan_id: "p-1", plan_version: 1 }), ME);
    expect(html).toContain("The request is approved. No change is possible.");
    for (const id of ["venue-plan-approve", "venue-plan-reject", "venue-plan-upload-submit", "venue-plan-return-submit", "venue-plan-claim"]) {
      expect(html).toMatch(new RegExp(`disabled=""[^>]*data-testid="${id}"`));
    }
  });

  it("validates the forms", () => {
    expect(validateRejectReason("  ")).toMatch(/Write the cause/);
    expect(validateRejectReason("Unreadable scan")).toBeNull();
    expect(validateRejectReason("x".repeat(1001))).toMatch(/up to 1000/);
    expect(validateDraftFile(null)).toMatch(/Select/);
    expect(validateDraftFile(new File(["x"], "a.png", { type: "image/png" }))).toMatch(/must be an SVG/);
    expect(validateDraftFile(new File(["x"], "a.svg", { type: "image/svg+xml" }))).toBeNull();
  });
});

describe("verification tab", () => {
  const row = (patch: Partial<VerificationRow> = {}): VerificationRow => ({
    planId: "p1",
    name: "Main floor",
    orgName: "Acme Events",
    venueName: "Grand Theatre",
    venueId: "v1",
    seats: 1200,
    createdAt: "2026-11-01T10:00:00Z",
    state: "unverified",
    note: null,
    currentVersionId: "ver-1",
    ...patch,
  });

  it("renders plan, organization, venue, seats, created and the two buttons", () => {
    const html = renderToStaticMarkup(
      <VerificationTable rows={[row()]} selectedId={null} busyId={null} onPreview={() => undefined} onVerify={() => undefined} onNeedsFix={() => undefined} forceLayout="desktop" />,
    );
    expect(html).toContain("Main floor");
    expect(html).toContain("Acme Events");
    expect(html).toContain("Grand Theatre");
    expect(html).toContain("1,200");
    expect(html).toContain("2026-11-01 10:00Z");
    expect(html).toContain("Not verified");
    expect(html).toMatch(/<button[^>]*data-testid="venue-plans-verify-p1"/);
    expect(html).toMatch(/<button[^>]*data-testid="venue-plans-needsfix-p1"/);
    expect(html).not.toMatch(/disabled=""[^>]*data-testid="venue-plans-verify-p1"/);
  });

  it("shows a plan without a state, an unknown seat count and a note", () => {
    const html = renderToStaticMarkup(
      <VerificationTable
        rows={[row({ state: null, seats: null }), row({ planId: "p2", state: "needs_fix", note: "Row 4 is wrong" })]}
        selectedId={null}
        busyId={null}
        onPreview={() => undefined}
        onVerify={() => undefined}
        onNeedsFix={() => undefined}
        forceLayout="desktop"
      />,
    );
    expect(html).toContain("No status (imported)");
    expect(html).toContain("Row 4 is wrong");
    expect(html).toContain("—");
  });

  it("turns Verify off for a verified plan and while a request runs", () => {
    const html = renderToStaticMarkup(
      <VerificationTable
        rows={[row({ state: "verified" }), row({ planId: "p3" })]}
        selectedId={null}
        busyId="p3"
        onPreview={() => undefined}
        onVerify={() => undefined}
        onNeedsFix={() => undefined}
        forceLayout="desktop"
      />,
    );
    expect(html).toMatch(/disabled=""[^>]*data-testid="venue-plans-verify-p1"/);
    expect(html).toMatch(/disabled=""[^>]*data-testid="venue-plans-verify-p3"/);
  });

  it("renders the preview as an img", () => {
    const html = renderToStaticMarkup(<PlanPreviewBox row={row()} url="https://cdn.example.test/a.svg" loading={false} error={null} />);
    expect(html).toContain('<img src="https://cdn.example.test/a.svg"');
    expect(renderToStaticMarkup(<PlanPreviewBox row={row()} url={null} loading={false} error={null} />)).toContain("no stored SVG");
  });

  it("asks for a note of Needs fix", () => {
    expect(validateNeedsFixNote("")).toMatch(/Write the correction/);
    expect(validateNeedsFixNote("Row 4")).toBeNull();
    expect(validateNeedsFixNote("x".repeat(1001))).toMatch(/up to 1000/);
  });
});
