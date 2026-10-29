/**
 * The invitations of one session, whoever issued them (spec 46, R-5: a manager
 * sees the owner's and the other way round), newest first.
 *
 * A fully canceled invitation keeps the order status `paid` on the server (it
 * stays billed), so the state shown here comes from the ticket counts. While
 * tickets are still being minted the list asks again every two seconds.
 *
 * Cancel asks once, says where the seats go back to (the reserve or sale) and
 * offers "tell the guest", on by default.
 */
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { InvitationActions } from "@/components/staffInvitations/InvitationActions";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { cancelStaffInvitation, listStaffInvitations } from "@/lib/api/endpoints";
import type { StaffInvitationListItem, StaffInvitationPlace } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import type { Tone } from "@/lib/orders";
import { STAFF_INVITATIONS_KEY, activePlaces, canAct, canCancelPlace, guestOf, invitationState, issuerName, originCounts, pollDelay, type InvitationState } from "@/lib/staffInvitations";

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
  sessionId: string;
  /** `complimentary.issue`: resend. */
  canIssue: boolean;
  /** `complimentary.revoke`: cancel. */
  canRevoke: boolean;
}

export function StaffInvitationList({ orgId, sessionId, canIssue, canRevoke }: Props) {
  const { t, locale } = useI18n();
  const qc = useQueryClient();
  // What the open question is about: the whole invitation, or the ticket of one place.
  const [cancelling, setCancelling] = useState<{ item: StaffInvitationListItem; place: StaffInvitationPlace | null } | null>(null);
  const [notify, setNotify] = useState(true);
  const [done, setDone] = useState<{ text: string } | null>(null);

  const list = useQuery({
    queryKey: [STAFF_INVITATIONS_KEY, orgId, sessionId],
    queryFn: () => listStaffInvitations(orgId, sessionId),
    // Tickets are minted a moment after the issue: read again until every invitation has them.
    refetchInterval: (q) => pollDelay(q.state.data?.items ?? []),
  });
  const items = list.data?.items ?? [];

  const reload = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: [STAFF_INVITATIONS_KEY, orgId, sessionId] }),
      // The seats went back to the reserve or on sale.
      qc.invalidateQueries({ queryKey: ["seats-admin", sessionId] }),
      qc.invalidateQueries({ queryKey: ["tiers", orgId] }),
      qc.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] }),
    ]);

  const open = (item: StaffInvitationListItem, place: StaffInvitationPlace | null = null) => {
    setDone(null);
    setNotify(true);
    setCancelling({ item, place });
  };

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
    setDone({ text: place !== null ? t("seatInv.cancel.placeDone", { place: placeText(place) }) : t("seatInv.cancel.done", { number: item.system_id }) });
    setCancelling(null);
    await reload();
  };

  const placeText = (p: StaffInvitationPlace): string =>
    p.kind === "seat" && p.row != null
      ? t("seatInv.seat", { sector: p.sector ?? "", row: p.row, seat: p.seat ?? "" })
      : p.tier_name;

  // Only the places whose ticket is still active go back: the cancelled ones already did.
  const origin = cancelling !== null ? originCounts(cancelling.place !== null ? [cancelling.place] : activePlaces(cancelling.item.places)) : { reserve: 0, sale: 0 };
  const explain = cancelling === null
    ? ""
    : [
        origin.reserve > 0 ? t("seatInv.cancel.reserve", { count: origin.reserve }) : "",
        origin.sale > 0 ? t("seatInv.cancel.sale", { count: origin.sale }) : "",
      ]
        .filter((x) => x !== "")
        .join(" ");

  return (
    <section aria-labelledby="inv-list-title" data-testid="inv-list">
      <h2 id="inv-list-title">{t("seatInv.list.title")}</h2>
      {done !== null && (
        <p className="notice notice-ok" role="status" data-testid="inv-cancelled">
          {done.text}
        </p>
      )}
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && <Empty>{t("seatInv.list.empty")}</Empty>}
      {items.length > 0 && (
        <ul className="cards">
          {items.map((item) => {
            const state = invitationState(item);
            const guest = guestOf(item);
            const issuer = issuerName(item);
            const when = formatDateTime(item.created_at, localZone(), locale);
            // A cancelled ticket is no longer part of the invitation. A fully cancelled one still lists its places, as history.
            const live = state === "cancelled" ? item.places : activePlaces(item.places);
            return (
              <li key={item.order_id} className="card" data-testid="inv-row" data-order={item.order_id} data-state={state}>
                <span className="card-title">
                  {guest === "" ? t("seatInv.list.noGuest") : guest}
                  <span className="muted"> · {t("seatInv.list.order", { number: item.system_id })}</span>
                </span>
                <ul className="inv-seats" data-testid="inv-row-places">
                  {live.map((p, i) => (
                    <li key={p.ticket_id ?? `${p.seat_key ?? p.tier_id}-${i}`} data-testid="inv-row-place" data-ticket-status={p.ticket_status ?? "pending"}>
                      <span className="muted">
                        {placeText(p)}
                        {p.origin === "unavailable" && ` · ${t("seatInv.picked.reserve")}`}
                      </span>
                      {canRevoke && canCancelPlace(p, live.length) && (
                        <button
                          type="button"
                          className="btn"
                          aria-label={t("seatInv.cancel.place", { place: placeText(p) })}
                          onClick={() => open(item, p)}
                          data-testid="inv-cancel-place"
                        >
                          {t("seatInv.cancel")}
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
                {item.places.length > live.length && (
                  <span className="muted" data-testid="inv-row-canceled">
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
                <span className="card-meta" data-testid="inv-row-state">
                  <Badge tone={TONE[state]}>{t(`seatInv.state.${state}` as MessageKey)}</Badge>
                </span>
                {canAct(item) && (
                  <p className="inv-row-actions">
                    <InvitationActions orgId={orgId} orderId={item.order_id} systemId={item.system_id} canResend={canIssue && item.delivery !== "pdf_only"} />
                    {canRevoke && (
                      <button type="button" className="btn" onClick={() => open(item)} data-testid="inv-cancel">
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
