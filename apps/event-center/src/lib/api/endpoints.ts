/**
 * One function per arena-api call of the event center. Every URL comes
 * from the API table (routes.ts), which the parity test checks against
 * openapi.yaml.
 */
import { ApiError, PAGE_READ_TIMEOUT_MS, POSTER_READ_TIMEOUT_MS, UPLOAD_TIMEOUT_MS, call, download, type DownloadedFile } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { PageStatsResponse, SalesFunnelResponse } from "@/lib/api/types";
import type {
  BillingAccountInvoice,
  BillingAccountInvoiceList,
  BillingBalanceResponse,
  OrgBillingModulesResponse,
  BillingChargeList,
  BillingEventSummary,
  BillingLedgerResponse,
  BillingPackList,
  BillingPayByCardResponse,
  BillingPayerRequest,
  BillingPayerResponse,
  BillingServiceFeeQuote,
  ComplimentaryListResponse,
  CreateOrderRefundsRequest,
  CreateOrderRefundsResponse,
  CreatePromoCodeRequest,
  GrowthSettings,
  GrowthSettingsUpdate,
  MarketingSubscriberList,
  BotInvitationCreateResponse,
  BotInvitationResendResponse,
  BotInvitationRevokeResponse,
  BotTeamResponse,
  EventDeleteImpact,
  EventEmailNoteTestRequest,
  EventEmailNoteTestResponse,
  EventEnvelope,
  MediaObject,
  PosterFactsResponse,
  EventPageFactsResponse,
  EventContactResponse,
  EventListResponse,
  EventSummary,
  OrgSummary,
  GeoCitiesResponse,
  GeoCountriesResponse,
  GeoCountryCurrencyResponse,
  GeoCountryTimezonesResponse,
  ImportBil24SessionResponse,
  ImportEventBundleDryRunResponse,
  MeResponse,
  OrderDetail,
  SessionTicketList,
  OrderListResponse,
  OrderRefundOptions,
  OrderResendTicketsResponse,
  ReissueHistory,
  InvitationReissueOptions,
  ReissueOptions,
  ReissueResendResponse,
  ReissueTicketsRequest,
  ReissueTicketsResponse,
  ExternalRefundRequest,
  ExternalRefundResponse,
  RefundProblemQueue,
  OrderSummary,
  OrganizationItem,
  OrgMeResponse,
  PaymentProviderConfigItem,
  ReferralCapability,
  ReferralProgram,
  ReferralProgramRequest,
  ReferralProgramResponse,
  ReferralRewardList,
  ReferralSummary,
  ReferralWarning,
  SellerProfile,
  PromoterEnvelope,
  PromoterListEnvelope,
  PromoCodeEnvelope,
  PromoCodeListResponse,
  PromoRedemptionListResponse,
  SalesLink,
  SalesLinkCreateRequest,
  SalesLinkList,
  SalesLinkStats,
  SalesLinkUpdateRequest,
  SessionChangeImpact,
  SessionEnvelope,
  SessionSalesState,
  GuestListSettings,
  GuestListSubscription,
  SessionSalesStateHistory,
  SessionListResponse,
  SessionSummary,
  TicketTierEnvelope,
  TicketTierListResponse,
  TierPriceScheduleEnvelope,
  TierPriceWindowInput,
  UpdateTicketTierRequest,
  VenueItem,
  SeatingPlan,
  SeatingPlanVersion,
  TextFormatRequest,
  TextFormatResponse,
  AdminSeatsResponse,
  SeatBatch,
  SeatBatchList,
  SeatBatchRequest,
  SeatLayoutAlgorithmsResponse,
  SeatLayoutSuggestRequest,
  SeatLayoutSuggestResponse,
} from "@/lib/api/types";
import type {
  SmartPricePreviewResponse,
  SmartPriceRequest,
  SmartPriceRulesResponse,
} from "@/lib/api/types";
import type {
  StaffInvitationCancelRequest,
  StaffInvitationCancelResponse,
  InvitationBatch,
  InvitationBatchReport,
  StaffInvitationIssueRequest,
  StaffInvitationIssueResponse,
  StaffInvitationListResponse,
  StaffInvitationOrgListResponse,
} from "@/lib/api/types";
import type { SchemaResponse } from "@seatmap/types";
import type { SessionPatchBody } from "@/lib/sessionChange";

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

/** The range of the organization summary and of the accounting workbook; empty bounds are open. */
export interface OrgSummaryRange {
  /** First day, `YYYY-MM-DD`. */
  from?: string;
  /** Last day included, `YYYY-MM-DD`. */
  to?: string;
  eventId?: string;
}

/** EC-V1: a card per session and currency for the sessions that start in the range. */
export function fetchOrgSummary(orgId: string, range: OrgSummaryRange): Promise<OrgSummary> {
  return call<OrgSummary>({
    route: API.orgSummary,
    params: { org_id: orgId },
    query: { from: range.from, to: range.to, event_id: range.eventId },
  });
}

/** EC-V2: the accountant's Excel workbook (by sessions, by prices, tickets). */
export function downloadAccountingXlsx(orgId: string, range: OrgSummaryRange, locale: string): Promise<DownloadedFile> {
  return download({
    route: API.accountingXlsx,
    params: { org_id: orgId },
    query: { from: range.from, to: range.to, event_id: range.eventId, locale },
    accept: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    fallbackName: "accounting.xlsx",
  });
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

export type OrderTab = "recent" | "paid" | "unpaid" | "refunds";

export interface OrderQuery {
  tab?: OrderTab;
  q?: string;
  eventId?: string;
  sessionId?: string;
  /** The order date range, RFC 3339 (EC-V3). */
  from?: string;
  to?: string;
  /** The range of the session start, RFC 3339 (EC-V3). */
  sessionFrom?: string;
  sessionTo?: string;
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
      from: query.from,
      to: query.to,
      session_from: query.sessionFrom,
      session_to: query.sessionTo,
      limit: query.limit,
      offset: query.offset,
    },
  });
}

export function fetchOrder(orgId: string, orderId: string): Promise<OrderDetail> {
  return call<OrderDetail>({ route: API.order, params: { org_id: orgId, id: orderId } });
}

export type CsvKind = "eventSales" | "sessionSales" | "sessionSummary" | "sessionGuestList";

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
    case "sessionGuestList":
      return download({
        route: API.sessionGuestListCsv,
        params: { org_id: orgId, session_id: id },
        query: { locale },
        accept,
        fallbackName: "guests.csv",
      });
  }
}

/**
 * The guest list of a session as an Excel workbook: the file people open.
 * Excel opens a CSV with the list separator of its regional settings, so
 * with English settings every row of the CSV lands in column A; the CSV
 * (`downloadCsv(..., "sessionGuestList", ...)`) stays for imports.
 */
