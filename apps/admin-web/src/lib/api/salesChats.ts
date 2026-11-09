/**
 * Telegram chats of the sales bot, the superadmin side (spec 08_architecture/61, section 7a.4).
 *
 *   GET    /v1/admin/organizations/{org_id}/sales-chats
 *   POST   /v1/admin/organizations/{org_id}/sales-chats/connect-codes
 *   GET    /v1/admin/sales-chats/operator
 *   POST   /v1/admin/sales-chats/operator/connect-codes
 *   PATCH  /v1/admin/sales-chats/{id}
 *   DELETE /v1/admin/sales-chats/{id}
 *
 * A chat is never typed in by number: the superadmin makes a one-time code, adds the sales bot to the group and
 * posts `/connect XXXXX-XXXXX` there, and the bot writes the chat id itself. Every write carries X-Admin-Reason,
 * passed here from the field the person just filled in.
 *
 * Mock data: NONE.
 */
import type { components } from "@openapi/index";
import { ApiError, authedFetch } from "@/lib/api/client";

type Schemas = components["schemas"];

export type SalesChat = Schemas["SalesChat"];
export type SalesChatList = Schemas["SalesChatList"];
export type SalesChatConnectCode = Schemas["SalesChatConnectCode"];
export type SalesChatConnectCodeRequest = Schemas["SalesChatConnectCodeRequest"];
export type SalesChatPatchRequest = Schemas["SalesChatPatchRequest"];

const enc = encodeURIComponent;

/** What a chat can hear, in the order the screen lists them. */
export const CHAT_KINDS = ["tickets", "shop_sale", "shop_delivery"] as const;
export type ChatKind = (typeof CHAT_KINDS)[number];

const KIND_LABELS: Record<ChatKind, string> = {
  tickets: "Tickets",
  shop_sale: "Shop sales",
  shop_delivery: "Delivery",
};

const KIND_HINTS: Record<ChatKind, string> = {
  tickets: "Ticket sales and refunds.",
  shop_sale: "A message for each shop sale: order number, amount and products. No buyer data.",
  shop_delivery: "The full card of the shop order with the step buttons. It is for the people who pack and deliver.",
};

export function kindLabel(kind: string): string {
  return kind in KIND_LABELS ? KIND_LABELS[kind as ChatKind] : kind;
}

export function kindHint(kind: ChatKind): string {
  return KIND_HINTS[kind];
}

/** "Tickets, Delivery": the kinds in the screen's order, unknown ones last. */
export function kindsLabel(kinds: readonly string[]): string {
  const known = CHAT_KINDS.filter((k) => kinds.includes(k)).map(kindLabel);
  const other = kinds.filter((k) => !(CHAT_KINDS as readonly string[]).includes(k));
  const all = [...known, ...other];
  return all.length > 0 ? all.join(", ") : "No kinds";
}

export function listOrgSalesChats(orgId: string): Promise<SalesChatList> {
  return authedFetch({ method: "GET", path: `/v1/admin/organizations/${enc(orgId)}/sales-chats` });
}

export function listOperatorSalesChats(): Promise<SalesChatList> {
  return authedFetch({ method: "GET", path: "/v1/admin/sales-chats/operator" });
}

/** A null `orgId` is the platform's own chat, which hears every organization. */
export function listSalesChats(orgId: string | null): Promise<SalesChatList> {
  return orgId === null ? listOperatorSalesChats() : listOrgSalesChats(orgId);
}

export function createSalesChatCode(orgId: string | null, body: SalesChatConnectCodeRequest, reason: string): Promise<SalesChatConnectCode> {
  const path = orgId === null ? "/v1/admin/sales-chats/operator/connect-codes" : `/v1/admin/organizations/${enc(orgId)}/sales-chats/connect-codes`;
  return authedFetch({ method: "POST", path, body, adminReason: reason });
}

export function patchSalesChat(id: string, body: SalesChatPatchRequest, reason: string): Promise<SalesChat> {
  return authedFetch({ method: "PATCH", path: `/v1/admin/sales-chats/${enc(id)}`, body, adminReason: reason });
}

export function deleteSalesChat(id: string, reason: string): Promise<Schemas["SalesChatDeleted"]> {
  return authedFetch({ method: "DELETE", path: `/v1/admin/sales-chats/${enc(id)}`, adminReason: reason });
}

// ---------------------------------------------------------------------------
// Forms
// ---------------------------------------------------------------------------

export interface AddChatForm {
  name: string;
  kinds: ChatKind[];
  reason: string;
}

export interface EditChatForm {
  name: string;
  kinds: ChatKind[];
  reason: string;
}

