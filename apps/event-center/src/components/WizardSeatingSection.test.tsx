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

vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({ orgId: ORG, access: { can: () => true, data: { platform_superadmin: false } } }),
}));
vi.mock("@/lib/api/endpoints", () => ({
  listVenueSeatingPlans: vi.fn(),
  listSeatingPlanVersions: vi.fn(),
}));

import { listSeatingPlanVersions, listVenueSeatingPlans } from "@/lib/api/endpoints";
import { WizardSeatingSection } from "@/components/WizardSeatingSection";
import { I18nProvider } from "@/lib/i18n";
import { newDraft, reduceDraft, type DraftAction, type WizardDraft } from "@/lib/wizard/draft";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
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
  vi.mocked(listVenueSeatingPlans).mockReset();
  vi.mocked(listSeatingPlanVersions).mockReset();
  vi.mocked(listSeatingPlanVersions).mockResolvedValue({ seating_plan_versions: [churchVersion] } as never);
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

describe("WizardSeatingSection", () => {
  it("says plainly that a venue has no plan, and how to sell seats", async () => {
    vi.mocked(listVenueSeatingPlans).mockResolvedValue({ seating_plans: [] } as never);
    const el = await mount();
    const none = byId(el, "wizard-seating-none");
    expect(none?.textContent).toContain("Iglesia");
    expect(none?.textContent).toContain("нет загруженной схемы");
    expect(none?.textContent).toContain("другую площадку");
    expect(byId(el, "wizard-seating-plan-none")).toBeNull();
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
