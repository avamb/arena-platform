// @vitest-environment jsdom
/**
 * The wizard's "Hall plan" section: a venue without a plan says so plainly
 * (another venue, or free seating), a venue with plans offers them and "without
 * a plan", a chosen plan asks the price of each category, and an extra price
 * category can be added over a plan drawn with one category. Only the
 * organization's own active plans at their current version are offered.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const ORG = "11111111-1111-4111-8111-111111111111";

// The rights of the person: the test turns single permissions off.
const denied = new Set<string>();
vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({ orgId: ORG, access: { can: (p: string) => !denied.has(p), data: { platform_superadmin: false } } }),
}));
vi.mock("@/lib/api/endpoints", () => ({
  listVenueSeatingPlans: vi.fn(),
  listSeatingPlanVersions: vi.fn(),
  listVenuePlanIntakes: vi.fn(),
  listPlanTemplates: vi.fn(),
  forkSeatingPlan: vi.fn(),
}));

import { forkSeatingPlan, listPlanTemplates, listSeatingPlanVersions, listVenuePlanIntakes, listVenueSeatingPlans } from "@/lib/api/endpoints";
import { WizardSeatingSection } from "@/components/WizardSeatingSection";
import { I18nProvider } from "@/lib/i18n";
import { newDraft, reduceDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
/** The queries of the section run one after the other (plans, then templates): give each hop its turn. */
async function settle() {
  for (let i = 0; i < 3; i += 1) {
    await act(async () => {
      await sleep(20);
    });
  }
}
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement | null;

function churchPlan(owner = ORG, status = "active") {
  return { id: "p1", venue_id: "v1", owner_org_id: owner, name: "Church", status, current_version_id: "ver1", category_name_overrides: {} };
}
const churchVersion = {
  id: "ver1",
  seating_plan_id: "p1",
  version_number: 1,
  geometry: { categories: [{ index: 1, name: "Hall" }], sections: [{ rows: [{ seats: [{ category_index: 1 }, { category_index: 1 }] }] }] },
};

let latest: WizardDraft;

function Harness() {
  const [draft, setDraft] = useState<WizardDraft>(() =>
    reduceDraft(newDraft("dr1"), { type: "venueExisting", venue: { id: "v1", name: "Iglesia", timezone: "Europe/Madrid", capacity: 2, country: "ES" } }),
  );
  latest = draft;
  const dispatch = (a: DraftAction) => setDraft((d) => reduceDraft(d, a));
  return <WizardSeatingSection draft={draft} dispatch={dispatch} touch={() => undefined} errorOf={() => undefined} />;
}

async function mount(): Promise<HTMLElement> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const root = createRoot(host);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="ru">
          <Harness />
        </I18nProvider>
      </QueryClientProvider>,
    );
  });
  await act(async () => {
    await sleep(20);
  });
  return host;
}

