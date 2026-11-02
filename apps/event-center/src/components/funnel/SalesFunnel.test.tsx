// @vitest-environment jsdom
/**
 * The sales funnel block (spec 50, A7): six steps with counts and drop-off, the
 * sources, the sites with the plugin apart (their funnel starts at the chosen
 * seats), the guide-not-proof note, and the session filter.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/endpoints", () => ({
  fetchEventFunnel: vi.fn(),
}));

import { fetchEventFunnel } from "@/lib/api/endpoints";
import { formatDateTime } from "@/lib/format";
import type { SalesFunnelResponse, SalesFunnelStep } from "@/lib/api/types";
import { SalesFunnel } from "@/components/funnel/SalesFunnel";
import { I18nProvider, type Locale } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const ORG = "11111111-1111-4111-8111-111111111111";
const EVENT = "22222222-2222-4222-8222-222222222222";
const SESSION = "33333333-3333-4333-8333-333333333333";

const step = (name: string, count: number | null, share: number | null = null, suspect = 0): SalesFunnelStep => ({
  step: name,
  count,
  suspect_count: suspect,
  share_of_previous: share,
});

function funnel(over: Partial<SalesFunnelResponse> = {}): SalesFunnelResponse {
  return {
    event_id: EVENT,
    session_id: null,
    from: "2026-10-03",
    to: "2026-11-01",
    timezone: "Europe/Madrid",
    sessions: [{ id: SESSION, start_at: "2026-12-01T23:30:00Z", status: "scheduled", timezone: "Pacific/Auckland" }],
    steps: [
      step("view", 200, null, 12),
      step("session_open", 100, 0.5),
      step("seats_chosen", 40, 0.4),
      step("checkout_start", 20, 0.5),
      step("payment_open", 15, 0.75),
      step("paid", 10, 0.6667),
    ],
    revenue: [{ currency: "EUR", amount_minor: 54000 }],
    sources: [
      { utm_source: "", utm_campaign: "", steps: [step("view", 120)], revenue: [] },
      { utm_source: "ig", utm_campaign: "spring", steps: [step("view", 80), step("paid", 6)], revenue: [{ currency: "EUR", amount_minor: 30000 }] },
    ],
    sales_links: [
      { sales_link_id: "44444444-4444-4444-8444-444444444444", code: "abc2345", label: "Window poster", checkout_start: 4, paid: 3, revenue: [{ currency: "EUR", amount_minor: 9000 }] },
    ],
    gateway: null,
    ...over,
  } as SalesFunnelResponse;
}

beforeEach(() => {
  vi.mocked(fetchEventFunnel).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

async function mount(locale: Locale = "en"): Promise<HTMLElement> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale={locale}>
          <SalesFunnel orgId={ORG} eventId={EVENT} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(20);
  });
  await settle(el);
  return el;
}

/** Waits until the screen stops loading (the queries need a few turns). */
async function settle(el: HTMLElement) {
  for (let i = 0; i < 40; i++) {
    await act(async () => {
      await sleep(25);
    });
    if (el.querySelector('[data-testid="loading"]') === null && byId(el, "funnel-window") !== null) return;
  }
}

