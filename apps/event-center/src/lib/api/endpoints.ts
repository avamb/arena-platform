/**
 * One function per arena-api call of the event center. Every URL comes
 * from the API table (routes.ts), which the parity test checks against
 * openapi.yaml.
 */
import { call, download, type DownloadedFile } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type {
  ComplimentaryListResponse,
  CreatePromoCodeRequest,
  BotInvitationCreateResponse,
  BotInvitationResendResponse,
  BotInvitationRevokeResponse,
  BotTeamResponse,
  EventListResponse,
  EventSummary,
  MeResponse,
  OrderDetail,
  OrderLetter,
  OrderListResponse,
  OrderResendTicketsResponse,
  OrderSummary,
  OrganizationItem,
  OrgMeResponse,
  PaymentProviderConfigItem,
  SellerProfile,
  PromoterEnvelope,
  PromoterListEnvelope,
  PromoCodeEnvelope,
  PromoCodeListResponse,
  PromoRedemptionListResponse,
  SessionListResponse,
  SessionSummary,
  TicketTierListResponse,
} from "@/lib/api/types";

export function fetchMe(): Promise<MeResponse> {
  return call<MeResponse>({ route: API.me });
}

export function fetchOrgMe(orgId: string): Promise<OrgMeResponse> {
  return call<OrgMeResponse>({ route: API.orgMe, params: { org_id: orgId } });
}

export function listEvents(orgId: string, lang?: string): Promise<EventListResponse> {
  return call<EventListResponse>({ route: API.events, params: { org_id: orgId }, query: { lang } });
}

export function fetchEventSummary(orgId: string, eventId: string): Promise<EventSummary> {
  return call<EventSummary>({ route: API.eventSummary, params: { org_id: orgId, event_id: eventId } });
}

export function listSessions(orgId: string, eventId: string): Promise<SessionListResponse> {
  return call<SessionListResponse>({ route: API.eventSessions, params: { org_id: orgId, event_id: eventId } });
}

export function fetchSessionSummary(orgId: string, sessionId: string): Promise<SessionSummary> {
  return call<SessionSummary>({ route: API.sessionSummary, params: { org_id: orgId, session_id: sessionId } });
}

export function listTiers(orgId: string, eventId: string, sessionId: string): Promise<TicketTierListResponse> {
  return call<TicketTierListResponse>({
    route: API.sessionTiers,
    params: { org_id: orgId, event_id: eventId, session_id: sessionId },
  });
}

export type OrderTab = "recent" | "paid" | "unpaid";

export interface OrderQuery {
  tab?: OrderTab;
  q?: string;
  eventId?: string;
  sessionId?: string;
  limit?: number;
  offset?: number;
}

export function listOrders(orgId: string, query: OrderQuery): Promise<OrderListResponse> {
  return call<OrderListResponse>({
    route: API.orderSearch,
    params: { org_id: orgId },
    body: {
      tab: query.tab,
      q: query.q === "" ? undefined : query.q,
      event_id: query.eventId,
      session_id: query.sessionId,
      limit: query.limit,
      offset: query.offset,
    },
  });
}

export function fetchOrder(orgId: string, orderId: string): Promise<OrderDetail> {
  return call<OrderDetail>({ route: API.order, params: { org_id: orgId, id: orderId } });
}

export type CsvKind = "eventSales" | "sessionSales" | "sessionSummary";

export function downloadCsv(
  orgId: string,
  kind: CsvKind,
  id: string,
  locale: string,
): Promise<DownloadedFile> {
  const accept = "text/csv";
  switch (kind) {
    case "eventSales":
      return download({
        route: API.eventSalesCsv,
        params: { org_id: orgId, event_id: id },
        query: { locale },
        accept,
        fallbackName: "sales.csv",
      });
    case "sessionSales":
      return download({
        route: API.sessionSalesCsv,
        params: { org_id: orgId, session_id: id },
        query: { locale },
        accept,
        fallbackName: "sales.csv",
      });
    case "sessionSummary":
      return download({
        route: API.sessionSummaryCsv,
        params: { org_id: orgId, session_id: id },
        query: { locale },
        accept,
        fallbackName: "summary.csv",
      });
  }
}

