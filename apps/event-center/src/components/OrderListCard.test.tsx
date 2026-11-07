import { describe, expect, it } from "vitest";
import { renderInRouter } from "@/test-utils/renderInRouter";
import type { OrderListItem, OrderListTicket } from "@/lib/api/types";
import { NO_DATES } from "@/lib/orderCards";
import { OrderDateFilters } from "@/components/OrderDateFilters";
import { OrderListCard } from "@/components/OrderListCard";

function ticket(over: Partial<OrderListTicket> = {}): OrderListTicket {
  return {
    id: "t1",
    system_ticket_id: 1004567,
    status: "active",
    tier_name: "Stalls",
    seat_label: "A / 3 / 12",
    price: 2500,
    delivery_status: "sent",
    recipient_email: "anna@example.com",
    ...over,
  };
}

function order(over: Partial<OrderListItem> = {}): OrderListItem {
  return {
    id: "o1",
    system_id: 1000000512,
    status: "paid",
    currency: "EUR",
    total: 5000,
    buyer_name: "Anna Ruiz",
    buyer_email: "anna@example.com",
    buyer_phone: "+34 600 111 222",
    is_test: false,
    letter_failed: false,
    event_name: "Wine night",
    session_start_at: "2026-12-01T18:00:00Z",
    session_timezone: "Europe/Madrid",
    delivery_state: "sent",
    tickets: [ticket(), ticket({ id: "t2", system_ticket_id: 1004568, delivery_status: "failed" })],
    ...over,
  } as OrderListItem;
}

describe("the expanded order card", () => {
  it("shows the order, the event, the buyer's call and WhatsApp links and every ticket with its letter", async () => {
    const html = await renderInRouter(<OrderListCard orgId="org1" order={order()} />);
    expect(html).toContain("№1000000512");
    expect(html).toContain("Wine night");
    expect(html).toContain('href="tel:+34600111222"');
    expect(html).toContain("https://wa.me/34600111222");
    expect(html.match(/data-testid="order-card-ticket"/g)).toHaveLength(2);
    expect(html).toContain("Stalls · A / 3 / 12");
    expect(html).toContain('data-letter="failed"');
    expect(html).toContain("Not delivered");
    expect(html).toContain('data-testid="order-card-open"');
  });

  it("keeps the Test and letter-failed badges of the old row", async () => {
    const html = await renderInRouter(<OrderListCard orgId="org1" order={order({ is_test: true, letter_failed: true })} />);
    expect(html).toContain("Test");
    expect(html).toContain("Letter not delivered");
  });

  it("renders an order whose tickets are not issued yet, without a buyer phone", async () => {
    const html = await renderInRouter(<OrderListCard orgId="org1" order={order({ tickets: [], buyer_phone: null })} />);
    expect(html).not.toContain('data-testid="order-card-ticket"');
    expect(html).not.toContain("tel:");
  });
});

describe("the date filters", () => {
  it("print both ranges, and the warning only for a backwards one", async () => {
    const plain = await renderInRouter(<OrderDateFilters value={NO_DATES} onChange={() => undefined} backwards={false} />);
    for (const id of ["orders-order-from", "orders-order-to", "orders-session-from", "orders-session-to"]) {
      expect(plain, id).toContain(`data-testid="${id}"`);
    }
    expect(plain).not.toContain("orders-dates-backwards");
    expect(plain).not.toContain("orders-dates-clear");
    const back = await renderInRouter(
      <OrderDateFilters value={{ ...NO_DATES, orderFrom: "2026-12-02", orderTo: "2026-12-01" }} onChange={() => undefined} backwards />,
    );
    expect(back).toContain("orders-dates-backwards");
    expect(back).toContain("orders-dates-clear");
  });
});