beforeEach(() => {
  denied.clear();
  vi.mocked(listVenueSeatingPlans).mockReset();
  vi.mocked(listSeatingPlanVersions).mockReset();
  vi.mocked(listSeatingPlanVersions).mockResolvedValue({ seating_plan_versions: [churchVersion] } as never);
  vi.mocked(listVenuePlanIntakes).mockReset();
  vi.mocked(listVenuePlanIntakes).mockResolvedValue({ intakes: [] } as never);
  vi.mocked(listPlanTemplates).mockReset();
  vi.mocked(listPlanTemplates).mockResolvedValue({ plan_templates: [] } as never);
  vi.mocked(forkSeatingPlan).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

describe("WizardSeatingSection", () => {
  it("says plainly that a venue has no plan, and how to sell seats", async () => {
    denied.add("seating_plan.create");
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    const el = await mount();
    const none = byId(el, "wizard-seating-none");
    expect(none?.textContent).toContain("Iglesia");
    expect(none?.textContent).toContain("нет загруженной схемы");
    expect(none?.textContent).toContain("другую площадку");
    expect(byId(el, "wizard-seating-plan-none")).toBeNull();
    // Without seating_plan.create there is no upload.
    expect(byId(el, "wizard-seating-upload")).toBeNull();
  });

  it("offers the upload, and 'Continue without a plan', to a person who may create seating plans", async () => {
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    let release: (v: never) => void = () => undefined;
    vi.mocked(listPlanTemplates).mockReturnValue(new Promise((r) => (release = r as never)) as never);
    const el = await mount();
    // While the templates are read: the loading hint, no upload choice.
    expect(byId(el, "wizard-seating-templates-loading")).not.toBeNull();
    expect(byId(el, "wizard-seating-upload")).toBeNull();
    await act(async () => release({ plan_templates: [] } as never));
    await settle();
    expect(byId(el, "wizard-seating-templates-loading")).toBeNull();
    expect(byId(el, "wizard-seating-none")?.textContent).toContain("Пришлите то, что у вас есть");
    expect(byId(el, "wizard-seating-template")).toBeNull();
    await act(async () => (byId(el, "wizard-seating-upload") as HTMLButtonElement).click());
    expect(byId(el, "wizard-plan-upload")).not.toBeNull();
    expect(byId(el, "wizard-plan-upload-input")).not.toBeNull();
    await act(async () => (byId(el, "wizard-seating-continue") as HTMLButtonElement).click());
    expect(byId(el, "wizard-plan-upload")).toBeNull();
    expect(byId(el, "wizard-seating-none")?.textContent).toContain("Без схемы");
  });

  it("offers a verified template BEFORE any upload, and a copy is selected", async () => {
    const copy = { ...churchPlan(), id: "p9", name: "Church (fork)", current_version_id: "ver9" };
    vi.mocked(listVenueSeatingPlans).mockResolvedValueOnce({ seating_plans: [] } as never).mockResolvedValue({ seating_plans: [copy] } as never);
    vi.mocked(listSeatingPlanVersions).mockResolvedValue({ seating_plan_versions: [{ ...churchVersion, id: "ver9", seating_plan_id: "p9" }] } as never);
    vi.mocked(listPlanTemplates).mockResolvedValue({ plan_templates: [{ plan_id: "t1", name: "Church", own: false, capacity_seated: 2 }] } as never);
    vi.mocked(forkSeatingPlan).mockResolvedValue({ seating_plan: copy } as never);
    const el = await mount();
    await settle();
    expect(byId(el, "wizard-seating-template")?.textContent).toContain("проверил");
    expect(byId(el, "wizard-seating-upload")).toBeNull();
    await act(async () => (byId(el, "wizard-seating-template-use") as HTMLButtonElement).click());
    await act(async () => {
      await sleep(20);
    });
    expect(forkSeatingPlan).toHaveBeenCalledWith("t1", { owner_org_id: ORG, venue_id: "v1" });
    expect(latest.seating.versionId).toBe("ver9");
  });

  it("falls back to the upload choice when the templates cannot be read", async () => {
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    vi.mocked(listPlanTemplates).mockRejectedValue(new Error("down"));
    const el = await mount();
    await settle();
    expect(byId(el, "wizard-seating-templates-loading")).toBeNull();
    expect(byId(el, "wizard-seating-upload")).not.toBeNull();
  });

  it("selects the copy from the answer of the copy request, even when the list does not hold it", async () => {
    const copy = { ...churchPlan(), id: "p9", name: "Church (fork)", current_version_id: "ver9" };
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    vi.mocked(listSeatingPlanVersions).mockResolvedValue({ seating_plan_versions: [{ ...churchVersion, id: "ver9", seating_plan_id: "p9" }] } as never);
    vi.mocked(listPlanTemplates).mockResolvedValue({ plan_templates: [{ plan_id: "t1", name: "Church", own: false, capacity_seated: 2 }] } as never);
    vi.mocked(forkSeatingPlan).mockResolvedValue({ seating_plan: copy } as never);
    const el = await mount();
    await settle();
    await act(async () => (byId(el, "wizard-seating-template-use") as HTMLButtonElement).click());
    await settle();
    expect(latest.seating.versionId).toBe("ver9");
  });

  it("says the copy has no current version, and offers no second copy", async () => {
    const copy = { ...churchPlan(), id: "p9", current_version_id: null };
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    vi.mocked(listPlanTemplates).mockResolvedValue({ plan_templates: [{ plan_id: "t1", name: "Church", own: false, capacity_seated: 2 }] } as never);
    vi.mocked(forkSeatingPlan).mockResolvedValue({ seating_plan: copy } as never);
    const el = await mount();
    await settle();
    await act(async () => (byId(el, "wizard-seating-template-use") as HTMLButtonElement).click());
    await settle();
    expect(byId(el, "wizard-seating-copy-note")?.getAttribute("data-kind")).toBe("noVersion");
    expect(byId(el, "wizard-seating-template-use")).toBeNull();
    expect(forkSeatingPlan).toHaveBeenCalledTimes(1);
  });

  it("says the copy exists when the read after the copy failed, and offers to reload the list", async () => {
    const copy = { ...churchPlan(), id: "p9", current_version_id: "ver9" };
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    vi.mocked(listSeatingPlanVersions).mockRejectedValue(new Error("down"));
    vi.mocked(listPlanTemplates).mockResolvedValue({ plan_templates: [{ plan_id: "t1", name: "Church", own: false, capacity_seated: 2 }] } as never);
    vi.mocked(forkSeatingPlan).mockResolvedValue({ seating_plan: copy } as never);
    const el = await mount();
    await settle();
    await act(async () => (byId(el, "wizard-seating-template-use") as HTMLButtonElement).click());
    await settle();
    expect(byId(el, "wizard-seating-copy-note")?.getAttribute("data-kind")).toBe("listFailed");
    expect(byId(el, "wizard-seating-reload")).not.toBeNull();
    expect(byId(el, "wizard-seating-template-use")).toBeNull();
    expect(forkSeatingPlan).toHaveBeenCalledTimes(1);
  });

  it("lets the person decline the template and upload their own plan", async () => {
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    vi.mocked(listPlanTemplates).mockResolvedValue({ plan_templates: [{ plan_id: "t1", name: "Church", own: false, capacity_seated: 2 }] } as never);
    const el = await mount();
    await settle();
    await act(async () => (byId(el, "wizard-seating-template-decline") as HTMLButtonElement).click());
    expect(byId(el, "wizard-plan-upload")).not.toBeNull();
    expect(forkSeatingPlan).not.toHaveBeenCalled();
  });

  it("offers neither a plan of another organization nor an archived one", async () => {
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [churchPlan("other-org"), { ...churchPlan(), id: "p2", status: "archived" }] } as never);
    const el = await mount();
    expect(byId(el, "wizard-seating-none")).not.toBeNull();
    expect(listSeatingPlanVersions).not.toHaveBeenCalled();
  });

  it("offers the plan, asks its prices and adds an extra category with a colour", async () => {
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [churchPlan()] } as never);
    const el = await mount();
    const pick = byId(el, "wizard-seating-plan-ver1") as HTMLInputElement;
    expect(pick).not.toBeNull();
    expect((byId(el, "wizard-seating-plan-none") as HTMLInputElement).checked).toBe(true);
    await act(async () => pick.click());
    expect(latest.seating.versionId).toBe("ver1");
    expect(latest.seating.seats).toBe(2);
    expect(byId(el, "wizard-seating-one")?.textContent).toContain("одна категория");
    expect(byId(el, "wizard-seating-cat-1-price")).not.toBeNull();

    await act(async () => (byId(el, "wizard-seating-extra-add") as HTMLButtonElement).click());
    expect(latest.seating.extras).toHaveLength(1);
    expect(byId(el, "wizard-seating-one")).toBeNull();
    const green = byId(el, "wizard-seating-extra-0-color-16a34a") as HTMLInputElement;
    await act(async () => green.click());
    expect(latest.seating.extras[0].color).toBe("#16a34a");

    await act(async () => (byId(el, "wizard-seating-plan-none") as HTMLInputElement).click());
    expect(latest.seating.versionId).toBe("");
    expect(byId(el, "wizard-seating-categories")).toBeNull();
  });
});
