import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { ComplimentaryListItem, OrderDetail, ReissueOptions } from "@/lib/api/types";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import { apiErrorStem } from "@/lib/errors";
import {
  INVITATION_TEXT_VARIANTS,
  MAX_REISSUE_NOTE,
  invitationReissueOffered,
  reissueText,
  viewFromInvitationOptions,
  viewFromOrderOptions,
  backToAddress,
  checkReissueAddress,
  checkReissueReason,
  historyStateKey,
  newReissueKey,
  pairsText,
  providerKeepsOldAddress,
  providerNoteKey,
  reasonKey,
  reissueKeyFor,
  reissueOffered,
  reissueOutcomeUnknown,
  reissueRequest,
  reissueStart,
  reissuableTickets,
  scannedTickets,
} from "@/lib/reissue";

const ticket = (status: string) => ({ status }) as OrderDetail["tickets"][number];
const order = (status: string, tickets: string[]) => ({ status, tickets: tickets.map(ticket) }) as Pick<OrderDetail, "status" | "tickets">;
const all = () => true;

function options(over: Partial<ReissueOptions> = {}): ReissueOptions {
  return {
    route: "arena",
    order_id: "o1",
    order_number: 1001,
    order_status: "paid",
    order_email: "wrong@example.test",
    buyer_name: "Ana",
    buyer_phone: "+34600000000",
    tickets: [
      { ticket_id: "t1", number: 11, seat: "", category: "Standing", status: "active", reissuable: true },
      { ticket_id: "t2", number: 12, seat: "", category: "Standing", status: "active", reissuable: true, scanned_at: "2026-11-07T19:05:00Z" },
      { ticket_id: "t3", number: 13, seat: "", category: "Standing", status: "cancelled", reissuable: false, reason: "not_active" },
    ],
    blocked: null,
    ...over,
  };
}

describe("who sees the button", () => {
  it("a paid order with an active ticket, for ticket.update", () => {
    expect(reissueOffered(order("paid", ["active"]), all)).toBe(true);
    expect(reissueOffered(order("partially_refunded", ["active", "cancelled"]), all)).toBe(true);
  });
  it("not without the permission, an unpaid order or an active ticket", () => {
    expect(reissueOffered(order("paid", ["active"]), (p) => p !== "ticket.update")).toBe(false);
    expect(reissueOffered(order("pending_payment", ["active"]), all)).toBe(false);
    expect(reissueOffered(order("refunded", ["cancelled"]), all)).toBe(false);
    expect(reissueOffered(order("paid", ["transferred"]), all)).toBe(false);
  });
});

describe("the first screen", () => {
  it("a seller site explains first", () => {
    expect(reissueStart(options({ route: "seller_site", blocked: "order.seller_site_order" }))).toEqual({ kind: "site" });
  });
  it("a reissue still waiting for its letter shows its state", () => {
    const pending = { id: "r1", state: "send_failed", new_email: "right@example.test", created_at: "2026-11-07T10:00:00Z" };
    expect(reissueStart(options({ pending_reissue: pending, blocked: "reissue.in_progress" }))).toEqual({
      kind: "pending",
      id: "r1",
      state: "send_failed",
      email: "right@example.test",
    });
  });
  it("any other block is explained, else the case question", () => {
    expect(reissueStart(options({ blocked: "reissue.session_closed" }))).toEqual({ kind: "blocked", code: "reissue.session_closed" });
    expect(reissueStart(options())).toEqual({ kind: "case" });
    expect(reissueStart(options({ blocked: "" }))).toEqual({ kind: "case" });
  });
});

