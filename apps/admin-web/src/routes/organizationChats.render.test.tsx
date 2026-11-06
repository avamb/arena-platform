/**
 * The "Telegram chats" tab. The admin-web Vitest environment is Node-only (no jsdom), so the query-free
 * presentational parts are rendered with renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import { AddChatBody, ChatsView, CodeBody, EditChatBody, KindsField, Problems } from "@/routes/organizationChats";
import { editChatForm, emptyAddChatForm, type SalesChat, type SalesChatConnectCode } from "@/lib/api/salesChats";

const NOW = new Date("2026-11-06T10:00:00Z");
const noop = (): void => undefined;

function chat(over: Partial<SalesChat> = {}): SalesChat {
  return {
    id: "c1",
    org_id: "o1",
    name: "Courier group",
    chat_id: "-100123",
    chat_title: "Couriers Madrid",
    kinds: ["shop_sale", "shop_delivery"],
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

function view(chats: SalesChat[], notice: string | null = null): string {
  return renderToStaticMarkup(createElement(ChatsView, { chats, scopeLabel: "this organization", notice, onAdd: noop, onEdit: noop, onToggle: noop, onDelete: noop }));
}

describe("ChatsView", () => {
  it("lists name, what it hears, state, title, connected since and the actions", () => {
    const html = view([chat()]);
    expect(html).toContain("Courier group");
    expect(html).toContain("Shop sales, Delivery");
    expect(html).toContain("Couriers Madrid");
    expect(html).toContain("2026-11-05 10:00 UTC");
    expect(html).toContain(">On<");
    expect(html).toContain('data-testid="sales-chat-edit-c1"');
    expect(html).toContain("Turn off");
    expect(html).toContain('data-testid="sales-chat-delete-c1"');
    expect(html).toContain('data-testid="sales-chat-add"');
  });

  it("shows the last delivery error and that the bot was removed from the group", () => {
    const html = view([chat({ last_error: "Forbidden: bot was kicked", bot_removed_at: "2026-11-05T12:00:00Z" })]);
    expect(html).toContain("Forbidden: bot was kicked");
    expect(html).toContain("Bot removed from the group");
  });

  it("a switched-off chat offers Turn on", () => {
    const html = view([chat({ allowed: false })]);
    expect(html).toContain("Turn on");
    expect(html).toContain(">Off<");
  });

  it("explains an empty list and shows a notice", () => {
    const html = view([], "The chat is saved.");
    expect(html).toContain('data-testid="sales-chats-empty"');
    expect(html).toContain("The chat is saved.");
    expect(html).not.toContain("<table");
  });
});

describe("forms", () => {
  it("the add form has a name, the three kinds as checkboxes and a reason", () => {
    const html = renderToStaticMarkup(createElement(AddChatBody, { form: emptyAddChatForm(), onChange: noop }));
    for (const id of ["sales-chat-name", "sales-chat-kind-tickets", "sales-chat-kind-shop_sale", "sales-chat-kind-shop_delivery", "sales-chat-reason"]) {
      expect(html).toContain(`data-testid="${id}"`);
    }
    expect(html).not.toContain("checked");
  });

  it("the edit form starts from the chat", () => {
    const html = renderToStaticMarkup(createElement(EditChatBody, { form: editChatForm(chat()), onChange: noop }));
    expect(html).toContain('value="Courier group"');
    expect(html).toContain("checked");
  });

  it("kinds carry a plain hint each", () => {
    const html = renderToStaticMarkup(createElement(KindsField, { kinds: ["shop_sale"], onChange: noop }));
    expect(html).toContain("No buyer data");
    expect(html).toContain("step buttons");
  });

  it("problems are announced", () => {
    const html = renderToStaticMarkup(createElement(Problems, { errors: ["Name: write a name"], serverError: "The chat was not found" }));
    expect(html).toContain('role="alert"');
    expect(html).toContain("Name: write a name");
    expect(html).toContain("The chat was not found");
    expect(renderToStaticMarkup(createElement(Problems, { errors: [], serverError: null }))).toBe("");
  });
});

describe("CodeBody", () => {
  const code: SalesChatConnectCode = {
    id: "k1",
    code: "7KQ2M-9ABCD",
    command: "/connect 7KQ2M-9ABCD",
    expires_at: "2026-11-06T10:29:41Z",
    name: "Courier group",
    kinds: ["shop_delivery"],
  } as SalesChatConnectCode;

  it("shows the exact command, the instruction, a copy button and the time left", () => {
    const html = renderToStaticMarkup(createElement(CodeBody, { code, now: NOW, copied: false, onCopy: noop }));
    expect(html).toContain("/connect 7KQ2M-9ABCD");
    expect(html).toContain("@ArenaSoldOutSalesBot");
    expect(html).toContain('data-testid="sales-chat-copy"');
    expect(html).toContain(">Copy<");
    expect(html).toContain("29:41");
    expect(html).toContain("Delivery");
  });

  it("says Copied after the copy and Expired after the expiry", () => {
    expect(renderToStaticMarkup(createElement(CodeBody, { code, now: NOW, copied: true, onCopy: noop }))).toContain(">Copied<");
    const late = new Date("2026-11-06T11:00:00Z");
    expect(renderToStaticMarkup(createElement(CodeBody, { code, now: late, copied: false, onCopy: noop }))).toContain("Expired");
  });
});
