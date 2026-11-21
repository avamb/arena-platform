/**
 * "Refund outside the platform" on the card of a paid order (spec 62 14.6, P-14).
 *
 * For orders of the WordPress era, whose money was returned in the payment
 * provider's own cabinet (AllPay). The person ticks the tickets, types the FINAL
 * amount returned for each (at most its price) and the provider's refund number,
 * checks the summary and records it. Arena moves no money: the server voids the
 * tickets and books one final refund per ticket in one transaction. A ticket that
 * already has a refund is refused with the amount recorded.
 *
 * Money route: the password is asked again (`withStepUp`, spec 37 4.6).
 */
import { useEffect, useId, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";
import { useStepUp } from "@/components/StepUpDialog";
import { useDialogBehaviour } from "@/components/useDialogBehaviour";
import { recordExternalRefund } from "@/lib/api/endpoints";
import type { ExternalRefundResponse, OrderDetail } from "@/lib/api/types";
import {
  MAX_PROVIDER_REF,
  canOfferExternalRefund,
  emptyDraft,
  externalRefundRefusal,
  planExternalRefund,
  refundableLines,
  toggleLine,
  type DraftProblem,
  type ExternalRefundDraft,
  type Planned,
} from "@/lib/externalRefund";
import { formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { orderNumber } from "@/lib/orders";
import { StepUpCancelled, withStepUp } from "@/lib/stepUp";

export interface ExternalRefundProps {
  orgId: string;
  order: OrderDetail;
  /** refund.create (the manager holds it since migration 0129). */
  canCreate: boolean;
}

export function ExternalRefund({ orgId, order, canCreate }: ExternalRefundProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState<ExternalRefundResponse | null>(null);
  // The card is read again after a record, so the button may vanish while the
  // result is still on screen: the result stays until it is closed.
  if (!open && done === null && !canOfferExternalRefund(order, canCreate)) return null;
  return (
    <>
      {!open && canOfferExternalRefund(order, canCreate) && (
        <p>
          <button type="button" className="btn" onClick={() => setOpen(true)} data-testid="extref-open">
            {t("extref.button")}
          </button>
        </p>
      )}
      {open && (
        <ExternalRefundDialog
          orgId={orgId}
          order={order}
          onDone={setDone}
          onClose={() => {
            setOpen(false);
            setDone(null);
          }}
        />
      )}
    </>
  );
}

type Step = "pick" | "final" | "result";

function ExternalRefundDialog({
  orgId,
  order,
  onDone,
  onClose,
}: {
  orgId: string;
  order: OrderDetail;
  onDone: (r: ExternalRefundResponse) => void;
  onClose: () => void;
}) {
  const { t, locale } = useI18n();
  const stepUp = useStepUp();
  const queryClient = useQueryClient();
  const ids = useId();
  const box = useRef<HTMLDivElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const busyRef = useRef(false);
  const { onKeyDown } = useDialogBehaviour(box, title, () => {
    if (!busyRef.current) onClose();
  });

  const [step, setStep] = useState<Step>("pick");
  const [draft, setDraft] = useState<ExternalRefundDraft>(emptyDraft);
  const [problem, setProblem] = useState<DraftProblem | null>(null);
  const [plan, setPlan] = useState<Extract<Planned, { ok: true }> | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [result, setResult] = useState<ExternalRefundResponse | null>(null);

  useEffect(() => {
    title.current?.focus();
  }, [step]);

  const number = orderNumber(order.system_id);
  const lines = refundableLines(order);
  const currency = lines[0]?.currency ?? order.currency ?? "";
  const money = (minor: number, cur: string = currency) => formatMoney(minor, cur, locale);
  const numberOf = (ticketId: string) => order.tickets.find((x) => x.id === ticketId)?.system_ticket_id ?? ticketId.slice(0, 8);

  const check = () => {
    const p = planExternalRefund(order, draft);
    if (!p.ok) {
      setProblem(p.problem);
      return;
    }
    setProblem(null);
    setFailure(null);
    setPlan(p);
    setStep("final");
  };

  const record = async () => {
    if (plan === null || busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    setFailure(null);
    try {
      const res = await withStepUp(orgId, (headers) => recordExternalRefund(orgId, order.id, plan.request, headers), stepUp.ask);
      setResult(res);
      onDone(res);
      setStep("result");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["order", orgId, order.id] }),
        queryClient.invalidateQueries({ queryKey: ["orders", orgId] }),
      ]);
    } catch (err) {
      if (err instanceof StepUpCancelled) return;
      setFailure({ error: err });
      setStep("pick");
      // A definite refusal may mean the card is stale: read it again.
      void queryClient.invalidateQueries({ queryKey: ["order", orgId, order.id] });
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const refusal = externalRefundRefusal(failure?.error);
  const refusalText = (() => {
    if (refusal === null) return null;
    switch (refusal.kind) {
      case "exists":
        return t("extref.err.exists", { number: numberOf(refusal.ticketId), amount: money(refusal.amount, refusal.currency || currency) });
      case "notActive":
        return t("extref.err.notActive", { number: numberOf(refusal.ticketId) });
      case "tooMuch":
        return t("extref.err.tooMuch", { number: numberOf(refusal.ticketId) });
      case "notRefundable":
        return t("extref.err.notRefundable");
    }
  })();

  return (
    <div className="dialog-backdrop">
      <div
        ref={box}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${ids}-title`}
        data-testid="extref-dialog"
        onKeyDown={onKeyDown}
      >
        <h2 id={`${ids}-title`} ref={title} tabIndex={-1}>
          {step === "result" ? t("extref.done.title", { number }) : t("extref.title", { number })}
        </h2>

        {step === "pick" && (
          <form
            noValidate
            onSubmit={(e) => {
              e.preventDefault();
              check();
            }}
            data-testid="extref-pick"
          >
            <p>{t("extref.intro")}</p>
            <p className="muted">{t("extref.once")}</p>
            {refusalText !== null && (
              <p className="notice notice-bad" role="alert" data-testid="extref-refusal">
                {refusalText}
              </p>
            )}
            {failure !== null && refusal === null && <ErrorNotice error={failure.error} testId="extref-failure" />}
            <fieldset className="check-list">
              <legend>{t("extref.tickets")}</legend>
              <ul>
                {lines.map((tk) => {
                  const on = draft.selected.includes(tk.id);
                  return (
                    <li key={tk.id}>
                      <label className="check">
                        <input
                          type="checkbox"
                          checked={on}
                          onChange={() => setDraft(toggleLine(draft, tk))}
                          data-testid="extref-ticket"
                          data-ticket={tk.id}
                        />
                        <span>{t("extref.ticketLine", { number: tk.system_ticket_id, price: money(tk.price, tk.currency) })}</span>
                      </label>
                      {on && (
                        <TextField
                          label={t("extref.amountLabel", { number: tk.system_ticket_id, max: money(tk.price, tk.currency) })}
                          value={draft.amounts[tk.id] ?? ""}
                          onChange={(v) => {
                            setDraft({ ...draft, amounts: { ...draft.amounts, [tk.id]: v } });
                            setProblem(null);
                          }}
                          testId={`extref-amount-${tk.system_ticket_id}`}
                          maxLength={16}
                        />
                      )}
                    </li>
                  );
                })}
              </ul>
            </fieldset>
            <TextField
              label={t("extref.ref")}
              hint={t("extref.refHint")}
              value={draft.providerRef}
              onChange={(v) => {
                setDraft({ ...draft, providerRef: v });
                setProblem(null);
              }}
              testId="extref-ref"
              maxLength={MAX_PROVIDER_REF}
            />
            {problem !== null && (
              <p className="field-error" role="alert" data-testid="extref-problem">
                {t(problem.key, problem.params)}
              </p>
            )}
            <div className="dialog-actions">
              <button type="submit" className="btn btn-primary" data-testid="extref-check">
                {t("extref.check")}
              </button>
              <button type="button" className="btn" onClick={onClose} data-testid="extref-close">
                {t("extref.close")}
              </button>
            </div>
          </form>
        )}

        {step === "final" && plan !== null && (
          <div data-testid="extref-final">
            <h3>{t("extref.final.title")}</h3>
            <ul className="facts">
              {plan.lines.map((l) => (
                <li key={l.ticket.id} data-testid="extref-final-line">
                  {t("extref.final.line", { number: l.ticket.system_ticket_id, amount: money(l.amount, l.ticket.currency) })}
                </li>
              ))}
            </ul>
            <p>
              <strong data-testid="extref-final-total">{t("extref.final.total", { amount: money(plan.total) })}</strong>
            </p>
            <p>{t("extref.final.ref", { ref: plan.request.items[0]?.provider_refund_ref ?? "" })}</p>
            <p className="notice notice-bad" role="note">
              {t("extref.final.warning")}
            </p>
            <div className="dialog-actions">
              <button type="button" className="btn btn-primary" onClick={() => void record()} disabled={busy} data-testid="extref-record">
                {busy ? t("extref.recording") : t("extref.record", { amount: money(plan.total) })}
              </button>
              <button type="button" className="btn" onClick={() => setStep("pick")} disabled={busy} data-testid="extref-back">
                {t("extref.back")}
              </button>
            </div>
          </div>
        )}

        {step === "result" && result !== null && (
          <div data-testid="extref-result">
            <p>{t("extref.done.body", { count: result.items.length, amount: money(result.total_minor, result.currency) })}</p>
            <div className="dialog-actions">
              <button type="button" className="btn btn-primary" onClick={onClose} data-testid="extref-done">
                {t("extref.close")}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