export function downloadSampleTicket(orgId: string, sessionId: string, locale: string): Promise<DownloadedFile> {
  return download({
    route: API.sampleTicket,
    params: { org_id: orgId, session_id: sessionId },
    query: { locale },
    accept: "application/pdf",
    fallbackName: "sample-ticket.pdf",
  });
}

// 4-6
/** Every promoter of the organization, archived ones included (the screen filters). */
export function listPromoters(orgId: string): Promise<PromoterListEnvelope> {
  return call<PromoterListEnvelope>({ route: API.promoters, params: { org_id: orgId }, query: { include_archived: "true" } });
}

export interface PromoterCreateBody {
  name: string;
  legal_id?: string;
  phone?: string;
  email?: string;
  address?: string;
  website?: string;
}

export function createPromoter(orgId: string, body: PromoterCreateBody): Promise<PromoterEnvelope> {
  return call<PromoterEnvelope>({ route: API.promoterCreate, params: { org_id: orgId }, body });
}

/** Tri-state like the API: an absent key keeps the value, null clears it. */
export type PromoterPatch = Partial<{
  name: string;
  legal_id: string | null;
  phone: string | null;
  email: string | null;
  address: string | null;
  website: string | null;
  slug: string;
  archived: boolean;
}>;

export function updatePromoter(orgId: string, promoterId: string, body: PromoterPatch): Promise<PromoterEnvelope> {
  return call<PromoterEnvelope>({ route: API.promoterUpdate, params: { org_id: orgId, id: promoterId }, body });
}

// ── 4-3: promo codes ───────────────────────────────────────────────────

export function listPromoCodes(orgId: string): Promise<PromoCodeListResponse> {
  return call<PromoCodeListResponse>({ route: API.promoCodes, params: { org_id: orgId } });
}

export function fetchPromoCode(orgId: string, codeId: string): Promise<PromoCodeEnvelope> {
  return call<PromoCodeEnvelope>({ route: API.promoCode, params: { org_id: orgId, id: codeId } });
}

export function createPromoCode(orgId: string, body: CreatePromoCodeRequest): Promise<PromoCodeEnvelope> {
  return call<PromoCodeEnvelope>({ route: API.promoCodeCreate, params: { org_id: orgId }, body });
}

/** Pauses or activates a code. The body names only the status, so nothing else changes. */
export function setPromoStatus(orgId: string, codeId: string, status: "active" | "paused"): Promise<PromoCodeEnvelope> {
  return call<PromoCodeEnvelope>({
    route: API.promoCodeUpdate,
    params: { org_id: orgId, id: codeId },
    body: { status },
  });
}

/**
 * Replaces the sessions a code is for. An EMPTY list means every session of
 * the organization, future ones included (a club code). The body always names
 * the array: an absent or null one would leave the stored value alone.
 */
export function setPromoSessions(orgId: string, codeId: string, sessionIds: readonly string[]): Promise<PromoCodeEnvelope> {
  return call<PromoCodeEnvelope>({
    route: API.promoCodeUpdate,
    params: { org_id: orgId, id: codeId },
    body: { applies_to_session_ids: [...sessionIds] },
  });
}

export function deletePromoCode(orgId: string, codeId: string): Promise<unknown> {
  return call<unknown>({ route: API.promoCodeDelete, params: { org_id: orgId, id: codeId } });
}

export function listPromoRedemptions(orgId: string, codeId: string): Promise<PromoRedemptionListResponse> {
  return call<PromoRedemptionListResponse>({
    route: API.promoRedemptions,
    params: { org_id: orgId },
    query: { promo_code_id: codeId },
  });
}