describe("the address and the reason", () => {
  it("a bad address is stopped before the server", () => {
    expect(checkReissueAddress("not-an-email", "a@b.test")).toEqual({ ok: false, note: "badEmail" });
    expect(checkReissueAddress("", "a@b.test")).toEqual({ ok: false, note: "badEmail" });
  });
  it("trims and tells the order's own address apart", () => {
    expect(checkReissueAddress("  Right@Example.test ", "wrong@example.test")).toEqual({ ok: true, email: "Right@Example.test", same: false });
    expect(checkReissueAddress("WRONG@example.test", "wrong@example.test")).toEqual({ ok: true, email: "WRONG@example.test", same: true });
  });
  it("a reason is required and a note is at most 500 characters", () => {
    expect(checkReissueReason("", "")).toEqual({ ok: false, note: "reasonRequired" });
    expect(checkReissueReason("typo", "")).toEqual({ ok: false, note: "reasonRequired" });
    expect(checkReissueReason("other", "x".repeat(MAX_REISSUE_NOTE))).toEqual({ ok: true });
    expect(checkReissueReason("other", "я".repeat(MAX_REISSUE_NOTE + 1))).toEqual({ ok: false, note: "noteTooLong" });
  });
  it("the request leaves an empty note out", () => {
    expect(reissueRequest({ email: "r@e.test", reason: "other", note: "  " })).toEqual({ email: "r@e.test", reason: "other" });
    expect(reissueRequest({ email: "r@e.test", reason: "other", note: " call " })).toEqual({ email: "r@e.test", reason: "other", note: "call" });
  });
});

describe("one key per operation", () => {
  const plan = { email: "r@e.test", reason: "sent_to_wrong_address" as const, note: "" };
  it("keeps the key while the request is the same", () => {
    let n = 0;
    const mint = () => `k${++n}`;
    const first = reissueKeyFor(null, plan, mint);
    expect(first.key).toBe("k1");
    expect(reissueKeyFor(first, { ...plan }, mint)).toBe(first);
    expect(reissueKeyFor(first, { ...plan, note: "x" }, mint).key).toBe("k2");
    expect(reissueKeyFor(first, { ...plan, email: "o@e.test" }, mint).key).toBe("k3");
  });
  it("a web key", () => {
    expect(newReissueKey(() => "abc")).toBe("web-abc");
  });
  it("a lost answer is unknown, a refusal is not", () => {
    expect(reissueOutcomeUnknown(new ApiError(0, "", "net"))).toBe(true);
    expect(reissueOutcomeUnknown(new ApiError(502, "", "gw"))).toBe(true);
    expect(reissueOutcomeUnknown(new ApiError(500, "reissue.internal", "x"))).toBe(true);
    expect(reissueOutcomeUnknown(new ApiError(409, "reissue.in_progress", "x"))).toBe(false);
    expect(reissueOutcomeUnknown(new ApiError(422, "reissue.invalid_email", "x"))).toBe(false);
    expect(reissueOutcomeUnknown(new Error("x"))).toBe(false);
    expect(backToAddress(new ApiError(422, "reissue.invalid_email", "x"))).toBe(true);
    expect(backToAddress(new ApiError(409, "reissue.in_progress", "x"))).toBe(false);
  });
});

describe("the check screen", () => {
  it("lists the tickets that will be replaced and warns about used codes", () => {
    expect(reissuableTickets(options()).map((t) => t.number)).toEqual([11, 12]);
    expect(scannedTickets(options()).map((t) => t.number)).toEqual([12]);
  });
  it("names the payment system that keeps the old address", () => {
    expect(providerKeepsOldAddress(options())).toBeNull();
    expect(providerKeepsOldAddress(options({ provider: { name: "stripe", title: "Stripe", email_updatable: true } }))).toBeNull();
    expect(providerKeepsOldAddress(options({ provider: { name: "allpay", title: "AllPay", email_updatable: false } }))).toBe("AllPay");
    expect(providerKeepsOldAddress(options({ provider: { name: "flitt", title: "", email_updatable: false } }))).toBe("flitt");
  });
});

