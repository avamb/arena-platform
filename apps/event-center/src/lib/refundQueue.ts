/**
 * Pure helpers of the refund queue screen (EC-V5, spec 62 section 12.2): what
 * a queue row is called, which button it offers and what the nav badge says.
 */
import type { RefundProblemQueue, RefundQueueItem, UndeliveredLetter } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";
import { refundLineState, type RefundLineState } from "@/lib/refunds";

/** The number on the nav item: nothing at 0, "99+" past 99. */
export function badgeText(count: number): string | null {
  if (!Number.isFinite(count) || count <= 0) return null;
  return count > 99 ? "99+" : String(Math.floor(count));
}

/** The count the badge shows for a queue answer (0 while it is not there). */
export function queueCount(q: RefundProblemQueue | undefined): number {
  return q?.count ?? 0;
}

export type RefundProblem = "failed" | "review" | "stuck";

/** Why a refund is in the queue. The server lists failed, manual_review and long provider_pending. */
export function refundProblem(item: Pick<RefundQueueItem, "state">): RefundProblem {
  if (item.state === "failed") return "failed";
  if (item.state === "manual_review") return "review";
  return "stuck";
}

export const REFUND_PROBLEM_KEYS: Record<RefundProblem, MessageKey> = {
  failed: "queue.refund.failed",
  review: "queue.refund.review",
  stuck: "queue.refund.stuck",
};

/** What the retry button may do: the server says the refund is retryable, the person may approve refunds. */
export function canRetry(item: Pick<RefundQueueItem, "retryable">, canApprove: boolean): boolean {
  return canApprove && item.retryable === true;
}

/** The words for the state of the refund a retry started. */
export function retryLineKey(state: RefundLineState): MessageKey {
  switch (state) {
    case "succeeded":
      return "refund.st.succeeded";
    case "pending":
      return "refund.st.pending";
    case "queued":
      return "refund.st.queued";
    case "awaiting_approval":
      return "refund.st.awaitingApproval";
    case "failed":
      return "queue.retry.failedAgain";
    case "review":
      return "refund.st.review";
    default:
      return "queue.retry.started";
  }
}

export function retryOutcome(refunds: readonly { state: string; provider_refund_id?: string | null }[]): RefundLineState {
  const first = refunds[0];
  return first === undefined ? "other" : refundLineState({ state: first.state, provider_refund_id: first.provider_refund_id ?? null });
}

const LETTER_KIND_KEYS: Record<string, MessageKey> = {
  ticket: "queue.letter.kind.ticket",
  change: "queue.letter.kind.change",
  cancel: "queue.letter.kind.cancel",
  refund: "queue.letter.kind.refund",
  referral_cashback: "queue.letter.kind.other",
  referral_unavailable: "queue.letter.kind.other",
};

export function letterKindKey(kind: string): MessageKey {
  return LETTER_KIND_KEYS[kind] ?? "queue.letter.kind.other";
}

export function letterProblemKey(problem: string): MessageKey {
  return problem === "failed" ? "queue.letter.failed" : "queue.letter.stuck";
}

/** Only a ticket letter can be sent again from here: the resend route sends the order's tickets. */
export function canResend(letter: Pick<UndeliveredLetter, "kind">, canUpdate: boolean): boolean {
  return canUpdate && letter.kind === "ticket";
}

/** A stable key of a letter row: an order may have a ticket letter and a refund letter at once. */
export function letterRowKey(l: Pick<UndeliveredLetter, "order_id" | "kind">): string {
  return l.order_id + ":" + l.kind;
}