export function downloadPromoCsv(orgId: string, codeId: string, locale: string): Promise<DownloadedFile> {
  return download({
    route: API.promoRedemptionsCsv,
    params: { org_id: orgId, promo_code_id: codeId },
    query: { locale },
    accept: "text/csv",
    fallbackName: "promo-usage.csv",
  });
}

/**
 * Sends the tickets of a paid order again (4-1). Without `email` the letter goes to
 * the order's own address. With it, the address is used once and kept 24 hours on
 * the delivery task: it never replaces the order's buyer e-mail.
 */
export function resendOrderTickets(orgId: string, orderId: string, email?: string): Promise<OrderResendTicketsResponse> {
  return call<OrderResendTicketsResponse>({
    route: API.resendTickets,
    params: { org_id: orgId, order_id: orderId },
    body: email !== undefined && email !== "" ? { email } : undefined,
  });
}

/**
 * Cancels a `pending_payment` order and ends the hold on its seats (4-2).
 * The caller has already checked the typed word. `headers` carries the
 * step-up token (`withStepUp`). A paid order answers 409 `orders.invalid_transition`.
 */
export function cancelOrder(orgId: string, orderId: string, headers: Record<string, string>): Promise<OrderSummary> {
  return call<OrderSummary>({ route: API.cancelOrder, params: { org_id: orgId, id: orderId }, body: {}, headers });
}

// ─── 4-7: the owner's "Organization data" ────────────────────────────────────

/** The organization with its legal data and contact (`org.read`). */
export function fetchOrganization(orgId: string): Promise<{ organization: OrganizationItem }> {
  return call<{ organization: OrganizationItem }>({ route: API.orgDetail, params: { id: orgId } });
}

/**
 * Changes the legal data. A field that is absent is kept, `null` clears it.
 * When a required legal field is left empty, the answer lists the channels
 * whose seller block was turned off (`seller_display_hidden`).
 */
export function patchOrganization(
  orgId: string,
  body: Record<string, unknown>,
): Promise<{ organization: OrganizationItem; seller_display_hidden?: string[] }> {
  return call<{ organization: OrganizationItem; seller_display_hidden?: string[] }>({
    route: API.orgUpdate,
    params: { id: orgId },
    body,
  });
}

export async function fetchSellerProfile(orgId: string): Promise<SellerProfile> {
  const out = await call<{ seller_profile: SellerProfile }>({ route: API.sellerProfile, params: { org_id: orgId } });
  return out.seller_profile;
}

/** Buyers' messages from the sales page's contact form also go to the sales chat. */
export async function patchSellerProfile(orgId: string, contactFormToTelegram: boolean): Promise<SellerProfile> {
  const out = await call<{ seller_profile: SellerProfile }>({
    route: API.sellerProfileUpdate,
    params: { org_id: orgId },
    body: { contact_form_to_telegram: contactFormToTelegram },
  });
  return out.seller_profile;
}

/**
 * The public contact of one sales-page channel. Only `public_contact` is
 * sent: `seller_hidden` is the platform operator's switch and the legacy
 * `show_seller` no longer decides anything.
 */
export async function patchSellerDisplay(
  orgId: string,
  channelId: string,
  publicContact: { email?: string | null; phone?: string | null },
): Promise<SellerProfile> {
  const out = await call<{ seller_profile: SellerProfile }>({
    route: API.sellerDisplayUpdate,
    params: { org_id: orgId, channel_id: channelId },
    body: { public_contact: publicContact },
  });
  return out.seller_profile;
}

export function listPaymentConfigs(orgId: string): Promise<{ payment_configs: PaymentProviderConfigItem[] }> {
  return call<{ payment_configs: PaymentProviderConfigItem[] }>({ route: API.paymentConfigs, params: { org_id: orgId } });
}

// ─── 4-4: invitations ───────────────────────────────────────────────────────

export type InvitationState = "valid" | "used" | "revoked";

export interface InvitationQuery {
  state?: InvitationState;
  limit?: number;
  offset?: number;
}

