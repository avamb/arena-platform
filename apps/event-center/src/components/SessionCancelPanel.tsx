/**
 * 5-2: "Cancel the session". The web twin of the bot's `ses:cx` ... `ses:go`
 * and its typed word (eventbot/sessions_dialog.go, owner decision 2026-10-07).
 *
 * A cancellation is never one press. The dry run (GET .../change-impact with
 * status=cancelled) says how many buyers get a letter and whether the change
 * is blocked; the button then only opens the typed-word dialog with the
 * SESSION's word (CANCEL SESSION / ОТМЕНИТЬ СЕАНС / CANCELAR SESIÓN, not the
 * order's), and only the right word sends the PATCH. The screen says plainly,
 * before and in the dialog, that the letters go out but the orders stay paid:
 * the platform does not refund anything on a cancellation.
 *
 * This panel uses the dry-run lines (DryRunBody) and not DryRunConfirm: that
 * component ends in a "Done." line after its press, and here the press only
 * opens the word dialog.
 */
import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { DryRunBody } from "@/components/DryRunConfirm";
import { ErrorNotice } from "@/components/ErrorNotice";
import { SessionChangeMessage } from "@/components/SessionChangeMessage";
import { SessionContactForm } from "@/components/SessionContactForm";
import { Loading } from "@/components/ui";
import { fetchChangeImpact, patchSession } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import { isBlocked } from "@/lib/dryRun";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import {
  EVENT_UPDATE_PERMISSION,
  cancelBody,
  emptyMessage,
  impactQuery,
  impactToDryRun,
  isContactBlocked,
  noticeToSend,
  noticeTooLong,
  resetToSuggested,
  typeMessage,
  withSuggested,
  type MessageState,
} from "@/lib/sessionChange";
import { useOrg } from "@/screens/OrgLayout";

export interface SessionCancelDone {
  queued: number;
}

export interface SessionCancelPanelProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  startAt: string;
  timezone: string | null;
  onDone: (done: SessionCancelDone) => void;
  onClose: () => void;
}

export function SessionCancelPanel({ orgId, eventId, sessionId, startAt, timezone, onDone, onClose }: SessionCancelPanelProps) {
  const { t, locale } = useI18n();
  const { access } = useOrg();
  const queryClient = useQueryClient();
  const [wordOpen, setWordOpen] = useState(false);
  const [message, setMessage] = useState<MessageState>(emptyMessage);

  const impactQ = useQuery({
    queryKey: ["sessionImpact", orgId, sessionId, "cancel", locale],
    queryFn: () => fetchChangeImpact(orgId, sessionId, impactQuery({ cancel: true, locale })),
    // A dry run is a fresh question every time the panel opens.
    gcTime: 0,
    staleTime: 0,
  });
  const impact = impactQ.data ?? null;
  const suggested = impact?.default_message;
  useEffect(() => {
    if (suggested !== undefined) setMessage((prev) => withSuggested(prev, suggested));
  }, [suggested]);
  const result = impact !== null ? impactToDryRun(impact, "cancel", t) : null;
  const blocked = result !== null && isBlocked(result);

  const onConfirm = async () => {
    if (noticeTooLong(message.text)) {
      throw new ApiError(422, "session.change_message_too_long", "the message to buyers is too long");
    }
    try {
      const answer = await patchSession(orgId, eventId, sessionId, cancelBody(noticeToSend(impact, message.text)));
      await queryClient.invalidateQueries();
      setWordOpen(false);
      onDone({ queued: answer.change?.queued ?? 0 });
    } catch (error) {
      // What the dry run said is no longer true: show the new answer.
      if (error instanceof ApiError && (error.code === "organization.contact_missing" || error.code === "session.change_site_unsupported")) {
        void impactQ.refetch();
      }
      throw error; // the dialog shows what went wrong and what to do
    }
  };

  return (
    <section className="panel session-change-panel" aria-labelledby="session-cancel-title" data-testid="session-cancel">
      <h2 id="session-cancel-title">{t("sessionChange.cancel.title")}</h2>
      <p className="muted">{t("sessionChange.cancel.now", { time: formatDateTime(startAt, timezone, locale) })}</p>
      <p className="notice" data-testid="session-cancel-refund-note">
        {t("sessionChange.cancel.refundNote")}
      </p>

      {impactQ.isFetching && impact === null && <Loading label={t("dryRun.loading")} />}
      {impactQ.error !== null && impact === null && <ErrorNotice error={impactQ.error} testId="session-cancel-load-error" />}

      {result !== null && impact !== null && (
        <>
          <DryRunBody result={result} />
          {!blocked && impact.orders > 0 && (
            <SessionChangeMessage
              state={message}
              suggested={impact.default_message}
              onType={(text) => setMessage(typeMessage(text))}
              onReset={() => setMessage(resetToSuggested(impact.default_message))}
            />
          )}
          {isContactBlocked(impact) && (
            <SessionContactForm
              orgId={orgId}
              eventId={eventId}
              contact={impact.contact}
              canEdit={access.can(EVENT_UPDATE_PERMISSION)}
              onSaved={() => void impactQ.refetch()}
            />
          )}
          {!blocked && (
            <p>
              <button type="button" className="btn btn-primary" onClick={() => setWordOpen(true)} data-testid="session-cancel-continue">
                {t("sessionChange.cancel.continue")}
              </button>
            </p>
          )}
        </>
      )}

      <p>
        <button type="button" className="btn" onClick={onClose} data-testid="session-change-close">
          {t("sessionChange.close")}
        </button>
      </p>

      {wordOpen && (
        <ConfirmWordDialog
          kind="cancel_session"
          subject={formatDateTime(startAt, timezone, locale)}
          onConfirm={onConfirm}
          onCancel={() => setWordOpen(false)}
        />
      )}
    </section>
  );
}
