/**
 * 4-4: the issued invitations, newest first, each with the state of its
 * letter (`letters[]` of GET .../complimentary, read through the ticket's
 * delivery job because an invitation has no order) and the annul button.
 *
 * Annulling asks for the bot's word (ANNUL / АННУЛИРОВАТЬ / ANULAR) and says
 * Arena does not tell the guest. Only a valid invitation offers it, and only
 * to a person who may issue (the revoke route reuses `complimentary.issue`).
 * A ticket already scanned at the door cannot be annulled: its card says
 * "used" and offers nothing.
 */
import { useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { listInvitations, annulInvitation, type InvitationState } from "@/lib/api/endpoints";
import type { ComplimentaryListItem } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  canRevoke,
  isIssuanceState,
  issuanceTone,
  letterNeedsAttention,
  letterSummary,
  letterTone,
} from "@/lib/invitations";

export const INVITATIONS_PAGE_SIZE = 20;

type Filter = "all" | InvitationState;
const FILTERS: readonly Filter[] = ["all", "valid", "used", "revoked"];

function InvitationCard({
  item,
  canIssue,
  onAnnul,
}: {
  item: ComplimentaryListItem;
  canIssue: boolean;
  onAnnul: (item: ComplimentaryListItem) => void;
}) {
  const { t, locale } = useI18n();
  const ticket = item.tickets[0];
  const guest = ticket?.holder_name ?? ticket?.holder_email ?? t("invitations.guest.none");
  const letter = letterSummary(item.letters);
  const stateText = isIssuanceState(item.state) ? t(`invitations.state.${item.state}` as MessageKey) : item.state;
  // An annulled invitation's letter was a real letter: keep showing how it went.
  return (
    <li className="card" data-testid="invitation-row" data-state={item.state} data-letter={letter}>
      <span className="card-title">
        {guest}
        {ticket?.holder_name != null && ticket.holder_email != null && (
          <span className="muted"> · {ticket.holder_email}</span>
        )}
      </span>
      <span className="muted">
        {item.event_name} · {formatDateTime(item.session_start_at, item.venue_timezone, locale)}
        {item.tier_name != null && ` · ${item.tier_name}`}
        {ticket !== undefined && ` · ${t("invitations.ticket", { number: ticket.system_ticket_id })}`}
      </span>
      <span className="card-meta">
        <span data-testid="invitation-state">
          <Badge tone={issuanceTone(item.state)}>{stateText}</Badge>
        </span>
        <span data-testid="invitation-letter">
          <Badge tone={letterTone(letter)}>{t(`invitations.letter.${letter}` as MessageKey)}</Badge>
        </span>
      </span>
      {letterNeedsAttention(letter) && item.state !== "revoked" && (
        <p className="notice notice-bad" role="status" data-testid="invitation-attention">
          {t("invitations.letter.attention")}
        </p>
      )}
      {canRevoke(item.state, canIssue) && (
        <p>
          <button type="button" className="btn" onClick={() => onAnnul(item)} data-testid="invitation-annul">
            {t("invitations.annul")}
          </button>
        </p>
      )}
    </li>
  );
}

export function InvitationList({ orgId, canIssue }: { orgId: string; canIssue: boolean }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState<Filter>("all");
  const [annulling, setAnnulling] = useState<ComplimentaryListItem | null>(null);
  const [annulled, setAnnulled] = useState(false);

  const list = useInfiniteQuery({
    queryKey: ["invitations", orgId, filter],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      listInvitations(orgId, {
        state: filter === "all" ? undefined : filter,
        limit: INVITATIONS_PAGE_SIZE,
        offset: pageParam,
      }),
    getNextPageParam: (last, all) =>
      last.has_more ? all.reduce((n, p) => n + p.issuances.length, 0) : undefined,
  });
  const items = list.data?.pages.flatMap((p) => p.issuances) ?? [];

  const reload = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["invitations", orgId] }),
      // The place went back on sale.
      queryClient.invalidateQueries({ queryKey: ["tiers", orgId] }),
      queryClient.invalidateQueries({ queryKey: ["sessionSummary", orgId] }),
    ]);

  const onConfirm = async () => {
    if (annulling === null) return;
    try {
      await annulInvitation(annulling.id);
    } catch (err) {
      await reload(); // the state may have changed meanwhile
      throw err; // the dialog says what happened and what to do
    }
    setAnnulling(null);
    setAnnulled(true);
    await reload();
  };

  const ticket = annulling?.tickets[0];
  return (
    <section aria-labelledby="invitations-list-title" data-testid="invitation-list">
      <h2 id="invitations-list-title">{t("invitations.list.title")}</h2>
      <div className="tabs" role="group" aria-label={t("invitations.list.filter")}>
        {FILTERS.map((f) => (
          <button
            key={f}
            type="button"
            className={filter === f ? "tab active" : "tab"}
            aria-pressed={filter === f}
            data-testid={`invitation-filter-${f}`}
            onClick={() => setFilter(f)}
          >
            {t(`invitations.filter.${f}` as MessageKey)}
          </button>
        ))}
      </div>
      {annulled && (
        <p className="notice notice-ok" role="status" data-testid="invitation-annulled">
          {t("invitations.annul.done")}
        </p>
      )}
      {list.isLoading && <Loading />}
      {list.error !== null && <ErrorBox error={list.error} onRetry={() => void list.refetch()} />}
      {list.data !== undefined && items.length === 0 && <Empty>{t("invitations.list.empty")}</Empty>}
      {items.length > 0 && (
        <ul className="cards">
          {items.map((item) => (
            <InvitationCard
              key={item.id}
              item={item}
              canIssue={canIssue}
              onAnnul={(it) => {
                setAnnulled(false);
                setAnnulling(it);
              }}
            />
          ))}
        </ul>
      )}
      {list.hasNextPage && (
        <p>
          <button
            type="button"
            className="btn"
            disabled={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
            data-testid="invitation-more"
          >
            {t("invitations.list.more")}
          </button>
        </p>
      )}
      {annulling !== null && (
        <ConfirmWordDialog
          kind="annul_invitation"
          subject={t("invitations.annul.subject", {
            guest: ticket?.holder_name ?? ticket?.holder_email ?? t("invitations.guest.none"),
            number: ticket?.system_ticket_id ?? "",
          })}
          onConfirm={onConfirm}
          onCancel={() => setAnnulling(null)}
        />
      )}
    </section>
  );
}
