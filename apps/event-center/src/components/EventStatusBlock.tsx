/**
 * 5-1: the "Status" block of the event card. The web twin of the bot's status
 * buttons (eventbot/evstatus.go, EC-10):
 *
 * - Publish acts at once; the API's publish gate (a date, a priced category)
 *   answers in words.
 * - Take off sale and Archive are ONE confirmation press, and the text says
 *   plainly that tickets already sold stay valid (archive also says it is final).
 * - Delete asks the API what it would touch (delete-impact). An event that sold
 *   anything is refused with the numbers and the archive is offered instead.
 *   Otherwise the person has to TYPE the delete word; a button never deletes.
 *
 * Which buttons show comes from the lifecycle table and the person's rights
 * (lib/eventStatus.ts). The API still decides every press.
 */
import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { DryRunConfirm } from "@/components/DryRunConfirm";
import { ErrorNotice } from "@/components/ErrorNotice";
import { deleteEvent, fetchEventDeleteImpact, updateEventStatus } from "@/lib/api/endpoints";
import type { EventDeleteImpact } from "@/lib/api/types";
import type { DryRunResult } from "@/lib/dryRun";
import {
  ACTION_TARGET,
  deleteVerdict,
  isEventStatus,
  offerArchiveInstead,
  statusActions,
  type StatusAction,
} from "@/lib/eventStatus";
import { useI18n } from "@/lib/i18n";

export interface EventStatusBlockProps {
  orgId: string;
  eventId: string;
  eventName: string;
  status: string;
  /** `event.publish` in the organization. */
  canPublish: boolean;
  /** `event.delete` in the organization. */
  canDelete: boolean;
}

type MoveAction = Exclude<StatusAction, "delete">;
type Step = "none" | "off" | "archive" | "impact" | "word";

export function EventStatusBlock({ orgId, eventId, eventName, status, canPublish, canDelete }: EventStatusBlockProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [step, setStep] = useState<Step>("none");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<MoveAction | null>(null);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [impact, setImpact] = useState<EventDeleteImpact | null>(null);

  const actions = statusActions(status, { canPublish, canDelete });
  // The agent has no buttons and sees no block. A finished move keeps it until reload.
  if (actions.length === 0 && done === null) return null;

  const reload = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["events", orgId] }),
      queryClient.invalidateQueries({ queryKey: ["eventSummary", orgId, eventId] }),
      queryClient.invalidateQueries({ queryKey: ["sessions", orgId, eventId] }),
    ]);

  /** One move along the lifecycle. A failure reloads the card (the status may have changed) and is thrown on. */
  const move = async (action: MoveAction) => {
    try {
      await updateEventStatus(orgId, eventId, ACTION_TARGET[action]);
    } catch (error) {
      await reload();
      throw error;
    }
    setDone(action);
    await reload();
  };

  const press = async (action: MoveAction) => {
    if (busy) return;
    setBusy(true);
    setFailure(null);
    setDone(null);
    try {
      await move(action);
    } catch (error) {
      setFailure({ error });
    } finally {
      setBusy(false);
    }
  };

  const open = (action: StatusAction) => {
    setFailure(null);
    setDone(null);
    if (action === "publish") void press("publish");
    else if (action === "off") setStep("off");
    else if (action === "archive") setStep("archive");
    else setStep("impact");
  };

  /** The delete dry run, in the shape DryRunConfirm shows. */
  const dryRun = async (): Promise<DryRunResult> => {
    const found = await fetchEventDeleteImpact(orgId, eventId);
    setImpact(found);
    const blocked = deleteVerdict(found) === "blocked";
    return {
      summary: blocked ? undefined : t("eventStatus.delete.summaryFree", { sessions: found.sessions }),
      figures: [
        { label: t("eventStatus.delete.sessions"), value: found.sessions },
        { label: t("eventStatus.delete.paidOrders"), value: found.paid_orders },
        { label: t("eventStatus.delete.tickets"), value: found.tickets },
      ],
      blocked: blocked
        ? found.blocked === "has_paid_orders"
          ? t("eventStatus.delete.blocked", { orders: found.paid_orders, tickets: found.tickets })
          : t("eventStatus.delete.blockedOther")
        : null,
    };
  };

  const closeDelete = () => {
    setStep("none");
    setImpact(null);
  };

  const label = isEventStatus(status) ? t(`eventStatus.s.${status}`) : status;

  return (
    <section aria-labelledby="event-status-heading" data-testid="event-status" data-status={status}>
      <h2 id="event-status-heading">{t("eventStatus.heading")}</h2>
      <p data-testid="event-status-current">{t("eventStatus.current", { status: label })}</p>
      {isEventStatus(status) && <p className="muted">{t(`eventStatus.hint.${status}`)}</p>}

      {done !== null && (
        <p className="notice notice-ok" role="status" data-testid="event-status-done">
          {t(`eventStatus.done.${done}`)}
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure.error} testId="event-status-error" />}

      <div className="actions">
        {actions.map((action) => (
          <button
            key={action}
            type="button"
            className="btn"
            disabled={busy}
            onClick={() => open(action)}
            data-testid={`event-status-${action}`}
          >
            {t(`eventStatus.button.${action}`)}
          </button>
        ))}
      </div>

      {step === "impact" && (
        <div className="card" data-testid="event-delete-panel">
          <DryRunConfirm
            title={t("eventStatus.delete.title")}
            dryRun={dryRun}
            confirmLabel={t("eventStatus.delete.continue")}
            onConfirm={() => setStep("word")}
          />
          <p className="actions">
            {impact !== null && canPublish && offerArchiveInstead(impact) && (
              <button type="button" className="btn btn-primary" onClick={() => setStep("archive")} data-testid="event-delete-archive-instead">
                {t("eventStatus.delete.archiveInstead")}
              </button>
            )}
            <button type="button" className="btn" onClick={closeDelete} data-testid="event-delete-close">
              {t("eventStatus.delete.close")}
            </button>
          </p>
        </div>
      )}

      {step === "off" && (
        <ConfirmPressDialog
          testId="event-off-dialog"
          title={t("eventStatus.off.title")}
          subject={eventName}
          explain={t("eventStatus.off.explain")}
          confirmLabel={t("eventStatus.off.confirm")}
          onConfirm={async () => {
            await move("off");
            setStep("none");
          }}
          onCancel={() => setStep("none")}
        />
      )}
      {step === "archive" && (
        <ConfirmPressDialog
          testId="event-archive-dialog"
          title={t("eventStatus.archive.title")}
          subject={eventName}
          explain={t("eventStatus.archive.explain")}
          confirmLabel={t("eventStatus.archive.confirm")}
          onConfirm={async () => {
            await move("archive");
            setStep("none");
            setImpact(null);
          }}
          onCancel={() => setStep("none")}
        />
      )}
      {step === "word" && (
        <ConfirmWordDialog
          kind="delete_event"
          subject={eventName}
          onConfirm={async () => {
            await deleteEvent(orgId, eventId);
            await navigate({ to: "/o/$orgId/events", params: { orgId } });
            void queryClient.invalidateQueries({ queryKey: ["events", orgId] });
            queryClient.removeQueries({ queryKey: ["eventSummary", orgId, eventId] });
          }}
          onCancel={closeDelete}
        />
      )}
    </section>
  );
}
