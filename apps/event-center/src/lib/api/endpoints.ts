/**
 * One function per arena-api call of the event center. Every URL comes
 * from the API table (routes.ts), which the parity test checks against
 * openapi.yaml.
 */
import { call, download, type DownloadedFile } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type {
  CreatePromoCodeRequest,
  EventListResponse,
  EventSummary,
  MeResponse,
  OrderDetail,
  OrderListResponse,
  OrderSummary,
  OrgMeResponse,
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
 * Cancels a `pending_payment` order and ends the hold on its seats (4-2).
 * The caller has already checked the typed word. `headers` carries the
 * step-up token (`withStepUp`). A paid order answers 409 `orders.invalid_transition`.
 */
export function cancelOrder(orgId: string, orderId: string, headers: Record<string, string>): Promise<OrderSummary> {
  return call<OrderSummary>({ route: API.cancelOrder, params: { org_id: orgId, id: orderId }, body: {}, headers });
}
