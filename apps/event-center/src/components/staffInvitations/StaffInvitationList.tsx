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
import { STAFF_INVITATIONS_KEY, canAct, guestOf, invitationState, issuerName, originCounts, pollDelay, type InvitationState } from "@/lib/staffInvitations";

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
  const [cancelling, setCancelling] = useState<StaffInvitationListItem | null>(null);
  const [notify, setNotify] = useState(true);
  const [done, setDone] = useState<number | null>(null);

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

  const open = (item: StaffInvitationListItem) => {
    setDone(null);
    setNotify(true);
    setCancelling(item);
  };

  const onConfirm = async () => {
    if (cancelling === null) return;
    try {
      await cancelStaffInvitation(orgId, cancelling.order_id, { notify_guest: notify });
    } catch (err) {
      await reload(); // the state may have changed meanwhile
      throw err; // the dialog says what happened and what to do
    }
    setDone(cancelling.system_id);
    setCancelling(null);
    await reload();
  };

  const placeText = (p: StaffInvitationPlace): string =>
    p.kind === "seat" && p.row != null
      ? t("seatInv.seat", { sector: p.sector ?? "", row: p.row, seat: p.seat ?? "" })
      : p.tier_name;

  const origin = cancelling !== null ? originCounts(cancelling.places) : { reserve: 0, sale: 0 };
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
          {t("seatInv.cancel.done", { number: done })}
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
            return (
              <li key={item.order_id} className="card" data-testid="inv-row" data-order={item.order_id} data-state={state}>
                <span className="card-title">
                  {guest === "" ? t("seatInv.list.noGuest") : guest}
                  <span className="muted"> · {t("seatInv.list.order", { number: item.system_id })}</span>
                </span>
                <span className="muted" data-testid="inv-row-places">
                  {item.places.map(placeText).join(" / ")}
                  {item.places.some((p) => p.origin === "unavailable") && ` · ${t("seatInv.picked.reserve")}`}
                </span>
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
          title={t("seatInv.cancel.title", { number: cancelling.system_id })}
          subject={guestOf(cancelling) || undefined}
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
