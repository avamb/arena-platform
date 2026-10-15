import { describe, expect, it } from "vitest";
import type { OrderDetail, OrderLetter } from "@/lib/api/types";
import { CATALOGS, LOCALES, translate } from "@/lib/i18n";
import { hasLetterText, letterLine } from "@/lib/letterText";
import { letterFailed, letterTone, refundLetterStatus, ticketLetterStatus, type LetterState } from "@/lib/letters";

const T0 = "2026-10-12T10:00:00Z";
const T1 = "2026-10-12T10:05:00Z";

function letter(over: Partial<OrderLetter>): OrderLetter {
  return {
    id: null,
    kind: "ticket",
    status: "sent",
    recipient: "anna@example.test",
    ticket_ids: ["t1", "t2"],
    queued_at: null,
    sent_at: T0,
    delivered_at: null,
    proxy_opened_at: null,
    opened_at: null,
    clicked_at: null,
    deferred_at: null,
    failed_at: null,
    failure_event: null,
    failure_reason: null,
    stuck: false,
    ...over,
  } as OrderLetter;
}

const card = (letters: OrderLetter[], extra: Record<string, unknown> = {}) =>
  ({ letters, delivery: [], buyer_email: "anna@example.test", refund_letter: null, ...extra }) as unknown as Pick<
    OrderDetail,
    "letters" | "delivery" | "buyer_email" | "refund_letter"
  >;

describe("ticketLetterStatus", () => {
  it("names the step every ticket's letter reached", () => {
    expect(ticketLetterStatus(card([letter({ status: "sent" })])).state).toBe("sent");
    expect(ticketLetterStatus(card([letter({ status: "delivered", delivered_at: T1 })])).state).toBe("delivered");
    expect(ticketLetterStatus(card([letter({ status: "opened", opened_at: T1 })])).state).toBe("opened");
    // Two letters: the lower step wins.
    const s = ticketLetterStatus(
      card([letter({ status: "opened", opened_at: T1, ticket_ids: ["a"] }), letter({ status: "delivered", delivered_at: T1, ticket_ids: ["b"] })]),
    );
    expect(s.state).toBe("delivered");
    expect(s.done).toBe(2);
    expect(s.total).toBe(2);
  });

  it("reads queued, partly queued and partial", () => {
    expect(ticketLetterStatus(card([letter({ status: "queued", sent_at: null, queued_at: T0 })])).state).toBe("queued");
    expect(
      ticketLetterStatus(card([letter({ status: "sent", ticket_ids: ["a"] }), letter({ status: "queued", sent_at: null, ticket_ids: ["b"] })])).state,
    ).toBe("partial_queued");
    expect(
      ticketLetterStatus(card([letter({ status: "sent", ticket_ids: ["a"] }), letter({ status: "failed", sent_at: null, ticket_ids: ["b"] })])).state,
    ).toBe("partial");
  });

  it("tells a refused send from a bounce", () => {
    const refused = ticketLetterStatus(card([letter({ status: "failed", sent_at: null, failure_reason: "relay  refused\nthe letter" })]));
    expect(refused.state).toBe("failed");
    expect(refused.why).toBe("relay refused the letter");
    const bounced = ticketLetterStatus(card([letter({ status: "failed", failed_at: T1, failure_reason: "mailbox full" })]));
    expect(bounced.state).toBe("bounced");
    expect(letterFailed(bounced)).toBe(true);
  });

  it("reads delayed and stuck", () => {
    expect(ticketLetterStatus(card([letter({ status: "delayed", deferred_at: T1 })])).state).toBe("delayed");
    const stuck = ticketLetterStatus(card([letter({ status: "sent", stuck: true })]));
    expect(stuck.state).toBe("stuck");
    expect(letterFailed(stuck)).toBe(true);
  });

  it("never calls a proxy open reading", () => {
    // The API folds Apple's proxy fetch into `delivered`: the card says delivered.
    const s = ticketLetterStatus(card([letter({ status: "delivered", proxy_opened_at: T1, delivered_at: null })]));
    expect(s.state).toBe("delivered");
    expect(s.at).toBe(T1);
  });

  it("falls back to the delivery jobs when the card has no ticket letters", () => {
    const o = card([], {
      delivery: [
        { ticket_id: "a", status: "sent", sent_at: T0, last_error: null, recipient_email: "other@example.test" },
        { ticket_id: "b", status: "pending", sent_at: null, last_error: null },
      ],
    });
    const s = ticketLetterStatus(o);
    expect(s.state).toBe("partial_queued");
    expect(s.to).toBe("other@example.test");
    expect(ticketLetterStatus(card([], { delivery: [] })).state).toBe("none");
    expect(ticketLetterStatus(card([], { delivery: [{ ticket_id: "a", status: "failed", sent_at: null, last_error: "smtp down" }] })).state).toBe("failed");
  });

  it("tolerates a card without letters at all", () => {
    const o = { buyer_email: null, delivery: [{ ticket_id: "a", status: "sent", sent_at: T0, last_error: null }] } as unknown as OrderDetail;
    expect(ticketLetterStatus(o).state).toBe("sent");
  });
});

