/**
 * 8-1: "Refund" on the card of a paid order. The web twin of the bot's
 * refund dialog (eventbot/refund_dialog.go, `or:f:`), rules in
 * src/lib/refunds.ts.
 *
 * 1. The button reads GET .../refund-options and branches on the SERVER's
 *    `route`: arena returns the money (choose tickets), or an explanation for
 *    a seller's site, an unsupported provider, an order without payment.
 * 2. The person ticks tickets (none at the start), may return a part of ONE
 *    ticket, picks a reason and the buyer letter.
 * 3. "Continue" shows the final check: tickets, amount, letter, and that the
 *    tickets stop being valid even for a part of their price.
 * 4. "Refund <amount>" opens the typed word (REFUND / ВЕРНУТЬ / DEVOLVER,
 *    ConfirmWordDialog); the right word asks for the password again
 *    (`withStepUp`, spec 37 4.6) and only then sends the request.
 *
 * One operation has one Idempotency-Key, minted at the final check and kept
 * while the plan is the same. When the answer is lost (network, timeout, a
 * gateway), the screen does NOT say "failed": it offers "Check the refund",
 * which repeats the same request with the same key, so the server answers
 * the first operation and never returns the money twice.
 */
import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";
import { useStepUp } from "@/components/StepUpDialog";
import { ErrorBox, Loading } from "@/components/ui";
import { createOrderRefunds, fetchRefundOptions } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import type { CreateOrderRefundsResponse, OrderRefundOptions, OrderRefundTicketOption } from "@/lib/api/types";
import { formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { orderNumber } from "@/lib/orders";
import {
  MAX_REFUND_REASON,
  REFUND_REASONS,
  canOfferRefund,
  clearPendingRefund,
  emptyRefundDraft,
  inProgressTickets,
  keyFor,
  loadPendingRefund,
  outcomeUnknown,
  partialAllowed,
  planRefund,
  providerName,
  refundEntry,
  refundLineState,
  refundRequest,
  refundableTickets,
  savePendingRefund,
  scannedRefusal,
  scannedTickets,
  selectAll,
  ticketLabel,
  toggleTicket,
  type RefundDraft,
  type RefundPlan,
  type ScannedRefusal,
} from "@/lib/refunds";
import { StepUpCancelled, withStepUp } from "@/lib/stepUp";

export interface RefundOrderProps {
  orgId: string;
  orderId: string;
  systemId: number;
  /** The status the card shows now. */
  status: string;
  canCreate: boolean;
  /** Without refund.approve the refund is recorded and waits for approval. */
  canApprove: boolean;
}

type Step = "pick" | "final";

interface HeldKey {
  key: string;
  fingerprint: string;
}

export function RefundOrder({ orgId, orderId, systemId, status, canCreate, canApprove }: RefundOrderProps) {
  const { t, locale } = useI18n();
  const stepUp = useStepUp();
  const queryClient = useQueryClient();
  const ids = useId();
  const heading = useRef<HTMLHeadingElement>(null);

  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<Step>("pick");
  const [draft, setDraft] = useState<RefundDraft>(emptyRefundDraft);
  const [problem, setProblem] = useState<MessageKey | null>(null);
  const [plan, setPlan] = useState<RefundPlan | null>(null);
  const [held, setHeld] = useState<HeldKey | null>(null);
  const [wordOpen, setWordOpen] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [checking, setChecking] = useState(false);
  const [result, setResult] = useState<CreateOrderRefundsResponse | null>(null);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [scanned, setScanned] = useState<ScannedRefusal | null>(null);

  const options = useQuery({
    queryKey: ["refund-options", orgId, orderId],
    queryFn: () => fetchRefundOptions(orgId, orderId),
    enabled: open,
  });

  useEffect(() => {
    if (open) heading.current?.focus();
  }, [open, step]);

  // A request of this tab whose answer never came (a reload, a closed tab):
  // the panel opens on "Check the refund" with the SAME key and plan.
  const restorePending = (): boolean => {
    const p = loadPendingRefund(orgId, orderId);
    if (p === null) return false;
    setPlan(p.plan);
    setHeld({ key: p.key, fingerprint: p.fingerprint });
    setUnknown(true);
    return true;
  };
  useEffect(() => {
    if (restorePending()) setOpen(true);
    // Only when the card of another order is shown.
  }, [orgId, orderId]);

  const number = orderNumber(systemId);
  const offer = canOfferRefund(status, canCreate);
  // The panel stays reachable while an answer is outstanding, even after
  // the card's status moved on.
  if (!offer && !open && !unknown && result === null) return null;

  const reload = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["order", orgId, orderId] }),
      queryClient.invalidateQueries({ queryKey: ["orders", orgId] }),
      queryClient.invalidateQueries({ queryKey: ["refund-options", orgId, orderId] }),
    ]);

  const startOver = () => {
    setStep("pick");
    setDraft(emptyRefundDraft());
    setPlan(null);
    setHeld(null);
    setProblem(null);
  };

  const openPanel = () => {
    setOpen(true);
    setResult(null);
    setFailure(null);
    setScanned(null);
    if (!unknown && !restorePending()) startOver();
    void options.refetch();
  };

  const closePanel = () => {
    setOpen(false);
    setWordOpen(false);
  };

  /** Sends the plan with its key, behind the password question. */
  const send = (p: RefundPlan, k: HeldKey) =>
    withStepUp(
      orgId,
      (headers) => {
        // Stored before the request leaves: a reload during the call still
        // finds the key (cleared again once a final answer comes).
        savePendingRefund(orgId, orderId, { key: k.key, fingerprint: k.fingerprint, plan: p });
        return createOrderRefunds(orgId, orderId, refundRequest(p), k.key, headers);
      },
      stepUp.ask,
    );

  /** A final answer came: the stored operation is done with. */
  const settled = () => clearPendingRefund(orgId, orderId);

  const finished = async (res: CreateOrderRefundsResponse) => {
    settled();
    setResult(res);
    setUnknown(false);
    startOver();
    await reload();
  };

  /** Errors that end the attempt: shown on the panel, the word dialog closes. */
  const refused = async (err: unknown): Promise<boolean> => {
    if (outcomeUnknown(err)) {
      setUnknown(true);
      return true;
    }
    if (err instanceof ApiError) {
      // A definite answer: the operation is over, nothing is left to check.
      setUnknown(false);
      settled();
    }
    const sc = scannedRefusal(err);
    if (sc !== null) {
      setScanned(sc);
      startOver();
      await reload();
      return true;
    }
    // The order changed under the screen (a refund under way, a ticket no
    // longer refundable, nothing left) or the provider cannot do it: nothing
    // was refunded, the panel says why and shows the latest options.
    if (err instanceof ApiError && (err.status === 409 || err.status === 422)) {
      setFailure({ error: err });
      startOver();
      await reload();
      return true;
    }
    return false;
  };

  const onConfirm = async () => {
    if (plan === null || held === null) return;
    let res: CreateOrderRefundsResponse;
    try {
      res = await send(plan, held);
    } catch (err) {
      // The person closed the password question: nothing was sent, the dialog stays.
      if (err instanceof StepUpCancelled) return;
      if (await refused(err)) {
        setWordOpen(false);
        return;
      }
      throw err; // the dialog shows what went wrong and what to do
    }
    setWordOpen(false);
    await finished(res);
  };

  const checkAgain = async () => {
    if (plan === null || held === null) return;
    setChecking(true);
    setFailure(null);
    try {
      await finished(await send(plan, held));
    } catch (err) {
      if (err instanceof StepUpCancelled) return;
      if (await refused(err)) return;
      // Any other definite refusal (400, 403, …): refused() already cleared
      // the stored key; startOver() below drops plan and key together, so the
      // panel never shows "Check" without something to check.
      settled();
      setUnknown(false);
      setFailure({ error: err });
      startOver();
      await reload();
    } finally {
      setChecking(false);
    }
  };

  const toFinal = (o: OrderRefundOptions) => {
    const p = planRefund(draft, o, t);
    if (!p.ok) {
      setProblem(p.error);
      return;
    }
    setProblem(null);
    setPlan(p.value);
    setHeld(keyFor(held, p.value));
    setStep("final");
  };

  const money = (minor: number, currency: string) => formatMoney(minor, currency, locale);

  return (
    <>
      {offer && !open && (
        <p>
          <button type="button" className="btn" onClick={openPanel} data-testid="refund-open">
            {t("refund.button")}
          </button>
        </p>
      )}
      {!open && result !== null && <RefundResult result={result} number={number} options={options.data} />}
      {open && (
        <section className="panel" aria-labelledby={`${ids}-title`} data-testid="refund-panel">
          <h2 id={`${ids}-title`} ref={heading} tabIndex={-1}>
            {t("refund.title", { number })}
          </h2>
          {result !== null && <RefundResult result={result} number={number} options={options.data} />}
          {scanned !== null && <ScannedNotice refusal={scanned} options={options.data} />}
          {failure !== null && <ErrorNotice error={failure.error} testId="refund-failure" />}
          {unknown ? (
            <div className="notice notice-bad" role="alert" data-testid="refund-unknown">
              <p>
                <strong>{t("refund.unknown.title")}</strong>
              </p>
              <p>{t("refund.unknown.next")}</p>
              <p>
                <button type="button" className="btn btn-primary" onClick={() => void checkAgain()} disabled={checking} data-testid="refund-check">
                  {checking ? t("refund.unknown.checking") : t("refund.unknown.check")}
                </button>
              </p>
            </div>
          ) : options.isLoading ? (
            <Loading />
          ) : options.data === undefined ? (
            <ErrorBox error={options.error} onRetry={() => void options.refetch()} />
          ) : result === null ? (
            <RefundBody
              o={options.data}
              number={number}
              step={step}
              draft={draft}
              setDraft={(d) => {
                setDraft(d);
                setProblem(null);
              }}
              problem={problem}
              plan={plan}
              canApprove={canApprove}
              onContinue={() => toFinal(options.data as OrderRefundOptions)}
              onBack={() => setStep("pick")}
              onGo={() => setWordOpen(true)}
              money={money}
            />
          ) : null}
          <p>
            <button type="button" className="btn" onClick={closePanel} data-testid="refund-close">
              {t("refund.close")}
            </button>
          </p>
        </section>
      )}
      {wordOpen && plan !== null && (
        <ConfirmWordDialog
          kind="refund"
          subject={t("refund.word.subject", {
            amount: money(plan.total, plan.currency),
            count: plan.tickets.length,
            number,
          })}
          onConfirm={onConfirm}
          onCancel={() => setWordOpen(false)}
        />
      )}
    </>
  );
}

