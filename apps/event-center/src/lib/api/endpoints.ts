/**
 * One function per arena-api call of the event center. Every URL comes
 * from the API table (routes.ts), which the parity test checks against
 * openapi.yaml.
 */
import { call, download, type DownloadedFile } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type {
  EventListResponse,
  EventSummary,
  MeResponse,
  OrderDetail,
  OrderListResponse,
  OrderResendTicketsResponse,
  OrgMeResponse,
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