describe("refundLetterStatus", () => {
  const refund = (state: string, extra: Partial<{ refunds: number; sent: number; at: string | null }> = {}) =>
    card([], { refund_letter: { state, refunds: 2, sent: 1, at: T0, ...extra } });
  it("is absent when the order has no refund operation", () => {
    expect(refundLetterStatus(card([]))).toBeNull();
  });
  it("maps the server's states", () => {
    expect(refundLetterStatus(refund("off"))?.state).toBe("off");
    expect(refundLetterStatus(refund("queued"))?.state).toBe("queued");
    expect(refundLetterStatus(refund("partial"))?.state).toBe("partial_queued");
    expect(refundLetterStatus(refund("failed"))?.state).toBe("failed");
    expect(refundLetterStatus(refund("what"))).toBeNull();
  });
  it("moves a sent letter on to what the provider reported", () => {
    const sent = (l: Partial<OrderLetter>) => refundLetterStatus(card([letter({ kind: "refund", ...l })], { refund_letter: { state: "sent", refunds: 1, sent: 1, at: T0 } }));
    expect(sent({ status: "sent" })?.state).toBe("sent");
    expect(sent({ status: "delivered", delivered_at: T1 })?.state).toBe("delivered");
    expect(sent({ status: "opened", opened_at: T1 })?.state).toBe("opened");
    expect(sent({ status: "failed", failed_at: T1, failure_reason: "no such user" })?.state).toBe("bounced");
    expect(sent({ status: "sent", stuck: true })?.state).toBe("stuck");
  });
});

describe("letterTone", () => {
  it("is bad for what needs action, ok for what arrived, neutral for the rest", () => {
    const mk = (state: LetterState) => ({ state, at: null, to: "", done: 0, total: 0, why: "" });
    expect(letterTone(mk("bounced"))).toBe("bad");
    expect(letterTone(mk("stuck"))).toBe("bad");
    expect(letterTone(mk("delivered"))).toBe("ok");
    expect(letterTone(mk("queued"))).toBe("neutral");
    expect(letterTone(mk("delayed"))).toBe("neutral");
  });
});

describe("letterLine", () => {
  const states: LetterState[] = ["none", "off", "queued", "partial_queued", "sent", "delivered", "opened", "partial", "failed", "delayed", "stuck", "bounced"];
  for (const locale of LOCALES) {
    const t = (key: Parameters<typeof translate>[1], params?: Parameters<typeof translate>[2]) => translate(locale, key, params);
    it(`has a sentence for every state of a ticket letter in ${locale}, with no placeholder left over`, () => {
      for (const state of states) {
        if (!hasLetterText("tickets", state)) {
          expect(state, "only these have no ticket text").toBe("off");
          continue;
        }
        const line = letterLine(t, "tickets", { state, at: T0, to: "anna@example.test", done: 1, total: 2, why: "mailbox full" }, "Europe/Madrid", locale);
        expect(line, state).not.toBe("");
        expect(line, state).not.toMatch(/\{[a-zA-Z]+\}/);
      }
    });
    it(`has a sentence for every state of a refund letter in ${locale}`, () => {
      for (const state of states) {
        if (!hasLetterText("refund", state)) continue;
        const line = letterLine(t, "refund", { state, at: T0, to: "anna@example.test", done: 1, total: 2, why: "x" }, "Europe/Madrid", locale);
        expect(line, state).not.toMatch(/\{[a-zA-Z]+\}/);
      }
    });
  }

  it("drops an optional part with its preposition", () => {
    const t = (key: Parameters<typeof translate>[1], params?: Parameters<typeof translate>[2]) => translate("en", key, params);
    expect(letterLine(t, "tickets", { state: "sent", at: null, to: "", done: 1, total: 1, why: "" }, "Europe/Madrid", "en")).toBe("Ticket e-mail: sent.");
    const full = letterLine(t, "tickets", { state: "sent", at: T0, to: "anna@example.test", done: 1, total: 1, why: "" }, "Europe/Madrid", "en");
    expect(full).toContain("anna@example.test");
    expect(full).toMatch(/sent on .*12 Oct/);
  });

  it("says why a letter bounced", () => {
    const t = (key: Parameters<typeof translate>[1], params?: Parameters<typeof translate>[2]) => translate("ru", key, params);
    const line = letterLine(t, "tickets", { state: "bounced", at: null, to: "a@b.io", done: 0, total: 1, why: "ящик полон" }, "Europe/Madrid", "ru");
    expect(line).toContain("до a@b.io");
    expect(line).toContain("Причина: ящик полон");
  });

  it("every locale carries the same fragment set", () => {
    for (const locale of LOCALES) {
      for (const key of Object.keys(CATALOGS.en).filter((k) => k.startsWith("letter.frag."))) {
        expect(key in CATALOGS[locale], `${locale} ${key}`).toBe(true);
      }
    }
  });
});