interface BodyProps {
  o: OrderRefundOptions;
  number: string;
  step: Step;
  draft: RefundDraft;
  setDraft: (d: RefundDraft) => void;
  problem: MessageKey | null;
  plan: RefundPlan | null;
  canApprove: boolean;
  onContinue: () => void;
  onBack: () => void;
  onGo: () => void;
  money: (minor: number, currency: string) => string;
}

function ticketText(tk: OrderRefundTicketOption): string {
  return tk.seat_label !== null && tk.seat_label !== undefined && tk.seat_label !== ""
    ? `${ticketLabel(tk)} · ${tk.seat_label}`
    : ticketLabel(tk);
}

function RefundBody({ o, number, step, draft, setDraft, problem, plan, canApprove, onContinue, onBack, onGo, money }: BodyProps) {
  const { t } = useI18n();
  const ids = useId();
  const provider = providerName(o.provider);
  const entry = refundEntry(o);
  const scannedList = scannedTickets(o);
  const busy = inProgressTickets(o);

  const notes = (
    <>
      {busy.length > 0 && (
        <p className="notice" role="status" data-testid="refund-in-progress">
          {t("refund.inProgressNote", { tickets: busy.map(ticketLabel).join(", ") })}
        </p>
      )}
      {scannedList.length > 0 && entry === "pick" && (
        <p className="notice" role="status" data-testid="refund-scanned-note">
          {t("refund.scannedNote", { tickets: scannedList.map(ticketLabel).join(", ") })}
        </p>
      )}
    </>
  );

  if (entry !== "pick") {
    const key: Record<Exclude<typeof entry, "pick">, MessageKey> = {
      nothing: "refund.nothing",
      scanned_all: "refund.scannedAll",
      site: "refund.site",
      unsupported: "refund.unsupported",
      none: "refund.none",
      unknown_provider: "refund.unknownProvider",
    };
    return (
      <>
        {notes}
        <p className="notice" role="status" data-testid="refund-explain" data-entry={entry}>
          {t(key[entry], { number, provider })}
        </p>
      </>
    );
  }

  if (step === "final" && plan !== null) {
    return (
      <div data-testid="refund-final">
        <h3>{t("refund.final.title")}</h3>
        <dl className="facts-grid">
          <dt>{t("refund.final.order")}</dt>
          <dd>{number}</dd>
          <dt>{t("refund.final.tickets")}</dt>
          <dd>
            <ul className="facts">
              {plan.tickets.map((x) => (
                <li key={x.ticket.ticket_id} data-testid="refund-final-ticket">
                  {ticketText(x.ticket)} · {money(x.amount, plan.currency)}
                </li>
              ))}
            </ul>
          </dd>
          <dt>{t("refund.final.amount")}</dt>
          <dd data-testid="refund-final-amount">
            <strong>{money(plan.total, plan.currency)}</strong> · {t("refund.final.through", { provider })}
          </dd>
          <dt>{t("refund.final.reason")}</dt>
          <dd>{plan.reason}</dd>
          <dt>{t("refund.final.letter")}</dt>
          <dd data-testid="refund-final-letter">{t(plan.notifyBuyer ? "refund.final.letterYes" : "refund.final.letterNo")}</dd>
        </dl>
        <p className="notice notice-bad" role="note">
          {t("refund.final.warning")}
        </p>
        {!canApprove && (
          <p className="notice" role="note" data-testid="refund-needs-approval">
            {t("refund.final.needsApproval")}
          </p>
        )}
        <div className="actions">
          <button type="button" className="btn btn-primary" onClick={onGo} data-testid="refund-go">
            {t("refund.final.go", { amount: money(plan.total, plan.currency) })}
          </button>
          <button type="button" className="btn" onClick={onBack} data-testid="refund-back">
            {t("refund.back")}
          </button>
        </div>
      </div>
    );
  }

  const open = refundableTickets(o);
  const partialOk = partialAllowed(draft, o);
  const onlyOne = open.find((x) => x.ticket_id === draft.selected[0]);
  return (
    <form
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        onContinue();
      }}
      data-testid="refund-pick"
    >
      <p>{t("refund.intro", { provider })}</p>
      {notes}
      <fieldset className="check-list">
        <legend>{t("refund.tickets")}</legend>
        <ul>
          {open.map((tk) => (
            <li key={tk.ticket_id}>
              <label className="check">
                <input
                  type="checkbox"
                  checked={draft.selected.includes(tk.ticket_id)}
                  onChange={() => setDraft(toggleTicket(draft, tk.ticket_id))}
                  data-testid="refund-ticket"
                  data-ticket={tk.ticket_id}
                />
                <span>
                  {ticketText(tk)} · {t("refund.refundable", { amount: money(tk.refundable, o.currency) })}
                </span>
              </label>
            </li>
          ))}
        </ul>
        {open.length > 1 && (
          <button type="button" className="btn" onClick={() => setDraft(selectAll(draft, o))} data-testid="refund-select-all">
            {t("refund.selectAll")}
          </button>
        )}
      </fieldset>

      {partialOk && onlyOne !== undefined && (
        <fieldset>
          <legend>{t("refund.amount")}</legend>
          <label className="check">
            <input type="radio" name={`${ids}-part`} checked={!draft.partial} onChange={() => setDraft({ ...draft, partial: false })} data-testid="refund-whole" />
            <span>{t("refund.whole", { amount: money(onlyOne.refundable, o.currency) })}</span>
          </label>
          <label className="check">
            <input type="radio" name={`${ids}-part`} checked={draft.partial} onChange={() => setDraft({ ...draft, partial: true })} data-testid="refund-partial" />
            <span>{t("refund.partial")}</span>
          </label>
          {draft.partial && (
            <TextField
              label={t("refund.partLabel", { amount: money(onlyOne.refundable, o.currency) })}
              value={draft.partText}
              onChange={(v) => setDraft({ ...draft, partText: v })}
              testId="refund-part"
              maxLength={16}
            />
          )}
        </fieldset>
      )}

      <fieldset>
        <legend>{t("refund.reason")}</legend>
        {[...REFUND_REASONS, "other" as const].map((r) => (
          <label key={r} className="check">
            <input
              type="radio"
              name={`${ids}-reason`}
              checked={draft.reason === r}
              onChange={() => setDraft({ ...draft, reason: r })}
              data-testid={`refund-reason-${r}`}
            />
            <span>{t(`refund.reason.${r}` as MessageKey)}</span>
          </label>
        ))}
        {draft.reason === "other" && (
          <TextField
            label={t("refund.reasonText", { max: MAX_REFUND_REASON })}
            value={draft.reasonText}
            onChange={(v) => setDraft({ ...draft, reasonText: v })}
            testId="refund-reason-text"
            maxLength={MAX_REFUND_REASON}
          />
        )}
      </fieldset>

      <label className="check">
        <input
          type="checkbox"
          checked={draft.notifyBuyer}
          onChange={() => setDraft({ ...draft, notifyBuyer: !draft.notifyBuyer })}
          data-testid="refund-letter"
        />
        <span>{t("refund.letter")}</span>
      </label>

      {problem !== null && (
        <p className="field-error" role="alert" data-testid="refund-problem">
          {t(problem)}
        </p>
      )}
      <div className="actions">
        <button type="submit" className="btn btn-primary" data-testid="refund-continue">
          {t("refund.continue")}
        </button>
      </div>
    </form>
  );
}

