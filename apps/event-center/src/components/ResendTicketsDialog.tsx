/**
 * "Resend tickets" on the order card (4-1), the web twin of the bot's resend
 * dialog (eventbot/resend.go, spec 35 section 6.4). Four steps, as in the bot:
 *
 *   choose   "To the same address" (the order's, masked) or "To another address"
 *   email    the person types the one-time address
 *   confirm  a last press that shows where the letter goes. Nothing is queued before it.
 *   result   queued, or the refusal with its reason
 *
 * The order's own address never changes: a typed address goes to this one resend
 * only (the server keeps it for 24 hours on the delivery task), and the screens
 * say so. An order sold through a seller's own site is refused by the API
 * (409 order.seller_site_order, the site writes its own letters) and the result
 * says so with the order number, exactly as the bot does.
 */
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { useDialogBehaviour } from "@/components/useDialogBehaviour";
import { resendOrderTickets } from "@/lib/api/endpoints";
import type { OrderDetail, OrderResendTicketsResponse } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { orderNumber } from "@/lib/orders";
import {
  activeTickets,
  maskEmail,
  orderAddress,
  resendFailure,
  resendFailureKey,
  validResendEmail,
  type ResendFailure,
} from "@/lib/resend";

type Step = "choose" | "email" | "confirm" | "result";

type Outcome =
  | { kind: "done"; response: OrderResendTicketsResponse }
  | { kind: "refused"; failure: ResendFailure }
  | { kind: "error"; error: unknown };

export interface ResendTicketsDialogProps {
  orgId: string;
  order: OrderDetail;
  onClose: () => void;
}