describe("the sales funnel block", () => {
  it("shows the six steps with counts, shares and drop-off, and the revenue", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(funnel());
    const el = await mount();
    const steps = [...el.querySelectorAll('[data-testid="funnel-main"] [data-testid="funnel-step"]')];
    expect(steps.map((s) => s.getAttribute("data-step"))).toEqual(["view", "session_open", "seats_chosen", "checkout_start", "payment_open", "paid"]);
    expect(steps.map((s) => s.querySelector('[data-testid="funnel-count"]')?.textContent)).toEqual(["200", "100", "40", "20", "15", "10"]);
    // The second step: half of the first, half dropped.
    expect(steps[1].textContent).toContain("50%");
    expect(steps[1].textContent).toContain("50% dropped off");
    // Bots are named apart.
    expect(steps[0].textContent).toContain("12 more look like bots");
    expect(byId(el, "funnel-revenue").textContent).toContain("540");
  });

  it("says the counts are a guide, not proof", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(funnel());
    const el = await mount();
    expect(byId(el, "sales-funnel").textContent).toContain("A guide, not proof for an advertising account");
  });

  it("lists the sources with the no-campaign visits last, and the sales links", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(funnel());
    const el = await mount();
    const rows = [...el.querySelectorAll('[data-testid="funnel-source"]')].map((r) => r.querySelector("th")?.textContent);
    expect(rows).toEqual(["ig", "Without a campaign"]);
    expect(byId(el, "funnel-link").textContent).toContain("Window poster");
  });

  it("keeps the sites with the plugin apart, starting at the chosen seats", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(
      funnel({
        steps: [step("view", 0), step("session_open", 0), step("seats_chosen", 0), step("checkout_start", 0), step("payment_open", 0), step("paid", 0)],
        sources: [],
        sales_links: [],
        revenue: [],
        gateway: { steps: [step("seats_chosen", 10), step("checkout_start", 4, 0.4), step("paid", 2, 0.5)], revenue: [{ currency: "EUR", amount_minor: 1000 }] },
      }),
    );
    const el = await mount();
    const gw = byId(el, "funnel-gateway");
    expect(gw).not.toBeNull();
    const names = [...gw.querySelectorAll('[data-testid="funnel-step"]')].map((s) => s.getAttribute("data-step"));
    expect(names).toEqual(["seats_chosen", "checkout_start", "paid"]);
    expect(gw.textContent).toContain("starts at the chosen seats");
    // Not "nothing happened": the event has carts from the sites.
    expect(el.textContent).not.toContain("Nothing was counted");
  });

  it("says so when nothing was counted", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(
      funnel({ steps: ["view", "session_open", "seats_chosen", "checkout_start", "payment_open", "paid"].map((s) => step(s, 0)), sources: [], sales_links: [], revenue: [] }),
    );
    const el = await mount();
    expect(el.textContent).toContain("Nothing was counted for this window yet");
    expect(byId(el, "funnel-main")).toBeNull();
  });

  it("asks for one session and shows the view as unavailable", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(funnel());
    const el = await mount();
    vi.mocked(fetchEventFunnel).mockResolvedValue(
      funnel({
        session_id: SESSION,
        steps: [step("view", null), step("session_open", 30), step("seats_chosen", 12, 0.4), step("checkout_start", 5, 0.4167), step("payment_open", 4, 0.8), step("paid", 3, 0.75)],
      }),
    );
    const select = byId(el, "funnel-session") as HTMLSelectElement;
    await act(async () => {
      select.value = SESSION;
      select.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(20);
    });
    await settle(el);
    expect(fetchEventFunnel).toHaveBeenLastCalledWith(ORG, EVENT, { from: "", to: "", sessionId: SESSION });
    const first = el.querySelector('[data-testid="funnel-main"] [data-testid="funnel-count"]');
    expect(first?.textContent).toBe("—");
    expect(el.textContent).toContain("A page view belongs to the whole event");
  });

  it("names each date on the clock of its own venue", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(funnel());
    const el = await mount();
    const option = el.querySelector(`#funnel-session option[value="${SESSION}"]`) as HTMLOptionElement;
    expect(option.textContent).toBe(formatDateTime("2026-12-01T23:30:00Z", "Pacific/Auckland", "en"));
    expect(option.textContent).not.toBe(formatDateTime("2026-12-01T23:30:00Z", "UTC", "en"));
  });

  it("sets the window from a preset", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(funnel());
    const el = await mount();
    await act(async () => {
      byId(el, "funnel-preset-7").click();
      await sleep(20);
    });
    await settle(el);
    const last = vi.mocked(fetchEventFunnel).mock.calls.at(-1)?.[2];
    expect(last?.from).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(last?.to).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    // The days are the venue's: the event's zone is Madrid in this answer.
    expect(last?.to).toBe(new Intl.DateTimeFormat("en-CA", { timeZone: "Europe/Madrid" }).format(new Date()));
    expect((byId(el, "funnel-from") as HTMLInputElement).value).toBe(last?.from);
  });

  it("speaks Russian and Spanish", async () => {
    vi.mocked(fetchEventFunnel).mockResolvedValue(funnel());
    const ru = await mount("ru");
    expect(byId(ru, "funnel-main").textContent).toContain("Выбрали места или билеты");
    const es = await mount("es");
    expect(byId(es, "funnel-main").textContent).toContain("Eligieron asientos o entradas");
  });
});
