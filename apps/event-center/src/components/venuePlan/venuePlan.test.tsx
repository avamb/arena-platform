/**
 * The panels of the seating plan intake, rendered to static markup (the test
 * runs in node: no clicks, no effects). The click rules are pure functions with
 * their own tests (lib/venuePlan/*). Here: what each state SHOWS, and which
 * buttons a person with or without seating_plan.create sees.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const ORG = "11111111-1111-4111-8111-111111111111";
const denied = new Set<string>();
vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({ orgId: ORG, orgName: "Org", access: { can: (p: string) => !denied.has(p), data: { platform_superadmin: false } } }),
}));

import { IntakeReview } from "@/components/venuePlan/IntakeReview";
import { IntakeStatus } from "@/components/venuePlan/IntakeStatus";
import { PanZoom } from "@/components/venuePlan/PanZoom";
import { PlanUpload } from "@/components/venuePlan/PlanUpload";
import { PlanPreviews, VenuePlanIntakeSlot, venueIntakesKey } from "@/components/venuePlan/VenuePlanIntakeSlot";
import { I18nProvider, type Locale } from "@/lib/i18n";
import { SavedPanel } from "@/screens/VenuePlanIntakeScreen";

async function render(node: ReactNode, opts: { locale?: Locale; client?: QueryClient } = {}): Promise<string> {
  const root = createRootRoute({ component: () => <>{node}</> });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  await router.load();
  return renderToStaticMarkup(
    <QueryClientProvider client={opts.client ?? new QueryClient()}>
      <I18nProvider locale={opts.locale ?? "en"}>
        <RouterProvider router={router} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

function intake(over: Record<string, unknown> = {}) {
  return {
    id: "in1",
    org_id: ORG,
    venue_id: "v1",
    venue_name: "Hall One",
    state: "review",
    channel: "event_center_web",
    escalation_reason: null,
    escalation_text: null,
    return_note: null,
    reject_reason: null,
    operator_claimed: false,
    current_draft: 1,
    plan_id: null,
    plan_version: null,
    created_at: "2026-11-12T10:00:00Z",
    updated_at: "2026-11-12T10:00:00Z",
    closed_at: null,
    sources: [
      { id: "s1", kind: "pdf", file_name: "hall.pdf", byte_size: 2048, pages: 1, width: null, height: null, needs_conversion: false, download_path: "/x", png_copy_path: null },
      { id: "s2", kind: "jpeg", file_name: "balcony.jpg", byte_size: 4096, pages: null, width: 10, height: 10, needs_conversion: false, download_path: "/y", png_copy_path: null },
    ],
    draft: { n: 1, author: "source_svg", note: null, summary: { seats: 120, rows: 10, sections: 2, plan_type: "assigned_seats", warnings: ["legend_missing"] }, svg_path: "/z", questions: [], created_at: "2026-11-12T10:00:00Z" },
    drafts: [{ n: 1, author: "source_svg", created_at: "2026-11-12T10:00:00Z" }],
    review_rounds: 1,
    ...over,
  } as never;
}

const has = (html: string, testId: string) => html.includes(`data-testid="${testId}"`);

beforeEach(() => denied.clear());

describe("IntakeStatus", () => {
  it("promises up to 5 minutes while the generator works", async () => {
    const html = await render(<IntakeStatus intake={intake({ state: "generating" })} />);
    expect(has(html, "intake-waiting-generator")).toBe(true);
    expect(html).toContain("up to 5 minutes");
  });

  it("promises one working day while an administrator works, in the words of the reason", async () => {
    const auto = await render(<IntakeStatus intake={intake({ state: "operator", escalation_reason: "no_generator" })} />);
    expect(has(auto, "intake-waiting-operator")).toBe(true);
    expect(auto).toContain("We did not make the plan automatically");
    expect(auto).toContain("one business day");
    const asked = await render(<IntakeStatus intake={intake({ state: "operator", escalation_reason: "organizer_request" })} />);
    expect(asked).toContain("An administrator got your message");
  });

  it("says why a plan was rejected and how to go on", async () => {
    const html = await render(<IntakeStatus intake={intake({ state: "rejected", reject_reason: "The file shows no seats" })} />);
    expect(html).toContain("The file shows no seats");
    expect(html).toContain("Send a different file");
    const none = await render(<IntakeStatus intake={intake({ state: "rejected" })} />);
    expect(none).not.toContain("Cause:");
  });

  it("says that the upload was canceled", async () => {
    expect(await render(<IntakeStatus intake={intake({ state: "cancelled" })} />)).toContain("The upload is canceled");
  });

  it("speaks Russian and Spanish", async () => {
    expect(await render(<IntakeStatus intake={intake({ state: "operator", escalation_reason: "no_generator" })} />, { locale: "ru" })).toContain("Администратор строит схему зала");
    expect(await render(<IntakeStatus intake={intake({ state: "generating" })} />, { locale: "es" })).toContain("Estamos construyendo el plano de sala");
  });
});

describe("PlanUpload", () => {
  it("shows the picker, the limits, the note with the terms link and a send button that waits for a file", async () => {
    const html = await render(<PlanUpload orgId={ORG} venueId="v1" onSent={() => undefined} />);
    expect(html).toContain('type="file"');
    expect(html).toContain("up to 10 files, 80 MB in total");
    expect(html).toContain("to make our generator better");
    expect(html).toMatch(/href="https:\/\/arenasoldout\.com\/legal\/terms"/);
    expect(html).toMatch(/data-testid="plan-upload-send"[^>]*disabled|disabled=""[^>]*data-testid="plan-upload-send"/);
    expect(has(html, "plan-upload-none")).toBe(true);
  });
});

describe("PanZoom", () => {
  it("has the zoom buttons and the hint", async () => {
    const html = await render(<PanZoom>{null}</PanZoom>);
    for (const id of ["plan-zoom-in", "plan-zoom-out", "plan-zoom-reset", "plan-zoom-box"]) expect(has(html, id)).toBe(true);
    expect(html).toContain("Use the mouse wheel to zoom");
  });
});

describe("IntakeReview", () => {
  it("shows the counts, the warnings and the sources, and every button to a person with seating_plan.create", async () => {
    const html = await render(<IntakeReview orgId={ORG} intake={intake()} canCreate onChanged={() => undefined} />);
    expect(html).toContain("120 seats, 10 rows");
    expect(html).toMatch(/data-testid="intake-stat-sections">2</);
    expect(html).toContain("The plan has no legend of categories");
    expect(has(html, "intake-source-s1")).toBe(true);
    expect(has(html, "intake-source-s2")).toBe(true);
    for (const id of ["intake-approve", "intake-fix", "intake-escalate", "intake-cancel", "intake-fix-text"]) expect(has(html, id), id).toBe(true);
    expect(has(html, "intake-read-only")).toBe(false);
    expect(has(html, "intake-many-rounds")).toBe(false);
  });

  it("shows NO button to a person who can only read", async () => {
    const html = await render(<IntakeReview orgId={ORG} intake={intake()} canCreate={false} onChanged={() => undefined} />);
    for (const id of ["intake-approve", "intake-fix", "intake-escalate", "intake-cancel", "intake-fix-text"]) expect(has(html, id), id).toBe(false);
    expect(has(html, "intake-read-only")).toBe(true);
  });

  it("suggests an administrator after the fifth draft", async () => {
    const html = await render(<IntakeReview orgId={ORG} intake={intake({ review_rounds: 5 })} canCreate onChanged={() => undefined} />);
    expect(has(html, "intake-many-rounds")).toBe(true);
  });

  it("shows the note of an administrator who returned the draft", async () => {
    const html = await render(<IntakeReview orgId={ORG} intake={intake({ return_note: "Row 3 fixed." })} canCreate onChanged={() => undefined} />);
    expect(html).toContain("An administrator corrected the seating plan of «Hall One»");
    expect(html).toContain("Row 3 fixed.");
  });

  it("asks to reload when the draft is missing", async () => {
    const html = await render(<IntakeReview orgId={ORG} intake={intake({ draft: null })} canCreate onChanged={() => undefined} />);
    expect(has(html, "intake-no-draft")).toBe(true);
  });
});

describe("SavedPanel", () => {
  it("offers 'Divide seats by price' for the date the page was opened from", async () => {
    const html = await render(<SavedPanel orgId={ORG} intake={intake({ state: "approved" })} search={{ event: "e1", session: "s1" }} canSplit />);
    expect(html).toContain("We saved the seating plan: 120 seats");
    expect(html).toContain(`href="/o/${ORG}/events/e1/sessions/s1/seats?tab=layout"`);
    expect(html).toContain("Divide seats by price");
    expect(has(html, "intake-saved-no-event")).toBe(false);
  });

  it("without a date says that prices are split after the event is created", async () => {
    const html = await render(<SavedPanel orgId={ORG} intake={intake({ state: "approved" })} search={{}} canSplit />);
    expect(has(html, "intake-split")).toBe(false);
    expect(html).toContain("Add an event at this venue first");
  });
});

describe("the venue card slot", () => {
  function seeded(intakes: unknown[]): QueryClient {
    const client = new QueryClient();
    client.setQueryData(venueIntakesKey(ORG, "v1"), { intakes });
    return client;
  }
  const item = (over: Record<string, unknown>) => ({ id: "in1", org_id: ORG, venue_id: "v1", venue_name: "Hall One", state: "operator", ...over });

  it("shows the running intake with a link to its page", async () => {
    const html = await render(<VenuePlanIntakeSlot orgId={ORG} venueId="v1" venueName="Hall One" />, { client: seeded([item({}), item({ id: "old", state: "cancelled" })]) });
    expect(has(html, "venue-plan-slot-running")).toBe(true);
    expect(html).toContain("With an administrator");
    expect(html).toContain(`href="/o/${ORG}/venues/v1/plan-intakes/in1"`);
    expect(has(html, "venue-plan-upload-button")).toBe(true);
  });

  it("says that nothing is running", async () => {
    const html = await render(<VenuePlanIntakeSlot orgId={ORG} venueId="v1" venueName="Hall One" />, { client: seeded([item({ state: "approved" })]) });
    expect(has(html, "venue-plan-slot-none")).toBe(true);
  });

  it("has no upload button without seating_plan.create", async () => {
    denied.add("seating_plan.create");
    const html = await render(<VenuePlanIntakeSlot orgId={ORG} venueId="v1" venueName="Hall One" />, { client: seeded([]) });
    expect(has(html, "venue-plan-upload-button")).toBe(false);
    expect(has(html, "venue-plan-slot")).toBe(true);
  });

  it("shows nothing to a person with neither right", async () => {
    denied.add("seating_plan.create");
    denied.add("seating_plan.read");
    const html = await render(<VenuePlanIntakeSlot orgId={ORG} venueId="v1" venueName="Hall One" />);
    expect(has(html, "venue-plan-slot")).toBe(false);
  });

  it("draws the previews of the plans as SVG elements", async () => {
    const preview = { viewBox: { x: 0, y: 0, w: 10, h: 10 }, dots: [{ x: 1, y: 1 }, { x: 2, y: 2 }], radius: 0.5 };
    const html = await render(<PlanPreviews previews={[{ id: "p1", name: "Main", preview }]} />);
    expect(html).toContain("<circle");
    expect(html).toContain('aria-label="Preview of «Main»"');
  });
});