export function emptyAddChatForm(): AddChatForm {
  return { name: "", kinds: [], reason: "" };
}

export function editChatForm(chat: Pick<SalesChat, "name" | "kinds">): EditChatForm {
  return { name: chat.name, kinds: CHAT_KINDS.filter((k) => chat.kinds.includes(k)), reason: "" };
}

/** Toggles a kind in the screen's order. */
export function toggleKind(kinds: readonly ChatKind[], kind: ChatKind, on: boolean): ChatKind[] {
  const set = new Set(kinds);
  if (on) set.add(kind);
  else set.delete(kind);
  return CHAT_KINDS.filter((k) => set.has(k));
}

export type Built<T> = { ok: true; request: T; reason: string } | { ok: false; errors: string[] };

function commonProblems(name: string, kinds: readonly ChatKind[], reason: string): string[] {
  const errors: string[] = [];
  const n = name.trim();
  if (n === "") errors.push("Name: type a name for the chat, for example the name of the group.");
  else if ([...n].length > 200) errors.push("Name: use 200 characters or less.");
  if (kinds.length === 0) errors.push("Gets: select one box or more.");
  if (reason.trim() === "") errors.push("Cause: type the cause. We keep it in the audit log.");
  return errors;
}

export function buildAddChatRequest(form: AddChatForm): Built<SalesChatConnectCodeRequest> {
  const errors = commonProblems(form.name, form.kinds, form.reason);
  if (errors.length > 0) return { ok: false, errors };
  return { ok: true, reason: form.reason.trim(), request: { name: form.name.trim(), kinds: [...form.kinds] } };
}

/** Sends only what changed; an edit that changes nothing is refused here instead of by the server. */
export function buildEditChatRequest(chat: Pick<SalesChat, "name" | "kinds">, form: EditChatForm): Built<SalesChatPatchRequest> {
  const errors = commonProblems(form.name, form.kinds, form.reason);
  const body: SalesChatPatchRequest = {};
  if (form.name.trim() !== chat.name) body.name = form.name.trim();
  const before = CHAT_KINDS.filter((k) => chat.kinds.includes(k)).join(",");
  if (form.kinds.join(",") !== before) body.kinds = [...form.kinds];
  if (errors.length === 0 && Object.keys(body).length === 0) errors.push("No change. Edit the name or what the chat gets.");
  if (errors.length > 0) return { ok: false, errors };
  return { ok: true, reason: form.reason.trim(), request: body };
}

// ---------------------------------------------------------------------------
// Reading the list
// ---------------------------------------------------------------------------

/** "Connected", "Needs /connect", "Bot removed from the group", "Off". */
export function chatState(chat: Pick<SalesChat, "allowed" | "bot_removed_at" | "connected_at" | "chat_id">): { label: string; tone: "good" | "warn" | "bad" | "muted" } {
  if (chat.bot_removed_at != null) return { label: "Bot removed from the group", tone: "bad" };
  if (!chat.allowed) return { label: "Off", tone: "muted" };
  if (chat.connected_at == null && (chat.chat_id === "" || chat.chat_id == null)) return { label: "Needs /connect", tone: "warn" };
  return { label: "On", tone: "good" };
}

/** Whole seconds left until `expiresAt`, never below 0. */
export function secondsLeft(expiresAt: string, now: Date = new Date()): number {
  const ms = Date.parse(expiresAt) - now.getTime();
  return Number.isFinite(ms) ? Math.max(0, Math.floor(ms / 1000)) : 0;
}

/** "29:41", or "Expired" at 0. */
export function countdownLabel(seconds: number): string {
  if (seconds <= 0) return "Expired";
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

/** Local date and time of an instant; empty for none. */
export function whenLabel(iso: string | null | undefined): string {
  if (iso == null || iso === "") return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toISOString().replace("T", " ").slice(0, 16) + " UTC";
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

export function chatErrorMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return err instanceof Error ? err.message : "The request did not complete. Please try again.";
  switch (err.code) {
    case "sales_chat.invalid_kinds":
      return "Select Tickets, Shop sales or Delivery. Select one kind or more.";
    case "sales_chat.invalid_name":
      return "The name is empty or has more than 200 characters.";
    case "sales_chat.invalid_body":
      return "We did not understand the request. Reload the page and try again.";
    case "sales_chat.not_found":
      return "We did not find the chat, or we deleted it before. Reload the list.";
    case "sales_chat.organization_not_found":
      return "We did not find the organization.";
    case "org_module.superadmin_only":
      return "Only a platform superadmin can manage the Telegram chats.";
    case "superadmin.missing_reason":
      return "Type the cause. We keep it in the audit log.";
    default:
      return err.message;
  }
}
