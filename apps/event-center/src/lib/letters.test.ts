import { describe, expect, it } from "vitest";
import type { OrderDetail, OrderLetter } from "@/lib/api/types";
import { CATALOGS, LOCALES, translate } from "@/lib/i18n";
import { hasLetterText, letterLine } from "@/lib/letterText";
import {
  latestTicketLetters,
  letterEvents,
  letterFailed,
  letterHistory,
  letterTone,
  refundLetterStatus,
  ticketLetterStatus,
  ticketLetterSteps,
  type LetterState,
} from "@/lib/letters";

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

describe("resends: the newest letter of every ticket", () => {
  const T2 = "2026-10-13T09:00:00Z";
  const T3 = "2026-10-13T09:10:00Z";
  const bounce = letter({ status: "failed", failed_at: T1, failure_event: "hard_bounce", recipient: "anna@exmaple.test" });
  const resent = letter({ status: "opened", sent_at: T2, delivered_at: T2, opened_at: T3, recipient: "anna.work@example.test" });

  it("a later letter for the same tickets replaces an old bounce", () => {
    expect(latestTicketLetters([bounce, resent])).toEqual([resent]);
    const s = ticketLetterStatus(card([bounce, resent]));
    expect(s.state).toBe("opened");
    expect(s.done).toBe(2);
    expect(s.total).toBe(2);
    expect(s.to).toBe("anna.work@example.test");
  });

  it("a queued resend is newer than the letter it replaces", () => {
    const queued = letter({ status: "queued", sent_at: null, queued_at: T2 });
    expect(ticketLetterStatus(card([letter({ status: "delivered", delivered_at: T1 }), queued])).state).toBe("queued");
  });

  it("keeps letters that name no ticket", () => {
    const old = letter({ ticket_ids: [] });
    expect(latestTicketLetters([old, resent])).toEqual([old, resent]);
  });
});

describe("ticketLetterSteps", () => {
  const steps = (ls: OrderLetter[]) => {
    const c = card(ls);
    return ticketLetterSteps(c, ticketLetterStatus(c));
  };

  it("marks each point the letter reached, with its time", () => {
    const s = steps([letter({ status: "opened", delivered_at: T1, opened_at: "2026-10-12T10:20:00Z" })]);
    expect(s?.map((p) => [p.key, p.mark, p.at])).toEqual([
      ["sent", "done", T0],
      ["delivered", "done", T1],
      ["opened", "done", "2026-10-12T10:20:00Z"],
    ]);
  });

  it("counts a proxy open as delivery, never as reading", () => {
    const s = steps([letter({ status: "delivered", proxy_opened_at: T1 })]);
    expect(s?.map((p) => p.mark)).toEqual(["done", "done", "todo"]);
    expect(s?.[1]?.at).toBe(T1);
  });

  it("puts the problem on the point where the letter stopped", () => {
    expect(steps([letter({ status: "failed", failed_at: T1 })])?.map((p) => [p.key, p.mark])).toEqual([
      ["sent", "done"],
      ["bounced", "bad"],
      ["opened", "todo"],
    ]);
    expect(steps([letter({ status: "failed", sent_at: null, failed_at: T1 })])?.[0]).toEqual({ key: "failed", mark: "bad", at: T1 });
    expect(steps([letter({ status: "delayed", deferred_at: T1 })])?.[1]).toEqual({ key: "delayed", mark: "wait", at: T1 });
    expect(steps([letter({ status: "sent", stuck: true })])?.[1]?.key).toBe("stuck");
    expect(steps([letter({ status: "queued", sent_at: null, queued_at: T0 })])?.map((p) => p.mark)).toEqual(["todo", "todo", "todo"]);
  });

  it("draws nothing when there is no letter", () => {
    expect(ticketLetterSteps({ letters: [] }, ticketLetterStatus(card([], { delivery: [] })))).toBeNull();
  });
});

describe("letterHistory", () => {
  it("lists every letter oldest first and marks resends and other addresses", () => {
    const first = letter({ status: "delivered", delivered_at: T1 });
    const again = letter({ status: "opened", sent_at: "2026-10-13T09:00:00Z", opened_at: "2026-10-13T09:05:00Z", recipient: "Anna.Work@example.test" });
    const change = letter({ kind: "change", ticket_ids: [], sent_at: "2026-10-14T08:00:00Z", recipient: "ANNA@example.test" });
    const h = letterHistory(card([change, again, first]));
    expect(h.map((e) => e.letter)).toEqual([first, again, change]);
    expect(h.map((e) => e.resend)).toEqual([false, true, false]);
    expect(h.map((e) => e.otherAddress)).toEqual([false, true, false]);
  });

  it("names what happened to a letter in order", () => {
    const l = letter({ status: "opened", delivered_at: T1, proxy_opened_at: T1, opened_at: "2026-10-12T10:20:00Z", clicked_at: "2026-10-12T10:21:00Z" });
    expect(letterEvents(l).map((e) => e.key)).toEqual(["sent", "delivered", "proxy", "opened", "clicked"]);
    expect(letterEvents(letter({ status: "queued", sent_at: null, queued_at: T0 })).map((e) => e.key)).toEqual(["queued"]);
  });
});
