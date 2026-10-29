/**
 * The staff invitations of the WHOLE organization (spec 46, INV-04b), whoever
 * issued them and for whichever date, newest first: the web twin of the bot's
 * list. Filters by event and date, tabs active / canceled / all, keyset paging
 * (`next_cursor`).
 *
 * A fully canceled invitation keeps the order status `paid` on the server (it
 * stays billed), so the state shown comes from the ticket counts, and while
 * tickets are still being minted the list asks again every two seconds.
 *
 * Cancel asks once, says where the seats go back to (the reserve or sale) and
 * offers "tell the guest", on by default. A person may cancel the whole
 * invitation or the ticket of one place.
 */
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { InvitationActions } from "@/components/staffInvitations/InvitationActions";
import { useEventSessions } from "@/components/staffInvitations/useEventSessions";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { cancelStaffInvitation, listOrgStaffInvitations } from "@/lib/api/endpoints";
import type { StaffInvitationOrgListItem, StaffInvitationPlace } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import type { Tone } from "@/lib/orders";
import {
  DEFAULT_FILTERS,
  ORG_PAGE_SIZE,
  ORG_STATES,
  allItems,
  filtersToQuery,
  isFromCsv,
  orgListKey,
  placeVip,
  venueOf,
  withEvent,
  type OrgFilters,
} from "@/lib/orgInvitations";
import {
  STAFF_INVITATIONS_KEY,
  activePlaces,
  canAct,
  canCancelPlace,
  guestOf,
  invitationState,
  issuerName,
  originCounts,
  pollDelay,
  type InvitationState,
} from "@/lib/staffInvitations";
import { useEventsList } from "@/screens/EventsScreen";

const TONE: Record<InvitationState, Tone> = { issuing: "wait", active: "ok", partial: "wait", cancelled: "neutral" };

function localZone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return undefined;
  }
}

interface Props {
  orgId: string;
  /** `complimentary.issue`: send the letter again. */
  canIssue: boolean;
  /** `complimentary.revoke`: cancel. */
  canRevoke: boolean;
  /** `event.read` and `session.read`: the event and date pickers of the filter. */
  canFilter: boolean;
}

