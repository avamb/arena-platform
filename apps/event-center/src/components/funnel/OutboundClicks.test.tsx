// @vitest-environment jsdom
/** The "Clicks to box offices" block: total, share, three tables, empty hint. */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/endpoints", () => ({
  fetchEventOutboundClicks: vi.fn(),
}));

import { fetchEventOutboundClicks } from "@/lib/api/endpoints";
import type { OutboundClicksResponse } from "@/lib/api/types";
import { OutboundClicks } from "@/components/funnel/OutboundClicks";
import { I18nProvider, type Locale } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const ORG = "11111111-1111-4111-8111-111111111111";
const EVENT = "22222222-2222-4222-8222-222222222222";

function resp(over: Partial<OutboundClicksResponse> = {}): OutboundClicksResponse {
  return {
    event_id: EVENT,
    from: "2026-10-03",
    to: "2026-11-01",
    timezone: "Europe/Madrid",
    total: 12,
    visits: 100,
    share_of_visitors: 0.12,
    by_day: [
      { day: "2026-10-05", clicks: 4 },
      { day: "2026-10-04", clicks: 8 },
    ],
    by_seller: [{ seller: "tickets.example", clicks: 12 }],
    by_source: [
      { utm_source: "", clicks: 3 },
      { utm_source: "ig", clicks: 9 },
    ],
    ...over,
  };
}

beforeEach(() => {
  vi.mocked(fetchEventOutboundClicks).mockReset();
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
          <OutboundClicks orgId={ORG} eventId={EVENT} from="2026-10-03" to="2026-11-01" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(20);
  });
  for (let i = 0; i < 40 && el.querySelector('[data-testid="loading"]') !== null; i++) {
    await act(async () => {
      await sleep(25);
    });
  }
  await act(async () => {
    await sleep(25);
  });
  return el;
}

describe("the outbound clicks block", () => {
  it("shows the total, the share and the three tables", async () => {
    vi.mocked(fetchEventOutboundClicks).mockResolvedValue(resp());
    const el = await mount();
    expect(fetchEventOutboundClicks).toHaveBeenCalledWith(ORG, EVENT, { from: "2026-10-03", to: "2026-11-01" });
    expect(byId(el, "outbound-total").textContent).toContain("12");
    expect(byId(el, "outbound-share").textContent).toContain("12%");
    const days = [...el.querySelectorAll('[data-testid="outbound-days-row"] th')].map((x) => x.textContent);
    expect(days).toEqual(["2026-10-04", "2026-10-05"]);
    expect(byId(el, "outbound-sellers").textContent).toContain("tickets.example");
    const sources = [...el.querySelectorAll('[data-testid="outbound-sources-row"] th')].map((x) => x.textContent);
    expect(sources).toEqual(["ig", "Direct"]);
  });

  it("shows a one-line hint when there are no clicks", async () => {
    vi.mocked(fetchEventOutboundClicks).mockResolvedValue(resp({ total: 0, share_of_visitors: null, by_day: [], by_seller: [], by_source: [] }));
    const el = await mount();
    expect(el.textContent).toContain("No one went to a box office");
    expect(byId(el, "outbound-days")).toBeNull();
  });

  it("speaks Russian", async () => {
    vi.mocked(fetchEventOutboundClicks).mockResolvedValue(resp());
    const el = await mount("ru");
    expect(el.textContent).toContain("Переходы на кассы");
    expect(byId(el, "outbound-sources").textContent).toContain("Прямые");
  });
});