describe("the history", () => {
  it("keys of reasons, states and the provider note", () => {
    expect(reasonKey("forwarded_to_stranger")).toBe("reissue.reason.forwarded_to_stranger");
    expect(reasonKey("something")).toBe("reissue.reason.other");
    expect(historyStateKey("applied")).toBe("reissue.history.state.applied");
    expect(historyStateKey("send_failed")).toBe("reissue.history.state.send_failed");
    expect(historyStateKey("pending")).toBe("reissue.history.state.pending");
    expect(providerNoteKey({ provider_email_state: "manual" })).toBe("reissue.history.providerManual");
    expect(providerNoteKey({ provider_email_state: "failed" })).toBe("reissue.history.providerFailed");
    expect(providerNoteKey({ provider_email_state: "updated" })).toBeNull();
    expect(providerNoteKey({ provider_email_state: null })).toBeNull();
  });
  it("pairs read old to new", () => {
    expect(pairsText([{ old_number: 11, new_number: 21 }, { old_number: 12, new_number: 22 }])).toBe("№11 → №21, №12 → №22");
  });
});

describe("every reissue refusal has its own text", () => {
  for (const code of [
    "order.not_paid",
    "reissue.nothing_to_reissue",
    "reissue.ticket_not_reissuable",
    "reissue.session_closed",
    "reissue.in_progress",
    "reissue.idempotency_key_reused",
    "reissue.already_applied",
    "reissue.invalid_email",
    "reissue.invalid_reason",
    "reissue.note_too_long",
    "reissue.api_key_forbidden",
    "reissue.invitation_not_issued",
  ]) {
    it(code, () => {
      const stem = apiErrorStem(new ApiError(409, code, "x"));
      expect(["conflict", "invalid", "generic", "server"]).not.toContain(stem);
    });
  }
});

describe("an invitation without an order (stage R-5)", () => {
  const item = (status: string, tickets: string[]) =>
    ({ status, tickets: tickets.map((s) => ({ status: s })) }) as unknown as Pick<ComplimentaryListItem, "status" | "tickets">;
  it("is offered while issued with an active ticket, for ticket.update", () => {
    expect(invitationReissueOffered(item("issued", ["active"]), all)).toBe(true);
    expect(invitationReissueOffered(item("issued", ["active"]), (p) => p !== "ticket.update")).toBe(false);
    expect(invitationReissueOffered(item("revoked", ["cancelled"]), all)).toBe(false);
    expect(invitationReissueOffered(item("issued", ["transferred"]), all)).toBe(false);
  });
  it("its options read like an order's without a site, a buyer or a payment", () => {
    const view = viewFromInvitationOptions({
      invitation_id: "i1",
      invitation_status: "issued",
      guest_email: "guest@example.test",
      tickets: options().tickets,
      blocked: null,
    });
    expect(view.route).toBe("arena");
    expect(view.email).toBe("guest@example.test");
    expect(view.provider).toBeNull();
    expect(providerKeepsOldAddress(view)).toBeNull();
    expect(reissueStart(view)).toEqual({ kind: "case" });
    expect(reissueStart({ ...view, blocked: "reissue.invitation_not_issued" })).toEqual({ kind: "blocked", code: "reissue.invitation_not_issued" });
  });
  it("an order's options keep the venue zone", () => {
    expect(viewFromOrderOptions(options({ venue_timezone: "Europe/Madrid" })).timezone).toBe("Europe/Madrid");
    expect(viewFromOrderOptions(options()).timezone).toBe("");
  });
  it("every invitation text variant exists in every language", () => {
    for (const key of INVITATION_TEXT_VARIANTS) {
      expect(reissueText("order", key)).toBe(key);
      const inv = reissueText("invitation", key);
      expect(inv.startsWith("reissue.inv.")).toBe(true);
      for (const locale of LOCALES) expect(Object.prototype.hasOwnProperty.call(CATALOGS[locale], inv), `${locale} ${inv}`).toBe(true);
    }
  });
  it("an annulled invitation has its own refusal text", () => {
    expect(apiErrorStem(new ApiError(409, "reissue.invitation_not_issued", "x"))).toBe("reissueInvitationNotIssued");
  });
});
