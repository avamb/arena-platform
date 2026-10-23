/**
 * The sales end and doors block (5-3) rendered on the server: the three
 * states the screen can put it in besides the normal editor. Clicks are
 * proven by the live twin (tests/smoke/live/sale-times.spec.ts).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SessionSaleTimes, type SessionSaleTimesProps } from "@/components/SessionSaleTimes";
import { I18nProvider } from "@/lib/i18n";

const base: SessionSaleTimesProps = {
  orgId: "o",
  eventId: "e",
  sessionId: "s",
  startAt: "2026-11-20T19:00:00Z",
  salesEndAt: "2026-11-20T19:00:00Z",
  doorsOpenAt: "2026-11-20T18:30:00Z",
  timezone: "Europe/Madrid",
};

function render(props: Partial<SessionSaleTimesProps>): string {
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <I18nProvider locale="en">
        <SessionSaleTimes {...base} {...props} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("SessionSaleTimes", () => {
  it("offers the buttons for a live session", () => {
    const html = render({});
    expect(html).toContain('data-testid="sales-end-change"');
    expect(html).toContain('data-testid="doors-remove"');
    expect(html).not.toContain("sale-times-readonly");
  });

  it("shows a canceled session read-only, and says why", () => {
    const html = render({ readOnly: true });
    expect(html).toContain('data-testid="sale-times-readonly"');
    expect(html).toContain("canceled");
    expect(html).toContain('data-testid="sales-end-value"');
    expect(html).not.toContain('data-testid="sales-end-change"');
    expect(html).not.toContain('data-testid="doors-change"');
    expect(html).not.toContain('data-testid="doors-remove"');
  });

  it("tells a venue without a zone apart from a summary that did not load", () => {
    const venue = render({ timezone: null });
    expect(venue).toContain("no time zone on file");
    expect(venue).toMatch(/write to support/i);
    expect(venue).not.toContain('data-testid="sales-end-change"');

    const unloaded = render({ timezone: null, zoneUnloaded: true });
    expect(unloaded).toContain("could not load the time zone");
    expect(unloaded).toContain("Reload the page");
    expect(unloaded).toMatch(/write to support/i);
    expect(unloaded).not.toContain("no time zone on file");
  });
});
