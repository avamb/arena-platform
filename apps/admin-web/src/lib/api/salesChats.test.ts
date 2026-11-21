/**
 * Telegram chats of the sales bot, superadmin side: forms are checked before sending, the list reads as the
 * screen shows it, and every write carries the reason the person wrote.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { __TEST_ONLY_resetTokenStore, setSession } from "@/lib/api/tokenStore";
import { ApiError } from "@/lib/api/client";
import { setActiveReason } from "@/lib/api/reason";
import {
  buildAddChatRequest,
  buildEditChatRequest,
  chatErrorMessage,
  chatState,
  countdownLabel,
  createSalesChatCode,
  deleteSalesChat,
  editChatForm,
  emptyAddChatForm,
  kindsLabel,
  listSalesChats,
  patchSalesChat,
  secondsLeft,
  toggleKind,
  type SalesChat,
} from "@/lib/api/salesChats";

function chat(over: Partial<SalesChat> = {}): SalesChat {
  return {
    id: "c1",
    org_id: "o1",
    name: "Delivery",
    chat_id: "-100123",
    chat_title: "Couriers",
    kinds: ["shop_delivery"],
    allowed: true,
    on_order_paid: true,
    on_ticket_refunded: false,
    last_error: null,
    bot_removed_at: null,
    connected_at: "2026-11-05T10:00:00Z",
    created_at: "2026-11-05T09:00:00Z",
    updated_at: "2026-11-05T10:00:00Z",
    ...over,
  } as SalesChat;
}

describe("kinds", () => {
  it("names what a chat hears in the screen's order", () => {
    expect(kindsLabel(["shop_delivery", "tickets"])).toBe("Tickets, Delivery");
    expect(kindsLabel(["shop_sale"])).toBe("Shop sales");
    expect(kindsLabel([])).toBe("No kinds");
    expect(kindsLabel(["tickets", "future_kind"])).toBe("Tickets, future_kind");
  });

  it("toggles a kind and keeps the order", () => {
    expect(toggleKind(["shop_delivery"], "tickets", true)).toEqual(["tickets", "shop_delivery"]);
    expect(toggleKind(["tickets", "shop_delivery"], "tickets", false)).toEqual(["shop_delivery"]);
  });
});

describe("buildAddChatRequest", () => {
  it("needs a name, a kind and a reason, and says all that is missing", () => {
    const built = buildAddChatRequest(emptyAddChatForm());
    expect(!built.ok && built.errors).toHaveLength(3);
  });

  it("trims and builds the code request", () => {
    const built = buildAddChatRequest({ name: "  Delivery  ", kinds: ["shop_delivery"], reason: " New logistics group " });
    expect(built).toEqual({ ok: true, reason: "New logistics group", request: { name: "Delivery", kinds: ["shop_delivery"] } });
  });

  it("refuses a name over 200 characters", () => {
    expect(buildAddChatRequest({ name: "x".repeat(201), kinds: ["tickets"], reason: "r" }).ok).toBe(false);
  });
});

describe("buildEditChatRequest", () => {
  const c = chat({ kinds: ["tickets"] });

  it("sends only what changed", () => {
    const form = { ...editChatForm(c), kinds: ["tickets", "shop_sale"] as ("tickets" | "shop_sale")[], reason: "Sales chat too" };
    expect(buildEditChatRequest(c, form)).toEqual({ ok: true, reason: "Sales chat too", request: { kinds: ["tickets", "shop_sale"] } });
    const renamed = { ...editChatForm(c), name: "New name", reason: "Rename" };
    expect(buildEditChatRequest(c, renamed)).toEqual({ ok: true, reason: "Rename", request: { name: "New name" } });
  });

  it("refuses an edit that changes nothing", () => {
    const built = buildEditChatRequest(c, { ...editChatForm(c), reason: "r" });
    expect(!built.ok && built.errors.join(" ")).toContain("No change");
  });

  it("refuses an empty list of kinds and a missing reason", () => {
    const built = buildEditChatRequest(c, { name: "Delivery", kinds: [], reason: "" });
    expect(built.ok).toBe(false);
    expect(!built.ok && built.errors).toHaveLength(2);
  });
});

describe("chatState", () => {
  it("reads the row the way the operator needs it", () => {
    expect(chatState(chat()).label).toBe("On");
    expect(chatState(chat({ allowed: false })).label).toBe("Off");
    expect(chatState(chat({ bot_removed_at: "2026-11-05T12:00:00Z" })).label).toBe("Bot removed from the group");
    expect(chatState(chat({ bot_removed_at: "2026-11-05T12:00:00Z", allowed: false })).tone).toBe("bad");
    expect(chatState(chat({ chat_id: "", connected_at: null })).label).toBe("Needs /connect");
  });
});

describe("countdown", () => {
  const now = new Date("2026-11-06T10:00:00Z");
  it("counts down to the expiry and stops at zero", () => {
    expect(secondsLeft("2026-11-06T10:29:41Z", now)).toBe(1781);
    expect(countdownLabel(1781)).toBe("29:41");
    expect(countdownLabel(65)).toBe("1:05");
    expect(secondsLeft("2026-11-06T09:00:00Z", now)).toBe(0);
    expect(countdownLabel(0)).toBe("Expired");
    expect(secondsLeft("not a date", now)).toBe(0);
  });
});

describe("chatErrorMessage", () => {
  it("explains the codes in plain words", () => {
    expect(chatErrorMessage(new ApiError(422, { code: "sales_chat.invalid_kinds", message: "x" }))).toContain("Select Tickets, Shop sales or Delivery");
    expect(chatErrorMessage(new ApiError(404, { code: "sales_chat.not_found", message: "x" }))).toContain("we deleted it before");
    expect(chatErrorMessage(new ApiError(403, { code: "org_module.superadmin_only", message: "x" }))).toContain("platform superadmin");
    expect(chatErrorMessage(new ApiError(500, { code: "something.else", message: "Boom" }))).toBe("Boom");
  });
});

describe("calls", () => {
  const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

  beforeEach(() => {
    __TEST_ONLY_resetTokenStore();
    setActiveReason("Reading the chats");
    setSession({ accessToken: "t", refreshToken: "r", expiresAt: new Date(Date.now() + 3_600_000).toISOString(), userId: "u" });
  });
  afterEach(() => {
    setActiveReason(null);
    __TEST_ONLY_resetTokenStore();
    vi.restoreAllMocks();
  });

  it("lists the organization's chats and the operator's from their own routes", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ org_id: null, items: [] })));
    vi.stubGlobal("fetch", fetchMock);
    await listSalesChats("org 1");
    await listSalesChats(null);
    expect((fetchMock.mock.calls[0] as [string])[0]).toContain("/v1/admin/organizations/org%201/sales-chats");
    expect((fetchMock.mock.calls[1] as [string])[0]).toContain("/v1/admin/sales-chats/operator");
  });

  it("makes a code with the reason of the dialog, for an organization and for the platform", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ id: "k", code: "ABCDE-FGHJK", command: "/connect ABCDE-FGHJK" }, 201)));
    vi.stubGlobal("fetch", fetchMock);
    await createSalesChatCode("org-1", { name: "Delivery", kinds: ["shop_delivery"] }, "New group");
    await createSalesChatCode(null, { name: "Ops", kinds: ["tickets"] }, "Ops");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/v1/admin/organizations/org-1/sales-chats/connect-codes");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["X-Admin-Reason"]).toBe("New group");
    expect(JSON.parse(String(init.body))).toEqual({ name: "Delivery", kinds: ["shop_delivery"] });
    expect((fetchMock.mock.calls[1] as [string])[0]).toContain("/v1/admin/sales-chats/operator/connect-codes");
  });

  it("patches and deletes with a reason", async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(json({ deleted: true, id: "c1" })));
    vi.stubGlobal("fetch", fetchMock);
    await patchSalesChat("c1", { allowed: false }, "Group closed");
    await deleteSalesChat("c1", "Not needed");
    const [pUrl, pInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(pUrl).toContain("/v1/admin/sales-chats/c1");
    expect(pInit.method).toBe("PATCH");
    expect((pInit.headers as Record<string, string>)["X-Admin-Reason"]).toBe("Group closed");
    expect(JSON.parse(String(pInit.body))).toEqual({ allowed: false });
    const [, dInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(dInit.method).toBe("DELETE");
    expect((dInit.headers as Record<string, string>)["X-Admin-Reason"]).toBe("Not needed");
  });
});