export function downloadGuestListXlsx(orgId: string, sessionId: string, locale: string): Promise<DownloadedFile> {
  return download({
    route: API.sessionGuestListXlsx,
    params: { org_id: orgId, session_id: sessionId },
    query: { locale },
    accept: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    fallbackName: "guests.xlsx",
  });
}

/** The guest list of every session of an event as one Excel workbook (a Session column first). */
export function downloadEventGuestListXlsx(orgId: string, eventId: string, locale: string): Promise<DownloadedFile> {
  return download({
    route: API.eventGuestListXlsx,
    params: { org_id: orgId, event_id: eventId },
    query: { locale },
    accept: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    fallbackName: "guests.xlsx",
  });
}

/** The organization switch for the automatic team guest list (on by default). */
export function getGuestListSettings(orgId: string): Promise<GuestListSettings> {
  return call<GuestListSettings>({ route: API.guestListSettings, params: { org_id: orgId } });
}

/** The owner turns the automatic team guest list on or off. */
export function setGuestListSettings(orgId: string, autoSend: boolean): Promise<GuestListSettings> {
  return call<GuestListSettings>({
    route: API.guestListSettingsSet,
    params: { org_id: orgId },
    body: { auto_send: autoSend },
  });
}

/** The caller's own "send me the guest list" setting in this organization. */
export function getGuestListSubscription(orgId: string): Promise<GuestListSubscription> {
  return call<GuestListSubscription>({ route: API.guestListSubscription, params: { org_id: orgId } });
}

