/**
 * The two kinds of row of the refund queue (EC-V5): a refund that needs a
 * person, and an order whose buyer has no letter. Each row owns the state of
 * its own button, so a retry or a resend never blurs another row.
 */
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useStepUp } from "@/components/StepUpDialog";
import { Badge } from "@/components/ui";
import { resendOrderTickets, retryRefund } from "@/lib/api/endpoints";
import type { RefundQueueItem, UndeliveredLetter } from "@/lib/api/types";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { orderNumber } from "@/lib/orders";
import {
  REFUND_PROBLEM_KEYS,
  canResend,
  canRetry,
  letterKindKey,
  letterProblemKey,
  refundProblem,
  retryLineKey,
  retryOutcome,
} from "@/lib/refundQueue";
import { StepUpCancelled, withStepUp } from "@/lib/stepUp";

type Done = { kind: "done"; text: string } | { kind: "error"; error: unknown } | null;

export function RefundRow({ orgId, item, canApprove }: { orgId: string; item: RefundQueueItem; canApprove: boolean }) {
  const { t, locale } = useI18n();
  const stepUp = useStepUp();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Done>(null);
  const problem = refundProblem(item);
  const why = item.failure_reason ?? item.failure_code ?? null;

  const retry = async () => {
    if (busy) return; // one press at a time
    setBusy(true);
    setResult(null);
    try {
      const res = await withStepUp(orgId, (headers) => retryRefund(orgId, item.id, headers), stepUp.ask);
      setResult({ kind: "done", text: t(retryLineKey(retryOutcome(res.refunds)), { provider: "", why: "", state: "" }) });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["refund-queue", orgId] }),
        queryClient.invalidateQueries({ queryKey: ["orders", orgId] }),
        queryClient.invalidateQueries({ queryKey: ["order", orgId] }),
      ]);
    } catch (err) {
      if (err instanceof StepUpCancelled) return; // the person closed the password question: nothing was sent
      setResult({ kind: "error", error: err });
    } finally {
      setBusy(false);
    }
  };

  return (
    <li className="queue-item" data-testid="queue-refund" data-state={item.state}>
      <div className="queue-item-head">
        {item.order_id !== null && item.order_id !== undefined ? (
          <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: item.order_id }} className="oc-num">
            {item.order_number !== null && item.order_number !== undefined ? orderNumber(item.order_number) : t("queue.order")}
          </Link>
        ) : (
          <span className="oc-num">{t("queue.order")}</span>
        )}
        <Badge tone={problem === "failed" ? "bad" : "wait"}>{t(REFUND_PROBLEM_KEYS[problem])}</Badge>
        <strong className="oc-total">{formatMoney(item.amount, item.currency, locale)}</strong>
      </div>
      <p className="muted small">
        {item.event_name ?? ""}
        {item.ticket_number !== null && item.ticket_number !== undefined ? " · " + t("ticket.number", { number: item.ticket_number }) : ""}
        {" · "}
        {formatDateTime(item.created_at, null, locale)}
      </p>
      {why !== null && <p className="small">{why.slice(0, 200)}</p>}
      {canRetry(item, canApprove) && (
        <p>
          <button type="button" className="btn btn-primary" onClick={() => void retry()} disabled={busy} data-testid="queue-retry">
            {busy ? t("queue.retry.busy") : t("queue.retry")}
          </button>
        </p>
      )}
      {result?.kind === "done" && (
        <p className="notice" role="status" data-testid="queue-retry-done">
          {result.text}
        </p>
      )}
      {result?.kind === "error" && <ErrorNotice error={result.error} testId="queue-retry-failure" />}
    </li>
  );
}

export function LetterRow({ orgId, letter, canUpdate }: { orgId: string; letter: UndeliveredLetter; canUpdate: boolean }) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<Done>(null);

  const resend = async () => {
    if (busy) return;
    setBusy(true);
    setResult(null);
    try {
      await resendOrderTickets(orgId, letter.order_id);
      setResult({ kind: "done", text: t("queue.resend.done") });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["refund-queue", orgId] }),
        queryClient.invalidateQueries({ queryKey: ["orders", orgId] }),
        queryClient.invalidateQueries({ queryKey: ["order", orgId] }),
      ]);
    } catch (err) {
      setResult({ kind: "error", error: err });
    } finally {
      setBusy(false);
    }
  };

  return (
    <li className="queue-item" data-testid="queue-letter" data-kind={letter.kind}>
      <div className="queue-item-head">
        <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: letter.order_id }} className="oc-num">
          {orderNumber(letter.order_number)}
        </Link>
        <Badge tone={letter.problem === "failed" ? "bad" : "wait"}>{t(letterProblemKey(letter.problem))}</Badge>
        <span className="muted">{t(letterKindKey(letter.kind))}</span>
      </div>
      <p className="muted small">
        {letter.event_name} · {formatDateTime(letter.since, null, locale)}
      </p>
      <p className="small">{letter.recipient}</p>
      {letter.reason !== "" && <p className="small">{letter.reason}</p>}
      <p>
        {canResend(letter, canUpdate) ? (
          <button type="button" className="btn btn-primary" onClick={() => void resend()} disabled={busy} data-testid="queue-resend">
            {busy ? t("queue.resend.busy") : t("queue.resend")}
          </button>
        ) : (
          <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: letter.order_id }} className="btn btn-outline">
            {t("queue.openOrder")}
          </Link>
        )}
      </p>
      {result?.kind === "done" && (
        <p className="notice" role="status" data-testid="queue-resend-done">
          {result.text}
        </p>
      )}
      {result?.kind === "error" && <ErrorNotice error={result.error} testId="queue-resend-failure" />}
    </li>
  );
}