/** The event and date pickers. Their own component, so a person without `event.read` never asks for the events. */
function OrgInvitationFilters({ orgId, filters, onChange }: { orgId: string; filters: OrgFilters; onChange: (f: OrgFilters) => void }) {
  const { t, locale } = useI18n();
  const events = useEventsList(orgId);
  const eventSessions = useEventSessions(orgId, filters.eventId);
  const eventOptions = [...(events.data?.events ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  return (
    <div className="org-inv-filters" data-testid="org-inv-filters">
      <label htmlFor="org-inv-event">{t("orgInv.filter.event")}</label>
      <select id="org-inv-event" value={filters.eventId} data-testid="org-inv-event" onChange={(e) => onChange(withEvent(filters, e.target.value))}>
        <option value="">{t("orgInv.filter.eventAll")}</option>
        {eventOptions.map((e) => (
          <option key={e.id} value={e.id}>
            {e.name}
          </option>
        ))}
      </select>
      {filters.eventId !== "" && (
        <>
          <label htmlFor="org-inv-session">{t("orgInv.filter.session")}</label>
          <select
            id="org-inv-session"
            value={filters.sessionId}
            data-testid="org-inv-session"
            onChange={(e) => onChange({ ...filters, sessionId: e.target.value })}
          >
            <option value="">{t("orgInv.filter.sessionAll")}</option>
            {eventSessions.sorted.map((s) => (
              <option key={s.id} value={s.id}>
                {formatDateTime(s.start_at, eventSessions.zoneOf(s.id), locale)}
              </option>
            ))}
          </select>
        </>
      )}
    </div>
  );
}

export function OrgInvitationList({ orgId, canIssue, canRevoke, canFilter }: Props) {
  const { t, locale } = useI18n();
  const qc = useQueryClient();
  const [filters, setFilters] = useState<OrgFilters>(DEFAULT_FILTERS);
  const [cancelling, setCancelling] = useState<{ item: StaffInvitationOrgListItem; place: StaffInvitationPlace | null } | null>(null);
  const [notify, setNotify] = useState(true);
  const [done, setDone] = useState<string | null>(null);

  const list = useInfiniteQuery({
    queryKey: orgListKey(orgId, filters),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      listOrgStaffInvitations(orgId, { ...filtersToQuery(filters), limit: ORG_PAGE_SIZE, cursor: pageParam }),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    // Tickets are minted a moment after the issue: read again until every invitation has them.
    refetchInterval: (q) => pollDelay(allItems(q.state.data?.pages)),
  });
  const items = allItems(list.data?.pages);

  const reload = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: [STAFF_INVITATIONS_KEY, orgId] }),
      // The seats went back to the reserve or on sale.
      qc.invalidateQueries({ queryKey: ["seats-admin"] }),
      qc.invalidateQueries({ queryKey: ["tiers", orgId] }),
      qc.invalidateQueries({ queryKey: ["sessionSummary", orgId] }),
    ]);

  const open = (item: StaffInvitationOrgListItem, place: StaffInvitationPlace | null = null) => {
    setDone(null);
    setNotify(true);
    setCancelling({ item, place });
  };

  const placeText = (p: StaffInvitationPlace): string =>
    p.kind === "seat" && p.row != null ? t("seatInv.seat", { sector: p.sector ?? "", row: p.row, seat: p.seat ?? "" }) : p.tier_name;

  const onConfirm = async () => {
    if (cancelling === null) return;
    const { item, place } = cancelling;
    try {
      // Always an explicit boolean: the API's default is "tell the guest", and the person may have switched it off.
      await cancelStaffInvitation(
        orgId,
        item.order_id,
        place !== null && place.ticket_id != null ? { ticket_ids: [place.ticket_id], notify_guest: notify } : { notify_guest: notify },
      );
    } catch (err) {
      await reload(); // the state may have changed meanwhile
      throw err; // the dialog says what happened and what to do
    }
    setDone(place !== null ? t("seatInv.cancel.placeDone", { place: placeText(place) }) : t("seatInv.cancel.done", { number: item.system_id }));
    setCancelling(null);
    await reload();
  };

  // Only the places whose ticket is still active go back: the cancelled ones already did.
  const origin =
    cancelling !== null ? originCounts(cancelling.place !== null ? [cancelling.place] : activePlaces(cancelling.item.places)) : { reserve: 0, sale: 0 };
  const explain =
    cancelling === null
      ? ""
      : [
          origin.reserve > 0 ? t("seatInv.cancel.reserve", { count: origin.reserve }) : "",
          origin.sale > 0 ? t("seatInv.cancel.sale", { count: origin.sale }) : "",
        ]
          .filter((x) => x !== "")
          .join(" ");

  return (
    <section aria-labelledby="org-inv-title" data-testid="org-inv-list">
      <h2 id="org-inv-title">{t("orgInv.list.title")}</h2>

      {canFilter && <OrgInvitationFilters orgId={orgId} filters={filters} onChange={setFilters} />}

      <div className="tabs" role="group" aria-label={t("orgInv.filter.state")}>
        {ORG_STATES.map((s) => (
          <button
            key={s}
            type="button"
            className={filters.state === s ? "tab active" : "tab"}
            aria-pressed={filters.state === s}
            data-testid={`org-inv-tab-${s}`}
            onClick={() => setFilters((f) => ({ ...f, state: s }))}
          >
            {t(`orgInv.tab.${s}` as MessageKey)}
          </button>
        ))}
      </div>

      {done !== null && (
        <p className="notice notice-ok" role="status" data-testid="org-inv-cancelled">
          {done}
        </p>
      )}
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && <Empty>{t("orgInv.list.empty")}</Empty>}
      {items.length > 0 && (
        <ul className="cards">
          {items.map((item) => {
            const state = invitationState(item);
            const guest = guestOf(item);
            const issuer = issuerName(item);
            const when = formatDateTime(item.created_at, localZone(), locale);
            const venue = venueOf(item);
            // A cancelled ticket is no longer part of the invitation. A fully cancelled one still lists its places, as history.
            const live = state === "cancelled" ? item.places : activePlaces(item.places);
            return (
              <li key={item.order_id} className="card" data-testid="org-inv-row" data-order={item.order_id} data-state={state} data-session={item.session.id}>
                <span className="card-title">
                  {guest === "" ? t("seatInv.list.noGuest") : guest}
                  <span className="muted"> · {t("seatInv.list.order", { number: item.system_id })}</span>
                  {isFromCsv(item) && (
                    <span data-testid="org-inv-csv">
                      {" "}
                      <Badge tone="neutral">{t("orgInv.row.csv")}</Badge>
                    </span>
                  )}
                </span>
                <span className="muted" data-testid="org-inv-session-line">
                  <Link
                    to="/o/$orgId/events/$eventId/sessions/$sessionId/invitations"
                    params={{ orgId, eventId: item.session.event_id, sessionId: item.session.id }}
                    title={t("orgInv.row.openDate")}
                    data-testid="org-inv-date-link"
                  >
                    {item.session.event_name}
                  </Link>
                  {" · "}
                  {formatDateTime(item.session.start_at, item.session.timezone, locale)}
                  {venue !== "" && ` · ${venue}`}
                </span>
                <ul className="inv-seats" data-testid="org-inv-places">
                  {live.map((p, i) => (
                    <li key={p.ticket_id ?? `${p.seat_key ?? p.tier_id}-${i}`} data-testid="org-inv-place" data-ticket-status={p.ticket_status ?? "pending"}>
                      <span className="muted">
                        {placeText(p)}
                        {p.origin === "unavailable" && ` · ${t("seatInv.picked.reserve")}`}
                      </span>
                      {placeVip(p) && (
                        <span data-testid="org-inv-vip">
                          {" "}
                          <Badge tone="wait">★ {t("vip.badge")}</Badge>
                        </span>
                      )}
                      {canRevoke && canCancelPlace(p, live.length) && (
                        <button
                          type="button"
                          className="btn"
                          aria-label={t("seatInv.cancel.place", { place: placeText(p) })}
                          onClick={() => open(item, p)}
                          data-testid="org-inv-cancel-place"
                        >
                          {t("seatInv.cancel")}
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
                {item.places.length > live.length && (
                  <span className="muted" data-testid="org-inv-canceled-places">
                    {t("seatInv.list.canceledPlaces", { count: item.places.length - live.length })}
                  </span>
                )}
                <span className="muted">
                  {issuer !== "" ? t("seatInv.list.issued", { issuer, when }) : t("seatInv.list.issuedApi", { when })}
                  {" · "}
                  {t("seatInv.list.delivery", {
                    mode: ["guest_email", "my_email", "pdf_only"].includes(item.delivery)
                      ? t(`seatInv.delivery.${item.delivery}` as MessageKey)
                      : item.delivery,
                  })}
                </span>
                <span className="card-meta" data-testid="org-inv-state">
                  <Badge tone={TONE[state]}>{t(`seatInv.state.${state}` as MessageKey)}</Badge>
                </span>
                {canAct(item) && (
                  <p className="inv-row-actions">
                    <InvitationActions orgId={orgId} orderId={item.order_id} systemId={item.system_id} canResend={canIssue && item.delivery !== "pdf_only"} />
                    {canRevoke && (
                      <button type="button" className="btn" onClick={() => open(item)} data-testid="org-inv-cancel">
                        {t("seatInv.cancel")}
                      </button>
                    )}
                  </p>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {list.hasNextPage && (
        <p>
          <button
            type="button"
            className="btn"
            disabled={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
            data-testid="org-inv-more"
          >
            {t("invitations.list.more")}
          </button>
        </p>
      )}
      {cancelling !== null && (
        <ConfirmPressDialog
          testId="inv-cancel-dialog"
          title={
            cancelling.place !== null
              ? t("seatInv.cancel.placeTitle", { place: placeText(cancelling.place) })
              : t("seatInv.cancel.title", { number: cancelling.item.system_id })
          }
          subject={guestOf(cancelling.item) || undefined}
          explain={explain}
          confirmLabel={t("seatInv.cancel.confirm")}
          onConfirm={onConfirm}
          onCancel={() => setCancelling(null)}
        >
          <p>
            <label className="inv-radio">
              <input type="checkbox" checked={notify} onChange={(e) => setNotify(e.target.checked)} data-testid="inv-cancel-notify" />
              <span>{t("seatInv.cancel.notify")}</span>
            </label>
          </p>
          <p className="muted">{t("seatInv.cancel.notifyHint")}</p>
        </ConfirmPressDialog>
      )}
    </section>
  );
}