/** One page of the issued invitations, newest first, each with its `letters[]`. */
export function listInvitations(orgId: string, query: InvitationQuery): Promise<ComplimentaryListResponse> {
  return call<ComplimentaryListResponse>({
    route: API.complimentaryList,
    params: { org_id: orgId },
    query: { state: query.state, limit: query.limit, offset: query.offset },
  });
}

export interface InvitationInput {
  sessionId: string;
  tierId: string;
  email: string;
  name: string;
  /** `web-<operation>-<index>` (lib/invitations.ts batchId): a repeat replays, it never mails twice. */
  batchId: string;
}

export interface InvitationIssued {
  issuance: { id: string };
  tickets: { id: string; system_ticket_id: number }[];
  letters: OrderLetter[];
  idempotent_replay: boolean;
}

/** Issues ONE free ticket to ONE guest, so each invitation can be annulled on its own. */
export function issueInvitation(orgId: string, input: InvitationInput): Promise<InvitationIssued> {
  return call<InvitationIssued>({
    route: API.complimentaryIssue,
    params: { org_id: orgId },
    body: {
      session_id: input.sessionId,
      tier_id: input.tierId,
      qty: 1,
      recipients: [input.email],
      recipient_names: input.name === "" ? undefined : [input.name],
      batch_id: input.batchId,
      issued_by: "event_center_web",
    },
  });
}

/**
 * Annuls every ticket of an invitation: its barcode stops working and the
 * place goes back on sale. Arena does not tell the guest. 409
 * `complimentary.already_revoked` or `complimentary.scanned_ticket_requires_manual_review`.
 */
export function annulInvitation(issuanceId: string): Promise<void> {
  return call<void>({
    route: API.complimentaryRevoke,
    params: { id: issuanceId },
    body: { reason: "revoked from the web event center" },
  });
}

// ─── 4-5: the team ───────────────────────────────────────────────────────────

/** The organization's owners and managers with where each invitation stands. */
export function fetchTeam(orgId: string): Promise<BotTeamResponse> {
  return call<BotTeamResponse>({ route: API.botTeam, params: { org_id: orgId } });
}

export type InviteChannel = "web" | "telegram";
export type InviteRole = "owner" | "manager";

export interface InviteInput {
  email: string;
  role: InviteRole;
  channel: InviteChannel;
  /** The language of the letter: en, ru or es. */
  locale: string;
}

/**
 * Invites a person. The membership exists at once (a person who is already a
 * member keeps their role); `channel` decides what the letter opens.
 */
export function inviteMember(orgId: string, input: InviteInput): Promise<BotInvitationCreateResponse> {
  return call<BotInvitationCreateResponse>({ route: API.inviteMember, params: { org_id: orgId }, body: input });
}

/** Sends a Telegram invitation again. A web invitation answers 409. Too soon is 429. */
export function resendInvitation(orgId: string, invitationId: string, locale?: string): Promise<BotInvitationResendResponse> {
  return call<BotInvitationResendResponse>({
    route: API.resendInvitation,
    params: { org_id: orgId, id: invitationId },
    body: locale === undefined ? {} : { locale },
  });
}

/** Annuls an invitation. The membership it created goes too, when the person never took part. */
export function revokeInvitation(orgId: string, invitationId: string): Promise<BotInvitationRevokeResponse> {
  return call<BotInvitationRevokeResponse>({ route: API.revokeInvitation, params: { org_id: orgId, id: invitationId } });
}

/**
 * Removes ONE role of a member. The body names it: an empty body is 400
 * `membership.empty_body`. The role is the row's `membership_role`.
 * (The generated body type lists no `org_admin`, which the server accepts.)
 */
export function removeMember(orgId: string, userId: string, membershipRole: string): Promise<{ revoked: boolean }> {
  return call<{ revoked: boolean }>({
    route: API.removeMember,
    params: { org_id: orgId, user_id: userId },
    body: { role: membershipRole },
  });
}