export function ResendTicketsDialog({ orgId, order, onClose }: ResendTicketsDialogProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const ids = useId();
  const box = useRef<HTMLDivElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const busyRef = useRef(false);
  const { onKeyDown } = useDialogBehaviour(box, title, () => {
    if (!busyRef.current) onClose();
  });

  const address = orderAddress(order);
  const count = activeTickets(order);
  const number = orderNumber(order.system_id);

  const [step, setStep] = useState<Step>("choose");
  const [typed, setTyped] = useState("");
  // "" means the order's own address.
  const [oneTime, setOneTime] = useState("");
  const [note, setNote] = useState<"badEmail" | "noRecipient" | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome | null>(null);

  // The step changed: the control that had the focus is gone, so the title takes it.
  useEffect(() => {
    title.current?.focus();
  }, [step]);

  const toEmail = () => {
    setNote(null);
    setStep("email");
  };

  const onEmailSubmit = (e: FormEvent) => {
    e.preventDefault();
    const addr = typed.trim();
    if (!validResendEmail(addr)) {
      setNote("badEmail");
      return;
    }
    setNote(null);
    setOneTime(addr);
    setStep("confirm");
  };

  const send = async () => {
    if (busyRef.current) return; // one press at a time
    busyRef.current = true;
    setBusy(true);
    try {
      const response = await resendOrderTickets(orgId, order.id, oneTime === "" ? undefined : oneTime);
      setOutcome({ kind: "done", response });
      setStep("result");
      // The card shows the new letter status as soon as it is read again.
      void queryClient.invalidateQueries({ queryKey: ["order", orgId, order.id] });
    } catch (error) {
      const failure = resendFailure(error);
      if (failure === "badEmail" || failure === "noRecipient") {
        setNote(failure);
        setOneTime("");
        setStep("email");
      } else if (failure !== null) {
        setOutcome({ kind: "refused", failure });
        setStep("result");
      } else {
        setOutcome({ kind: "error", error });
        setStep("result");
      }
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const same = oneTime === "";
  const confirmTo = same ? maskEmail(address) : oneTime;

  return (
    <div className="dialog-backdrop">
      <div
        ref={box}
        className="dialog resend-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${ids}-title`}
        data-testid="resend-dialog"
        onKeyDown={onKeyDown}
      >
        <h2 id={`${ids}-title`} ref={title} tabIndex={-1}>
          {step === "confirm" ? t("resend.confirmTitle") : t("resend.title", { number })}
        </h2>

        {step === "choose" && (
          <div data-testid="resend-choose">
            <p>{t(address === "" ? "resend.chooseNoAddr" : "resend.choose", { count })}</p>
            {address !== "" && <p>{t("resend.chooseAddress", { email: maskEmail(address) })}</p>}
            {address !== "" && <p>{t("resend.chooseWhere")}</p>}
            <div className="dialog-actions">
              {address !== "" && (
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => {
                    setOneTime("");
                    setStep("confirm");
                  }}
                  data-testid="resend-same"
                >
                  {t("resend.sameBtn", { email: maskEmail(address) })}
                </button>
              )}
              <button type="button" className="btn" onClick={toEmail} data-testid="resend-other">
                {t("resend.otherBtn")}
              </button>
              <button type="button" className="btn" onClick={onClose} data-testid="resend-cancel">
                {t("resend.close")}
              </button>
            </div>
          </div>
        )}

        {step === "email" && (
          <form onSubmit={onEmailSubmit} noValidate data-testid="resend-email-form">
            <p>{t("resend.otherWarn")}</p>
            <label htmlFor={`${ids}-email`}>{t("resend.emailLabel")}</label>
            <input
              id={`${ids}-email`}
              type="email"
              inputMode="email"
              autoComplete="off"
              spellCheck={false}
              value={typed}
              placeholder={t("resend.emailHint")}
              onChange={(e) => setTyped(e.target.value)}
              aria-invalid={note === "badEmail"}
              aria-describedby={note !== null ? `${ids}-note` : undefined}
              data-testid="resend-email"
            />
            {note !== null && (
              <p id={`${ids}-note`} className="notice notice-bad" role="alert" data-testid="resend-email-note">
                {t(note === "badEmail" ? "resend.badEmail" : "resend.noRecipient")}
              </p>
            )}
            <div className="dialog-actions">
              <button type="submit" className="btn btn-primary" data-testid="resend-continue">
                {t("resend.continue")}
              </button>
              <button
                type="button"
                className="btn"
                onClick={() => {
                  setNote(null);
                  setStep("choose");
                }}
                data-testid="resend-back"
              >
                {t("resend.back")}
              </button>
            </div>
          </form>
        )}

        {step === "confirm" && (
          <div data-testid="resend-confirm">
            <p data-testid="resend-confirm-text">
              {t(same ? "resend.confirmSame" : "resend.confirmOther", { number, count, email: confirmTo })}
            </p>
            <div className="dialog-actions">
              <button
                type="button"
                className="btn btn-primary"
                disabled={busy}
                onClick={() => void send()}
                data-testid="resend-send"
              >
                {busy ? t("resend.sending") : t("resend.sendBtn")}
              </button>
              <button
                type="button"
                className="btn"
                disabled={busy}
                onClick={() => setStep(same ? "choose" : "email")}
                data-testid="resend-back"
              >
                {t("resend.back")}
              </button>
            </div>
          </div>
        )}

        {step === "result" && outcome !== null && (
          <div data-testid="resend-result" aria-live="polite">
            {outcome.kind === "done" && (
              <>
                <p className="notice notice-ok" role="status" data-testid="resend-done">
                  {t(outcome.response.different_address ? "resend.doneOther" : "resend.doneSame", {
                    number,
                    count: outcome.response.queued_tickets,
                    email: outcome.response.recipient_masked,
                  })}
                </p>
                <p className="muted">{t("resend.queuedNote")}</p>
              </>
            )}
            {outcome.kind === "refused" && (
              <p className="notice notice-bad" role="alert" data-testid={`resend-refused-${outcome.failure}`}>
                {t(resendFailureKey(outcome.failure), { number })}
              </p>
            )}
            {outcome.kind === "error" && <ErrorNotice error={outcome.error} testId="resend-error" />}
            <div className="dialog-actions">
              {outcome.kind === "error" && (
                <button type="button" className="btn btn-primary" onClick={() => setStep("confirm")} data-testid="resend-retry">
                  {t("common.retry")}
                </button>
              )}
              <button type="button" className="btn" onClick={onClose} data-testid="resend-close">
                {t("resend.close")}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