/** Turns the caller's automatic guest list on or off. */
export function setGuestListSubscription(orgId: string, enabled: boolean): Promise<GuestListSubscription> {
  return call<GuestListSubscription>({
    route: API.guestListSubscriptionSet,
    params: { org_id: orgId },
    body: { enabled },
  });
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

// Spec 51-1
export function fetchGrowthSettings(orgId: string): Promise<GrowthSettings> {
  return call<GrowthSettings>({ route: API.growthSettings, params: { org_id: orgId } });
}

export function saveGrowthSettings(orgId: string, body: GrowthSettingsUpdate): Promise<GrowthSettings> {
  return call<GrowthSettings>({ route: API.growthSettingsUpdate, params: { org_id: orgId }, body });
}

export function listMarketingSubscribers(orgId: string, status: string, limit = 100): Promise<MarketingSubscriberList> {
  return call<MarketingSubscriberList>({
    route: API.marketingSubscribers,
    params: { org_id: orgId },
    query: { status, limit: String(limit) },
  });
}

export function downloadMarketingSubscribersCsv(orgId: string, status: string, locale: string): Promise<DownloadedFile> {
  return download({
    route: API.marketingSubscribersCsv,
    params: { org_id: orgId },
    query: { status, locale },
    accept: "text/csv",
    fallbackName: "subscribers.csv",
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

/** Sets the last moment a code works (an RFC 3339 instant); nothing else changes. */
export function setPromoValidUntil(orgId: string, codeId: string, validUntil: string): Promise<PromoCodeEnvelope> {
  return call<PromoCodeEnvelope>({
    route: API.promoCodeUpdate,
    params: { org_id: orgId, id: codeId },
    body: { valid_until: validUntil },
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

// ─── Spec 63: replace the tickets of an order ───────────────────────────────

/** The check screen of a ticket reissue: the tickets, the route, what blocks it now. */
export function fetchReissueOptions(orgId: string, orderId: string): Promise<ReissueOptions> {
  return call<ReissueOptions>({ route: API.reissueOptions, params: { org_id: orgId, order_id: orderId } });
}

/**
 * Replaces every active ticket of the order. `key` is the operation's
 * Idempotency-Key: the same key with the same body answers the first
 * operation (`replayed: true`) and writes nothing.
 */
export function reissueOrderTickets(
  orgId: string,
  orderId: string,
  body: ReissueTicketsRequest,
  key: string,
): Promise<ReissueTicketsResponse> {
  return call<ReissueTicketsResponse>({
    route: API.reissueTickets,
    params: { org_id: orgId, order_id: orderId },
    body,
    headers: { "Idempotency-Key": key },
  });
}

/** Queues the letter of a reissue that was not applied yet again, to the same address. */
export function resendReissueLetter(orgId: string, orderId: string, reissueId: string): Promise<ReissueResendResponse> {
  return call<ReissueResendResponse>({
    route: API.reissueResend,
    params: { org_id: orgId, order_id: orderId, reissue_id: reissueId },
    body: {},
  });
}

/** The check screen of an invitation that has no order (spec 63 R-5). */
export function fetchInvitationReissueOptions(orgId: string, invitationId: string): Promise<InvitationReissueOptions> {
  return call<InvitationReissueOptions>({ route: API.invitationReissueOptions, params: { org_id: orgId, id: invitationId } });
}

/** Replaces the tickets of an invitation; the same key rules as an order. */
export function reissueInvitationTickets(
  orgId: string,
  invitationId: string,
  body: ReissueTicketsRequest,
  key: string,
): Promise<ReissueTicketsResponse> {
  return call<ReissueTicketsResponse>({
    route: API.invitationReissueTickets,
    params: { org_id: orgId, id: invitationId },
    body,
    headers: { "Idempotency-Key": key },
  });
}

/** Queues the letter of an invitation's reissue again. */
export function resendInvitationReissueLetter(orgId: string, invitationId: string, reissueId: string): Promise<ReissueResendResponse> {
  return call<ReissueResendResponse>({
    route: API.invitationReissueResend,
    params: { org_id: orgId, id: invitationId, reissue_id: reissueId },
    body: {},
  });
}

/** The reissues of an order, newest first. */
export function fetchReissueHistory(orgId: string, orderId: string): Promise<ReissueHistory> {
  return call<ReissueHistory>({ route: API.reissueHistory, params: { org_id: orgId, order_id: orderId } });
}

/**
 * Cancels a `pending_payment` order and ends the hold on its seats (4-2).
 * The caller has already checked the typed word. `headers` carries the
 * step-up token (`withStepUp`). A paid order answers 409 `orders.invalid_transition`.
 */
export function cancelOrder(orgId: string, orderId: string, headers: Record<string, string>): Promise<OrderSummary> {
  return call<OrderSummary>({ route: API.cancelOrder, params: { org_id: orgId, id: orderId }, body: {}, headers });
}

// ─── 8-1: refunds of an order ─────────────────────────────────────────────────

/** Failed and stuck refunds with undelivered buyer letters (`refund.read`, EC-V5). */
export function fetchRefundQueue(orgId: string): Promise<RefundProblemQueue> {
  return call<RefundProblemQueue>({ route: API.refundQueue, params: { org_id: orgId } });
}

/**
 * Retries one failed refund (`refund.approve`, 202). The server starts the
 * "retry-<id>" operation, so a repeated press answers the first one. `headers`
 * carries the step-up token (`withStepUp`).
 */
export function retryRefund(orgId: string, refundId: string, headers: Record<string, string>): Promise<CreateOrderRefundsResponse> {
  return call<CreateOrderRefundsResponse>({
    route: API.refundRetry,
    params: { org_id: orgId, refund_id: refundId },
    body: {},
    headers,
  });
}

/** What can be refunded on an order and how (`refund.read`). Changes nothing. */
export function fetchRefundOptions(orgId: string, orderId: string): Promise<OrderRefundOptions> {
  return call<OrderRefundOptions>({ route: API.refundOptions, params: { org_id: orgId, order_id: orderId } });
}

/**
 * The server gives a refund up to two minutes for the provider's answer
 * (refundWorkBudget); the web waits a little longer before it calls the
 * outcome unknown and offers to check with the same key.
 */
export const REFUND_CREATE_TIMEOUT_MS = 130_000;

/**
 * Records one refund operation (202). `key` is the operation's Idempotency-Key:
 * the same key with the same body answers the first operation and writes
 * nothing. `headers` carries the step-up token (`withStepUp`).
 */
export async function createOrderRefunds(
  orgId: string,
  orderId: string,
  body: CreateOrderRefundsRequest,
  key: string,
  headers: Record<string, string>,
): Promise<CreateOrderRefundsResponse> {
  const answer = await call<CreateOrderRefundsResponse>({
    route: API.refundCreate,
    params: { org_id: orgId, order_id: orderId },
    body,
    headers: { ...headers, "Idempotency-Key": key },
    timeoutMs: REFUND_CREATE_TIMEOUT_MS,
  });
  // An HTTP success alone does not prove which refund operation was recorded.
  // Keep the idempotency key if a proxy corrupts or truncates the response.
  if (
    answer === null || typeof answer !== "object" || answer.order_id !== orderId ||
    typeof answer.batch_id !== "string" || answer.batch_id === "" || typeof answer.replayed !== "boolean" ||
    !Array.isArray(answer.refunds) || answer.refunds.length === 0 ||
    !answer.refunds.every((item) => item !== null && typeof item === "object" &&
      typeof item.id === "string" && item.id !== "" && typeof item.state === "string" &&
      typeof item.currency === "string" && Number.isSafeInteger(item.amount) && item.amount >= 0)
  ) {
    throw new ApiError(200, "refund.unconfirmed_response", "The refund response could not be verified.");
  }
  return answer;
}

// ─── P-14: a refund made outside the platform ────────────────────────────────

/**
 * Records money the organizer returned in the provider's own cabinet: voids the
 * named tickets and books one FINAL amount per ticket (spec 62 14.6). `headers`
 * carries the step-up token (`withStepUp`). A ticket that already has a refund
 * answers 409 `order.external_refund_exists` with the recorded amount.
 */
export function recordExternalRefund(
  orgId: string,
  orderId: string,
  body: ExternalRefundRequest,
  headers: Record<string, string>,
): Promise<ExternalRefundResponse> {
  return call<ExternalRefundResponse>({
    route: API.externalRefund,
    params: { org_id: orgId, order_id: orderId },
    body,
    headers,
  });
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
/** The organization-wide switches of the seller profile; send only what changes. */
export type SellerProfilePatch = {
  contact_form_to_telegram?: boolean;
  provider_receipt_enabled?: boolean;
};

export async function patchSellerProfile(orgId: string, patch: SellerProfilePatch): Promise<SellerProfile> {
  const out = await call<{ seller_profile: SellerProfile }>({
    route: API.sellerProfileUpdate,
    params: { org_id: orgId },
    body: patch,
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

// ─── Spec 46: staff invitations on chosen seats ──────────────────────────────

/** Every staff invitation of the session, whoever issued it (needs `complimentary.read`). */
export function listStaffInvitations(orgId: string, sessionId: string): Promise<StaffInvitationListResponse> {
  return call<StaffInvitationListResponse>({
    route: API.staffInvitationsList,
    params: { org_id: orgId, session_id: sessionId },
  });
}

export type OrgInvitationState = "active" | "cancelled" | "all";

export interface OrgInvitationQuery {
  sessionId?: string;
  eventId?: string;
  /** Default on the server: active (an invitation whose tickets are not issued yet counts as active). */
  state?: OrgInvitationState;
  limit?: number;
  /** The `next_cursor` of the previous page. */
  cursor?: string;
}

/** A page of the staff invitations of the whole organization, newest first (needs `complimentary.read`). */
export function listOrgStaffInvitations(orgId: string, q: OrgInvitationQuery): Promise<StaffInvitationOrgListResponse> {
  return call<StaffInvitationOrgListResponse>({
    route: API.staffInvitationsOrgList,
    params: { org_id: orgId },
    query: { session_id: q.sessionId, event_id: q.eventId, state: q.state, limit: q.limit, cursor: q.cursor },
  });
}

/**
 * Issues an invitation on chosen seats. 201 for a new one, 200 with `replayed`
 * for a repeat of the same `request_id`. 409 `invitation.seats_taken` carries
 * `details.seat_keys`.
 */
export function issueStaffInvitation(
  orgId: string,
  sessionId: string,
  body: StaffInvitationIssueRequest,
): Promise<StaffInvitationIssueResponse> {
  return call<StaffInvitationIssueResponse>({
    route: API.staffInvitationIssue,
    params: { org_id: orgId, session_id: sessionId },
    body,
  });
}

/** Cancels the whole invitation (no `ticket_ids`). 409 `invitation.tickets_not_issued` while the tickets are minted. */
export function cancelStaffInvitation(
  orgId: string,
  orderId: string,
  body: StaffInvitationCancelRequest,
): Promise<StaffInvitationCancelResponse> {
  return call<StaffInvitationCancelResponse>({
    route: API.staffInvitationCancel,
    params: { org_id: orgId, order_id: orderId },
    body,
  });
}

/** One PDF with every ticket of the invitation (INV-03). */
export function downloadStaffInvitationPdf(orgId: string, orderId: string, systemId: number): Promise<DownloadedFile> {
  return download({
    route: API.staffInvitationPdf,
    params: { org_id: orgId, order_id: orderId },
    accept: "application/pdf",
    fallbackName: `invitation-${systemId}.pdf`,
  });
}

/**
 * Uploads a CSV file of invitations (INV-06b). `dryRun` only checks the file and answers the report
 * (nothing is written, no request id). The real run needs a `requestId`: a repeat of the SAME id, file
 * and delivery answers the first batch (200, `replayed`), so a lost answer is retried with it unchanged.
 */
export function createInvitationBatch(
  orgId: string,
  sessionId: string,
  input: { file: Blob; fileName: string; delivery: string; dryRun: boolean; requestId?: string },
  signal?: AbortSignal,
): Promise<InvitationBatchReport | InvitationBatch> {
  const form = new FormData();
  form.append("delivery", input.delivery);
  if (!input.dryRun && input.requestId !== undefined) form.append("request_id", input.requestId);
  form.append("file", input.file, input.fileName);
  return call<InvitationBatchReport | InvitationBatch>({
    route: API.invitationBatchCreate,
    params: { org_id: orgId, session_id: sessionId },
    query: input.dryRun ? { dry_run: "true" } : undefined,
    form,
    signal,
    // The server works up to 60 s on a file: the default 30 s would give up on a request that is still running.
    timeoutMs: UPLOAD_TIMEOUT_MS,
  });
}

/** One issued batch with the order of each guest (`complimentary.read`). */
export function getInvitationBatch(orgId: string, batchId: string): Promise<InvitationBatch> {
  return call<InvitationBatch>({ route: API.invitationBatchGet, params: { org_id: orgId, id: batchId } });
}

/** The result file of a batch: one line per guest with the order number. */
export function downloadInvitationBatchResult(orgId: string, batchId: string): Promise<DownloadedFile> {
  return download({
    route: API.invitationBatchResult,
    params: { org_id: orgId, id: batchId },
    accept: "text/csv",
    fallbackName: `invitation-batch-${batchId.slice(0, 8)}.csv`,
  });
}

/** Sends the letter of the invitation again (INV-03). */
export function resendStaffInvitation(orgId: string, orderId: string): Promise<void> {
  return call<void>({ route: API.staffInvitationResend, params: { org_id: orgId, order_id: orderId }, body: {} });
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

// ─── 5-1: the status of an event ─────────────────────────────────────────────

/**
 * Moves an event along its lifecycle (`event.publish`). Re-applying the same
 * status is a silent 200. 422: `event.invalid_transition`,
 * `event.publish_requires_session`, `event.publish_requires_priced_tier`.
 */
export function updateEventStatus(orgId: string, eventId: string, status: string): Promise<EventEnvelope> {
  return call<EventEnvelope>({ route: API.eventStatus, params: { org_id: orgId, id: eventId }, body: { status } });
}

/** The dry run of a delete: what it would touch and whether it is allowed. Writes nothing. */
export function fetchEventDeleteImpact(orgId: string, eventId: string): Promise<EventDeleteImpact> {
  return call<EventDeleteImpact>({ route: API.eventDeleteImpact, params: { org_id: orgId, event_id: eventId } });
}

/** Deletes an event that sold nothing. 409 `event.has_paid_orders` otherwise. */
export function deleteEvent(orgId: string, eventId: string): Promise<unknown> {
  return call<unknown>({ route: API.eventDelete, params: { org_id: orgId, id: eventId } });
}

// 5-3: the sales end and doors-open time of a session. One PATCH names ONE
// of the two (the body builders are in lib/saleTimes.ts). No journal row and
// no letter: neither time is a buyer-visible change.
/** The session's sales state, its places and the delivery of its latest change to the sites. */
export function getSessionSalesState(orgId: string, sessionId: string): Promise<SessionSalesState> {
  return call<SessionSalesState>({ route: API.sessionSalesState, params: { org_id: orgId, session_id: sessionId } });
}

/**
 * Sets "soon" or "sold_out" for every channel, or lifts the state with "open".
 * Reversible and no letter goes out, so the screen asks with one press.
 */
export function setSessionSalesState(
  orgId: string,
  sessionId: string,
  state: "open" | "soon" | "sold_out",
): Promise<SessionSalesState> {
  return call<SessionSalesState>({
    route: API.sessionSalesStateSet,
    params: { org_id: orgId, session_id: sessionId },
    body: { state },
  });
}

/** Who switched the sales state and when, newest first. */
export function listSessionSalesStateHistory(orgId: string, sessionId: string): Promise<SessionSalesStateHistory> {
  return call<SessionSalesStateHistory>({
    route: API.sessionSalesStateHistory,
    params: { org_id: orgId, session_id: sessionId },
    query: { limit: 10 },
  });
}

export function updateSessionSaleTimes(
  orgId: string,
  eventId: string,
  sessionId: string,
  body: { sales_end_at: string } | { doors_open_at: string | null },
): Promise<SessionEnvelope> {
  return call<SessionEnvelope>({
    route: API.sessionUpdate,
    params: { org_id: orgId, event_id: eventId, id: sessionId },
    body,
  });
}

// ─── 5-4: a date's categories ────────────────────────────────────────────────

export interface TierRef {
  orgId: string;
  eventId: string;
  sessionId: string;
  tierId: string;
}

const tierParams = (r: TierRef) => ({
  org_id: r.orgId,
  event_id: r.eventId,
  session_id: r.sessionId,
  id: r.tierId,
});

/**
 * Changes ONE thing of a category. The body is exactly the keys to change:
 * an absent key keeps the stored value, an explicit null clears a sale-window
 * edge (UpdateTicketTierRequest is tri-state for those two).
 */
export function updateTier(ref: TierRef, body: UpdateTicketTierRequest): Promise<TicketTierEnvelope> {
  return call<TicketTierEnvelope>({ route: API.tierUpdate, params: tierParams(ref), body });
}

/** The category's price windows, to find the one in force now. */
export function fetchTierSchedule(ref: TierRef): Promise<TierPriceScheduleEnvelope> {
  return call<TierPriceScheduleEnvelope>({ route: API.tierSchedule, params: tierParams(ref) });
}

// ─── Smart price (spec 51 §4.2) ─────────────────────────────────────────────

/** The session's smart price presets, with where an active one stands now. */
export function listSmartPrice(orgId: string, sessionId: string): Promise<SmartPriceRulesResponse> {
  return call<SmartPriceRulesResponse>({ route: API.smartPriceList, params: { org_id: orgId, session_id: sessionId } });
}

/** The steps or price windows a preset would write. Writes nothing. */
export function previewSmartPrice(orgId: string, sessionId: string, body: SmartPriceRequest): Promise<SmartPricePreviewResponse> {
  return call<SmartPricePreviewResponse>({ route: API.smartPricePreview, params: { org_id: orgId, session_id: sessionId }, body });
}

/** Turns a preset on. The server writes it through the event-bundle import. */
export function enableSmartPrice(orgId: string, sessionId: string, body: SmartPriceRequest): Promise<SmartPriceRulesResponse> {
  return call<SmartPriceRulesResponse>({ route: API.smartPriceEnable, params: { org_id: orgId, session_id: sessionId }, body });
}

/** Freezes the price where it is now. */
export function stopSmartPrice(orgId: string, sessionId: string, ruleId: string): Promise<SmartPriceRulesResponse> {
  return call<SmartPriceRulesResponse>({
    route: API.smartPriceStop,
    params: { org_id: orgId, session_id: sessionId, rule_id: ruleId },
  });
}

/** Replaces the whole schedule. Send every window back, the changed one included. */
export function putTierSchedule(ref: TierRef, windows: TierPriceWindowInput[]): Promise<TierPriceScheduleEnvelope> {
  return call<TierPriceScheduleEnvelope>({
    route: API.tierSchedulePut,
    params: tierParams(ref),
    body: { windows },
  });
}

// ─── 6-1a: the event wizard ──────────────────────────────────────────────────

/** The organization's active venues (`venue.read`). */
export function listVenues(orgId: string): Promise<{ venues: VenueItem[] }> {
  return call<{ venues: VenueItem[] }>({ route: API.venues, params: { org_id: orgId } });
}

/** The seating plans of a venue (`seating_plan.read`). */
export function listVenueSeatingPlans(venueId: string): Promise<{ seating_plans: SeatingPlan[] }> {
  return call<{ seating_plans: SeatingPlan[] }>({ route: API.venueSeatingPlans, params: { venue_id: venueId } });
}

/** The versions of a seating plan, each with its geometry (`seating_plan.read`). */
export function listSeatingPlanVersions(planId: string): Promise<{ seating_plan_versions: SeatingPlanVersion[] }> {
  return call<{ seating_plan_versions: SeatingPlanVersion[] }>({ route: API.seatingPlanVersions, params: { id: planId } });
}

export function listCountries(lang?: string): Promise<GeoCountriesResponse> {
  return call<GeoCountriesResponse>({ route: API.geoCountries, query: { lang } });
}

/** Cities of a country that start with `q`, and places of the GeoNames data as suggestions. */
export function searchCities(iso2: string, q: string, lang?: string): Promise<GeoCitiesResponse> {
  return call<GeoCitiesResponse>({ route: API.geoCities, query: { country: iso2, q, lang } });
}

/** The zones of a country. 404 `geo.country_not_found` for a country the data does not know. */
export function countryTimezones(iso2: string): Promise<GeoCountryTimezonesResponse> {
  return call<GeoCountryTimezonesResponse>({ route: API.geoTimezones, params: { iso2 } });
}

/** The currency of a country. 404 `geo.country_not_found` for a country the platform has no row for. */
export function countryCurrency(iso2: string): Promise<GeoCountryCurrencyResponse> {
  return call<GeoCountryCurrencyResponse>({ route: API.geoCurrency, params: { iso2 } });
}

/**
 * Checks an event bundle with the import's own code and writes NOTHING
 * (`?dry_run=true`, task 3-1). The answer lists every error with the JSON
 * pointer of its field. It holds a transaction like an import, so a caller
 * asks at most once a second (lib/wizard/validation.ts, createScheduler).
 */
export function checkEventBundle(orgId: string, bundle: unknown): Promise<ImportEventBundleDryRunResponse> {
  return call<ImportEventBundleDryRunResponse>({
    route: API.eventBundleCheck,
    params: { org_id: orgId },
    query: { dry_run: "true" },
    body: bundle,
  });
}

/**
 * Creates the event: the SAME route as the check, without `dry_run`. The
 * bundle's `externalRef` is the idempotency key (a repeat edits the session it
 * made, 200 `created: false`), and `publish: false` keeps the event a draft.
 * Never retried by the client on its own (lib/wizard/save.ts decides).
 */
export function createEventFromBundle(orgId: string, bundle: unknown): Promise<ImportBil24SessionResponse> {
  return call<ImportBil24SessionResponse>({
    route: API.eventBundleCheck,
    params: { org_id: orgId },
    body: bundle,
  });
}

// ─── 7-1: the poster of an event ─────────────────────────────────────────────

/**
 * Uploads a picture as an event poster of the organization (`media.write`).
 * Multipart, one `file` part, `owner_type=event_poster`, `org_id` and the
 * `content_type` the web itself proved from the bytes. The answer is NOT flat:
 * `{ media_object }`. The file is named `poster.jpg|png`, never with the
 * person's own file name.
 */
export function uploadPosterMedia(
  orgId: string,
  file: Blob,
  contentType: string,
  signal?: AbortSignal,
): Promise<{ media_object: MediaObject }> {
  const form = new FormData();
  form.append("owner_type", "event_poster");
  form.append("org_id", orgId);
  form.append("content_type", contentType);
  form.append("file", file, contentType === "image/png" ? "poster.png" : "poster.jpg");
  return call<{ media_object: MediaObject }>({ route: API.mediaUpload, form, signal, timeoutMs: UPLOAD_TIMEOUT_MS });
}

/**
 * Asks the server to read the uploaded poster (`event.update` in this organization).
 * ONE request on its own long timeout: the caller never repeats it by itself, because
 * an answer that is lost may still have used one of the organization's daily readings.
 * Nothing is stored by the server; the answer is only a set of hints.
 */
export function readPosterFacts(orgId: string, mediaId: string, signal?: AbortSignal): Promise<PosterFactsResponse> {
  return call<PosterFactsResponse>({
    route: API.posterFacts,
    params: { org_id: orgId, media_id: mediaId },
    signal,
    timeoutMs: POSTER_READ_TIMEOUT_MS,
  });
}

/**
 * Asks the server to read the organizer's OWN event page (`event.update`, spec 59).
 * The server fetches the address, never the browser. ONE request on its own long
 * timeout, never repeated by itself: a lost answer may have used a daily reading.
 * The answer is only a set of hints; the page's picture is stored unattached.
 */
export function readEventPageFacts(orgId: string, url: string, signal?: AbortSignal): Promise<EventPageFactsResponse> {
  return call<EventPageFactsResponse>({
    route: API.eventPageFacts,
    params: { org_id: orgId },
    body: { url },
    signal,
    timeoutMs: PAGE_READ_TIMEOUT_MS,
  });
}

/** A media object with a signed address valid for about seven minutes (`media.read`). */
export function fetchMediaObject(mediaId: string, signal?: AbortSignal): Promise<{ media_object: MediaObject }> {
  return call<{ media_object: MediaObject }>({ route: API.mediaGet, params: { id: mediaId }, signal });
}

/**
 * Makes an uploaded picture the event's poster: ONE field of the PATCH
 * (`event.update`). `clear_session_overrides` is never sent: a date that has
 * its own poster keeps it.
 */
export function attachEventPoster(
  orgId: string,
  eventId: string,
  mediaId: string,
  signal?: AbortSignal,
): Promise<EventEnvelope> {
  return call<EventEnvelope>({
    route: API.eventUpdate,
    params: { org_id: orgId, id: eventId },
    body: { poster_media_id: mediaId },
    signal,
  });
}

// ─── RT-11: formatted texts ──────────────────────────────────────────────────

/**
 * The editor's "Format" button (`event.update` in this organization): the plain text goes to the server, a model
 * adds only the marks and the code proves that no word changed. ONE request, never repeated by itself (it costs a
 * daily slot). A failure of the model is NOT an error here: the answer is 200 with `ai_applied: false` and the
 * plain conversion of the text.
 */
export function formatText(orgId: string, body: TextFormatRequest, signal?: AbortSignal): Promise<TextFormatResponse> {
  return call<TextFormatResponse>({ route: API.textsFormat, params: { org_id: orgId }, body, signal });
}

/**
 * Saves or removes the organizer's own text of the buyer letters: ONE PATCH of the event with ART and its format.
 * `null` removes the text. The answer does not carry the text (only the events list does), so the caller reads
 * the list again.
 */
export function saveEmailNote(orgId: string, eventId: string, art: string | null): Promise<EventEnvelope> {
  return call<EventEnvelope>({
    route: API.eventUpdate,
    params: { org_id: orgId, id: eventId },
    body: art === null ? { email_note: null } : { email_note: art, email_note_format: "art" },
  });
}

/**
 * Saves the description of an event: ONE PATCH with `description` and `description_format: "art"` and nothing else, so the
 * name, the poster, the dates, the categories and the translations stay as they are (the server only converts the
 * translated descriptions that are still plain text to ART, word for word). An empty text clears the description.
 * Used by the Telegram mini app editor (spec 44, RT-12b). The answer does not carry the format, the caller reads the
 * events list again.
 */
export function saveDescription(orgId: string, eventId: string, art: string): Promise<EventEnvelope> {
  return call<EventEnvelope>({
    route: API.eventUpdate,
    params: { org_id: orgId, id: eventId },
    body: { description: art, description_format: "art" },
  });
}

/** A test letter with the text (the draft when `note` is given, else the saved one) to the caller's own address. */
export function sendEmailNoteTest(
  orgId: string,
  eventId: string,
  body: EventEmailNoteTestRequest,
): Promise<EventEmailNoteTestResponse> {
  return call<EventEmailNoteTestResponse>({ route: API.emailNoteTest, params: { org_id: orgId, event_id: eventId }, body });
}

// ─── 5-2: move or cancel a session ───────────────────────────────────────────

/** The dry run of a move or a cancellation. Writes nothing. See impactQuery(). */
export function fetchChangeImpact(
  orgId: string,
  sessionId: string,
  query: Record<string, string>,
): Promise<SessionChangeImpact> {
  return call<SessionChangeImpact>({
    route: API.sessionChangeImpact,
    params: { org_id: orgId, session_id: sessionId },
    query,
  });
}

/** The ONE write of a move or a cancellation: a PATCH of the existing session. */
export function patchSession(orgId: string, eventId: string, sessionId: string, body: SessionPatchBody): Promise<SessionEnvelope> {
  return call<SessionEnvelope>({ route: API.sessionUpdate, params: { org_id: orgId, event_id: eventId, id: sessionId }, body });
}

/** The e-mail buyers answer to when a session changes (the event's promoter, else the organization). */
export function setEventContact(
  orgId: string,
  eventId: string,
  input: { email: string; phone: string | null; phone_hidden: boolean },
): Promise<EventContactResponse> {
  return call<EventContactResponse>({ route: API.eventContact, params: { org_id: orgId, event_id: eventId }, body: input });
}
// M2
export interface SessionTicketsQuery {
  /** false = active tickets nobody entered with, true = tickets with an entry, absent = all. */
  entered?: boolean;
  limit: number;
  offset: number;
}

/** One page of a session's tickets with the buyer and the entry at the door. */
export function fetchSessionTickets(orgId: string, sessionId: string, q: SessionTicketsQuery): Promise<SessionTicketList> {
  return call<SessionTicketList>({
    route: API.sessionTickets,
    params: { org_id: orgId, session_id: sessionId },
    query: { entered: q.entered === undefined ? undefined : String(q.entered), limit: q.limit, offset: q.offset },
  });
}

// ─── V1: the VIP mark of a ticket ─────────────────────────────────────────────

/**
 * Sets or clears the organizer's private VIP mark of one ticket
 * (`ticket.update`). Idempotent: the value is the wanted one, not a toggle.
 */
export function setTicketVip(orgId: string, ticketId: string, vip: boolean): Promise<{ ticket_id: string; vip: boolean; changed: boolean }> {
  return call<{ ticket_id: string; vip: boolean; changed: boolean }>({
    route: API.ticketVip,
    params: { org_id: orgId, ticket_id: ticketId },
    body: { vip },
  });
}

// 2-3: statistics of the event's hosted page. The window is the API's default
// (the last 30 days, ending today in the venue's zone).
export function fetchPageStats(orgId: string, eventId: string): Promise<PageStatsResponse> {
  return call<PageStatsResponse>({ route: API.eventPageStats, params: { org_id: orgId, event_id: eventId } });
}

// A7: the sales funnel of an event. Without a window the API answers the last
// 30 days ending today in the venue's zone; a session limits it to that date.
export function fetchEventFunnel(
  orgId: string,
  eventId: string,
  window: { from?: string; to?: string; sessionId?: string } = {},
): Promise<SalesFunnelResponse> {
  return call<SalesFunnelResponse>({
    route: API.eventFunnel,
    params: { org_id: orgId, event_id: eventId },
    query: {
      from: window.from === "" ? undefined : window.from,
      to: window.to === "" ? undefined : window.to,
      session_id: window.sessionId === "" ? undefined : window.sessionId,
    },
  });
}

export function downloadPageStatsCsv(orgId: string, eventId: string, locale: string): Promise<DownloadedFile> {
  return download({
    route: API.eventPageStatsCsv,
    params: { org_id: orgId, event_id: eventId },
    query: { locale },
    accept: "text/csv",
    fallbackName: "page-stats.csv",
  });
}
// ─── B2-7: billing usage (read-only, preliminary) ───────────────────────────

export interface BillingChargesQuery {
  /** Inclusive start of the event time, RFC 3339. */
  from?: string;
  /** Exclusive end of the event time, RFC 3339. */
  to?: string;
  eventId?: string;
  limit?: number;
  cursor?: string;
}

/** One page of the charges, newest event first, with the sums of the whole filter. */
export function listBillingCharges(orgId: string, query: BillingChargesQuery): Promise<BillingChargeList> {
  return call<BillingChargeList>({
    route: API.billingCharges,
    params: { org_id: orgId },
    query: { from: query.from, to: query.to, event_id: query.eventId, limit: query.limit, cursor: query.cursor },
  });
}

/** The charge summary of one event: per currency, per rule, per session. */
export function fetchBillingEventSummary(orgId: string, eventId: string): Promise<BillingEventSummary> {
  return call<BillingEventSummary>({ route: API.billingEventSummary, params: { org_id: orgId, event_id: eventId } });
}

// ─── B3-3/B4: balance, packs, invoices, journal, commission quote, payer ────

/** The credit balance of each billing account (one per currency). */
export function fetchBillingBalance(orgId: string): Promise<BillingBalanceResponse> {
  return call<BillingBalanceResponse>({ route: API.billingBalance, params: { org_id: orgId } });
}

/** The paid modules of the organization (the shop) with the price that applies today; read only (spec 61). */
export function fetchBillingModules(orgId: string): Promise<OrgBillingModulesResponse> {
  return call<OrgBillingModulesResponse>({ route: API.billingModules, params: { org_id: orgId } });
}

/** The credit packs on offer, with the fee a card payment adds. */
export function listBillingPacks(orgId: string): Promise<BillingPackList> {
  return call<BillingPackList>({ route: API.billingPacks, params: { org_id: orgId } });
}

/**
 * Orders a pack: an issued invoice (201), or the first one again (200) when
 * `key` was used before. `key` is the Idempotency-Key of ONE confirmed order.
 */
export function orderBillingPack(
  orgId: string,
  packId: string,
  paymentMethod: "bank_transfer" | "stripe_card",
  key: string,
): Promise<BillingAccountInvoice> {
  return call<BillingAccountInvoice>({
    route: API.billingPackOrder,
    params: { org_id: orgId, pack_id: packId },
    body: { payment_method: paymentMethod },
    headers: { "Idempotency-Key": key },
  });
}

export interface AccountInvoicesQuery {
  state?: string;
  limit?: number;
  offset?: number;
}

/** A page of the organization's platform invoices, newest first. */
export function listAccountInvoices(orgId: string, query: AccountInvoicesQuery): Promise<BillingAccountInvoiceList> {
  return call<BillingAccountInvoiceList>({
    route: API.billingAccountInvoices,
    params: { org_id: orgId },
    query: { state: query.state, limit: query.limit, offset: query.offset },
  });
}

export function fetchAccountInvoice(orgId: string, invoiceId: string): Promise<BillingAccountInvoice> {
  return call<BillingAccountInvoice>({ route: API.billingAccountInvoice, params: { org_id: orgId, invoice_id: invoiceId } });
}

/** The Stripe payment page of an issued card invoice. */
export function payInvoiceByCard(orgId: string, invoiceId: string): Promise<BillingPayByCardResponse> {
  return call<BillingPayByCardResponse>({
    route: API.billingInvoicePayByCard,
    params: { org_id: orgId, invoice_id: invoiceId },
  });
}

/** The credit journal of one account, newest first; `before` is the previous page's `next_before`. */
export function listBillingLedger(
  orgId: string,
  query: { accountId?: string; before?: number; limit?: number },
): Promise<BillingLedgerResponse> {
  return call<BillingLedgerResponse>({
    route: API.billingLedger,
    params: { org_id: orgId },
    query: { account_id: query.accountId, before: query.before, limit: query.limit },
  });
}

/** The platform commission of one ticket at a price: the service charge that covers it. */
export function fetchServiceFeeQuote(
  orgId: string,
  query: { currency: string; priceMinor: number; seated: boolean },
): Promise<BillingServiceFeeQuote> {
  return call<BillingServiceFeeQuote>({
    route: API.billingServiceFeeQuote,
    params: { org_id: orgId },
    query: { currency: query.currency, price_minor: query.priceMinor, seated: query.seated ? "true" : "false" },
  });
}

/** Sets the payer details printed on the invoices. */
export function putBillingPayer(orgId: string, body: BillingPayerRequest): Promise<BillingPayerResponse> {
  return call<BillingPayerResponse>({ route: API.billingPayer, params: { org_id: orgId }, body });
}

// ── QL-12: tracked sales links and QR codes (spec 43) ─────────────────

export type SalesLinkStatusFilter = "active" | "archived" | "all";

export interface SalesLinkListQuery {
  status?: SalesLinkStatusFilter;
  eventId?: string;
  sessionId?: string;
  cursor?: string;
  limit?: number;
}

/** One page of the organization's links, newest first; `next_cursor` fetches the next page. */
export function listSalesLinks(orgId: string, q: SalesLinkListQuery = {}): Promise<SalesLinkList> {
  return call<SalesLinkList>({
    route: API.salesLinks,
    params: { org_id: orgId },
    query: { status: q.status, event_id: q.eventId, session_id: q.sessionId, cursor: q.cursor, limit: q.limit },
  });
}

export function fetchSalesLink(orgId: string, linkId: string): Promise<SalesLink> {
  return call<SalesLink>({ route: API.salesLink, params: { org_id: orgId, id: linkId } });
}

export function createSalesLink(orgId: string, body: SalesLinkCreateRequest): Promise<SalesLink> {
  return call<SalesLink>({ route: API.salesLinkCreate, params: { org_id: orgId }, body });
}

/**
 * Changes a link. The caller sends only the keys it means to change; a
 * `utm` key always carries the WHOLE set (the generated type has no
 * omitempty, so an undefined key must not be written as null).
 */
export function updateSalesLink(orgId: string, linkId: string, body: SalesLinkUpdateRequest): Promise<SalesLink> {
  const clean: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(body)) if (v !== undefined) clean[k] = v;
  return call<SalesLink>({ route: API.salesLinkUpdate, params: { org_id: orgId, id: linkId }, body: clean });
}

/** Scans per day and country of the range (UTC days) and the orders of the link. */
export function fetchSalesLinkStats(orgId: string, linkId: string, range: { from?: string; to?: string } = {}): Promise<SalesLinkStats> {
  return call<SalesLinkStats>({
    route: API.salesLinkStats,
    params: { org_id: orgId, id: linkId },
    query: { from: range.from, to: range.to },
  });
}

/** The QR image of the link's short address, as a file to show or to save. */
export function downloadSalesLinkQr(orgId: string, linkId: string, format: "png" | "svg", code: string, size = 1024): Promise<DownloadedFile> {
  return download({
    route: API.salesLinkQr,
    params: { org_id: orgId, id: linkId },
    query: { format, size: format === "png" ? size : undefined },
    accept: format === "png" ? "image/png" : "image/svg+xml",
    fallbackName: "qr-" + code + "." + format,
  });
}

// ─── S2b/S2c: the seat manager ────────────────────────────────────────────────

/** The hall of a session: geometry and the category of every plan category. */
export function fetchSessionSchema(sessionId: string): Promise<SchemaResponse> {
  return call<SchemaResponse>({ route: API.sessionSchema, params: { id: sessionId } });
}

/** The same hall for the organizer (`session.seats.manage`), also of an event that is not published yet. */
export function fetchSessionSchemaAdmin(sessionId: string): Promise<SchemaResponse> {
  return call<SchemaResponse>({ route: API.sessionSchemaAdmin, params: { id: sessionId } });
}

/** Every seat of the session with its category and status (the organizer's view). */
export function fetchSessionSeatsAdmin(sessionId: string): Promise<AdminSeatsResponse> {
  return call<AdminSeatsResponse>({ route: API.sessionSeatsAdmin, params: { id: sessionId } });
}

export function listSeatBatches(orgId: string, sessionId: string, limit = 20, offset = 0): Promise<SeatBatchList> {
  return call<SeatBatchList>({
    route: API.seatBatches,
    params: { org_id: orgId, session_id: sessionId },
    query: { limit, offset },
  });
}

/** ONE atomic batch: the whole tray, or nothing. */
export function createSeatBatch(orgId: string, sessionId: string, body: SeatBatchRequest): Promise<SeatBatch> {
  return call<SeatBatch>({ route: API.seatBatchCreate, params: { org_id: orgId, session_id: sessionId }, body });
}

/** Puts a batch back as a new batch. Seats that changed since are skipped, not forced. */
export function revertSeatBatch(orgId: string, sessionId: string, batchId: string): Promise<SeatBatch> {
  return call<SeatBatch>({
    route: API.seatBatchRevert,
    params: { org_id: orgId, session_id: sessionId, batch_id: batchId },
    body: {},
  });
}

export function listLayoutAlgorithms(orgId: string, sessionId: string): Promise<SeatLayoutAlgorithmsResponse> {
  return call<SeatLayoutAlgorithmsResponse>({
    route: API.seatLayoutAlgorithms,
    params: { org_id: orgId, session_id: sessionId },
  });
}

/** A proposal. Nothing is written. */
export function suggestSeatLayout(
  orgId: string,
  sessionId: string,
  body: SeatLayoutSuggestRequest,
): Promise<SeatLayoutSuggestResponse> {
  return call<SeatLayoutSuggestResponse>({
    route: API.seatLayoutSuggest,
    params: { org_id: orgId, session_id: sessionId },
    body,
  });
}

// ─── "Refer a friend" (spec 52) ──────────────────────────────────────────────

/** A programme (or the lack of one) with what the event's channels can do. */
export interface ReferralState {
  /** The live programme, else the latest ended one, else null (never started). */
  program: ReferralProgram | null;
  capability: ReferralCapability;
  warnings: ReferralWarning[];
}

/** What a programme belongs to: one event, or the organizer's shop (one per organization). */
export type ReferralTarget = { kind: "event"; eventId: string } | { kind: "shop" };

/** A stable id of the target for query keys. A shop never collides with an event id (a UUID). */
export function referralTargetKey(target: ReferralTarget): string {
  return target.kind === "event" ? target.eventId : "shop";
}

function referralParams(orgId: string, target: ReferralTarget): Record<string, string> {
  return target.kind === "event" ? { org_id: orgId, event_id: target.eventId } : { org_id: orgId };
}

/**
 * The programme of an event or of the shop. Without one the API answers a 404 `referral.program_not_found` whose
 * `error.details` carry the capability and the warnings, so the screen can explain before anything
 * is filled in whether the programme can start; that 404 is a normal answer here, not a failure.
 */
export async function fetchReferralProgram(orgId: string, target: ReferralTarget): Promise<ReferralState> {
  try {
    const r = await call<ReferralProgramResponse>({
      route: target.kind === "event" ? API.referralProgram : API.shopReferralProgram,
      params: referralParams(orgId, target),
    });
    return { program: r.program, capability: r.capability, warnings: r.warnings };
  } catch (error) {
    if (error instanceof ApiError && error.status === 404 && error.code === "referral.program_not_found") {
      const d = error.details;
      const capability = d?.capability as ReferralCapability | undefined;
      if (capability !== undefined) {
        const warnings = Array.isArray(d?.warnings) ? (d?.warnings as ReferralWarning[]) : [];
        return { program: null, capability, warnings };
      }
    }
    throw error;
  }
}

/** Starts or changes the programme. `accept_terms` only when this call starts one. */
export function saveReferralProgram(orgId: string, target: ReferralTarget, body: ReferralProgramRequest): Promise<ReferralProgramResponse> {
  return call<ReferralProgramResponse>({
    route: target.kind === "event" ? API.referralProgramPut : API.shopReferralProgramPut,
    params: referralParams(orgId, target),
    body,
  });
}

export type ReferralAction = "pause" | "resume" | "end";

/** Pause, resume or end the programme. An ended programme stays ended. */
export function referralAction(orgId: string, target: ReferralTarget, action: ReferralAction): Promise<ReferralProgramResponse> {
  const shop = target.kind === "shop";
  const route =
    action === "pause"
      ? shop ? API.shopReferralProgramPause : API.referralProgramPause
      : action === "resume"
        ? shop ? API.shopReferralProgramResume : API.referralProgramResume
        : shop ? API.shopReferralProgramEnd : API.referralProgramEnd;
  return call<ReferralProgramResponse>({ route, params: referralParams(orgId, target) });
}

export function fetchReferralSummary(orgId: string, target: ReferralTarget): Promise<ReferralSummary> {
  return call<ReferralSummary>({
    route: target.kind === "event" ? API.referralSummary : API.shopReferralSummary,
    params: referralParams(orgId, target),
  });
}

/** One page of rewards, newest first. `cursor` is the previous page's `next_cursor`. */
export function fetchReferralRewards(
  orgId: string,
  target: ReferralTarget,
  query: { state?: string; cursor?: string; limit?: number },
): Promise<ReferralRewardList> {
  return call<ReferralRewardList>({
    route: target.kind === "event" ? API.referralRewards : API.shopReferralRewards,
    params: referralParams(orgId, target),
    query: { state: query.state, cursor: query.cursor, limit: query.limit },
  });
}
