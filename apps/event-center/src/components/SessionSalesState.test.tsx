/**
 * The sales state block (migration 0183) rendered on the server from a
 * state already in the query cache: what each person sees. Presses and the
 * dialog are proven by the live twin (tests/smoke/live/sales-state.spec.ts).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SessionSalesState, type SessionSalesStateProps } from "@/components/SessionSalesState";
import type { SessionSalesState as SalesStateResponse } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";

const props: SessionSalesStateProps = {
  orgId: "o",
  eventId: "e",
  sessionId: "s",
  timezone: "Europe/Madrid",
  canUpdate: true,
};

const open: SalesStateResponse = {
  session_id: "s",
  event_id: "e",
  manual: null,
  effective: "open",
  total_places: 5,
  free_places: 4,
  held_places: 1,
  sales_end_at: "2026-11-20T19:00:00Z",
  changed_at: null,
  changed_by: null,
  site_sync: { status: "none", sites: 0, attempts: 0, last_error: null },
  external_allocations: 0,
  org_sales_chat: true,
} as SalesStateResponse;

function render(data: SalesStateResponse, over: Partial<SessionSalesStateProps> = {}, locale: "en" | "ru" | "es" = "en"): string {
  const client = new QueryClient();
  client.setQueryData(["salesState", "o", "s"], data);
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <I18nProvider locale={locale}>
        <SessionSalesState {...props} {...over} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("SessionSalesState", () => {
  it("shows an open session with the two other choices", () => {
    const html = render(open);
    expect(html).toContain("Set by the platform");
    expect(html).toContain("Free places: 4. In unpaid holds: 1.");
    expect(html).toContain('data-testid="sales-state-set-soon"');
    expect(html).toContain('data-testid="sales-state-set-sold_out"');
    expect(html).not.toContain('data-testid="sales-state-set-open"');
    expect(html).not.toContain('data-testid="sales-state-site"');
    expect(html).not.toContain('data-testid="sales-state-no-chat"');
  });

  it("says that a sell-out goes by e-mail only when the organization has no sales chat", () => {
    expect(render({ ...open, org_sales_chat: false })).toContain('data-testid="sales-state-no-chat"');
    expect(render({ ...open, org_sales_chat: false }, { canUpdate: false })).not.toContain("sales-state-no-chat");
  });

  it("names who set the state by hand and whether the sites heard", () => {
    const html = render({
      ...open,
      manual: "sold_out",
      effective: "sold_out",
      changed_at: "2026-11-01T10:00:00Z",
      changed_by: { type: "user", name: "Ana", role: "organizer", source: "telegram_bot" },
      site_sync: { status: "retrying", sites: 1, attempts: 2, last_error: "timeout" },
      external_allocations: 2,
    });
    expect(html).toContain("Set by hand");
    expect(html).toContain("Ana");
    expect(html).toContain("the Telegram bot");
    expect(html).toContain("attempts: 2");
    expect(html).toContain('data-testid="sales-state-site-check"');
    expect(html).toContain('data-testid="sales-state-external"');
    expect(html).toContain('data-testid="sales-state-set-open"');
  });

  it("gives a person who may not change sessions no button", () => {
    const html = render(open, { canUpdate: false });
    expect(html).toContain('data-testid="sales-state-value"');
    expect(html).not.toContain("sales-state-set-");
  });

  it("shows a canceled session read-only", () => {
    const html = render({ ...open, effective: "cancelled" }, { cancelled: true });
    expect(html).toContain('data-testid="sales-state-cancelled"');
    expect(html).not.toContain("sales-state-set-");
  });

  it("renders in Russian and Spanish without a raw key", () => {
    for (const locale of ["ru", "es"] as const) {
      const html = render({ ...open, manual: "soon", effective: "soon" }, {}, locale);
      expect(html).not.toContain("salesState.");
    }
  });
});