const LINE_KEYS: Record<ReturnType<typeof refundLineState>, MessageKey> = {
  succeeded: "refund.st.succeeded",
  pending: "refund.st.pending",
  queued: "refund.st.queued",
  awaiting_approval: "refund.st.awaitingApproval",
  failed: "refund.st.failed",
  review: "refund.st.review",
  other: "refund.st.other",
};

function RefundResult({
  result,
  number,
  options,
}: {
  result: CreateOrderRefundsResponse;
  number: string;
  options: OrderRefundOptions | undefined;
}) {
  const { t, locale } = useI18n();
  const provider = providerName(options?.provider ?? "");
  const label = (ticketId: string | null | undefined) => {
    const tk = options?.tickets.find((x) => x.ticket_id === ticketId);
    return tk !== undefined ? ticketLabel(tk) : (ticketId ?? "").slice(0, 8);
  };
  return (
    <div className="notice" role="status" data-testid="refund-result">
      <p>
        <strong>{t("refund.done.title", { number })}</strong>
      </p>
      {result.replayed && <p>{t("refund.done.replayed")}</p>}
      <ul className="facts">
        {result.refunds.map((r) => {
          const st = refundLineState(r);
          return (
            <li key={r.id} data-testid="refund-result-line" data-state={st}>
              {t("refund.line", {
                ticket: label(r.ticket_id),
                amount: formatMoney(r.amount, r.currency, locale),
                status: t(LINE_KEYS[st], {
                  provider,
                  why: r.failure_reason ?? r.failure_code ?? t("refund.st.failedNoReason"),
                  state: r.state,
                }),
              })}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function ScannedNotice({ refusal, options }: { refusal: ScannedRefusal; options: OrderRefundOptions | undefined }) {
  const { t } = useI18n();
  const tk = options?.tickets.find((x) => x.ticket_id === refusal.ticketId);
  const ticket = tk !== undefined ? ticketLabel(tk) : (refusal.ticketId ?? "").slice(0, 8);
  return (
    <div className="notice notice-bad" role="alert" data-testid="refund-scanned">
      <p>
        <strong>{t("refund.scanned.title", { ticket })}</strong>
      </p>
      {refusal.notice !== "silent" && <p>{t(refusal.notice === "notified" ? "refund.scanned.notified" : "refund.scanned.nobody")}</p>}
      <p>{t("refund.scanned.next")}</p>
    </div>
  );
}
