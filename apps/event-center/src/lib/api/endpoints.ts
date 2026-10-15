/**
 * One function per arena-api call of the event center. Every URL comes
 * from the API table (routes.ts), which the parity test checks against
 * openapi.yaml.
 */
import { call, download, type DownloadedFile } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type {
  ComplimentaryListResponse,
  EventListResponse,
  EventSummary,
  MeResponse,
  OrderDetail,
  OrderLetter,
  OrderListResponse,
  OrderResendTicketsResponse,
  OrderSummary,
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

/**
 * Cancels a `pending_payment` order and ends the hold on its seats (4-2).
 * The caller has already checked the typed word. `headers` carries the
 * step-up token (`withStepUp`). A paid order answers 409 `orders.invalid_transition`.
 */
export function cancelOrder(orgId: string, orderId: string, headers: Record<string, string>): Promise<OrderSummary> {
  return call<OrderSummary>({ route: API.cancelOrder, params: { org_id: orgId, id: orderId }, body: {}, headers });
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
export function revokeInvitation(issuanceId: string): Promise<void> {
  return call<void>({
    route: API.complimentaryRevoke,
    params: { id: issuanceId },
    body: { reason: "revoked from the web event center" },
  });
}
