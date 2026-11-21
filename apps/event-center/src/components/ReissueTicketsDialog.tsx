/**
 * "Replace tickets" on the order card (spec 63, stage R-3), the web twin of the
 * bot's reissue dialog. Steps, as in spec section 7.2:
 *
 *   case     "Which address is the buyer writing from?" From the order's
 *            address leads into the existing resend (same barcodes); from
 *            another address goes on here.
 *   address  the buyer's real address, the order's masked one beside it, and
 *            the buyer's name and phone to compare with the person asking.
 *   reason   one of three reasons ("the address had a typo" is chosen at
 *            the start, O-4) and an optional note.
 *   check    what happens, in words: the old tickets stop admitting, where the
 *            new ones go, a warning per code already used at the door, the
 *            payment system that keeps the old address.
 *   word     the typed word (ConfirmWordDialog kind `reissue`). Nothing is
 *            sent before it.
 *   result   the new numbers and the letter in the queue.
 *
 * One Idempotency-Key per operation, kept while the request is the same. A lost
 * answer is not a failure: "Check" repeats the SAME request with the SAME key,
 * and the server answers the first operation (`replayed`). Every rule lives in
 * src/lib/reissue.ts; this file only draws.
 *
 * The same dialog replaces an invitation issued before spec 46 (stage R-5, a
 * complimentary issuance without an order): no case question (the resend has
 * no invitation twin), no buyer details and no payment system; the texts that
 * speak of an order or a buyer have invitation variants (`reissueText`).
 */
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { useDialogBehaviour } from "@/components/useDialogBehaviour";
import {
  fetchInvitationReissueOptions,
  fetchReissueOptions,
  reissueInvitationTickets,
  reissueOrderTickets,
  resendInvitationReissueLetter,
  resendReissueLetter,
} from "@/lib/api/endpoints";
import type { ReissueTicketsResponse } from "@/lib/api/types";
import { describeApiError } from "@/lib/errors";
import { formatDateTime } from "@/lib/format";
import { useI18n, type Params } from "@/lib/i18n";
import {
  DEFAULT_REISSUE_REASON,
  MAX_REISSUE_NOTE,
  REISSUE_REASONS,
  backToAddress,
  checkReissueAddress,
  checkReissueReason,
  noteLength,
  pairsText,
  providerKeepsOldAddress,
  reasonKey,
  reissueKeyFor,
  reissueOutcomeUnknown,
  reissueRequest,
  reissueStart,
  reissueText,
  reissuableTickets,
  scannedTickets,
  viewFromInvitationOptions,
  viewFromOrderOptions,
  type ReissuePlan,
  type ReissueReason,
  type ReissueTarget,
  type ReissueTextKey,
} from "@/lib/reissue";
import { maskEmail } from "@/lib/resend";
import { ApiError } from "@/lib/api/http";

type Step = "case" | "address" | "reason" | "check" | "unknown" | "result";

type Outcome = { kind: "done"; response: ReissueTicketsResponse } | { kind: "refused"; error: unknown };

export interface ReissueTicketsDialogProps {
  orgId: string;
  target: ReissueTarget;
  onClose: () => void;
  /** "From the order's address": the buyer only wants the same tickets elsewhere. Orders only. */
  onUseResend?: () => void;
}

export function ReissueTicketsDialog({ orgId, target, onClose, onUseResend }: ReissueTicketsDialogProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const ids = useId();
  const box = useRef<HTMLDivElement>(null);
  const title = useRef<HTMLHeadingElement>(null);
  const busyRef = useRef(false);
  const [wordOpen, setWordOpen] = useState(false);
  const { onKeyDown } = useDialogBehaviour(box, title, () => {
    if (!busyRef.current && !wordOpen) onClose();
  });

  const kind = target.kind;
  const rt = (key: ReissueTextKey, params?: Params) => t(reissueText(kind, key), params);
  const optionsQuery = useQuery({
    queryKey: ["reissue-options", kind, orgId, target.id],
    queryFn: async () =>
      kind === "order"
        ? viewFromOrderOptions(await fetchReissueOptions(orgId, target.id))
        : viewFromInvitationOptions(await fetchInvitationReissueOptions(orgId, target.id)),
    staleTime: 0,
  });

  // The case question belongs to an order: an invitation goes straight to the address.
  const withCase = kind === "order" && onUseResend !== undefined;
  const [step, setStep] = useState<Step>(withCase ? "case" : "address");
  const [typed, setTyped] = useState("");
  const [email, setEmail] = useState("");
  const [same, setSame] = useState(false);
  const [reason, setReason] = useState<ReissueReason | "">(DEFAULT_REISSUE_REASON);
  const [note, setNote] = useState("");
  const [problem, setProblem] = useState<"badEmail" | "reasonRequired" | "noteTooLong" | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const held = useRef<{ key: string; fingerprint: string } | null>(null);
  const [resent, setResent] = useState<"done" | { error: unknown } | null>(null);

  useEffect(() => {
    title.current?.focus();
  }, [step, optionsQuery.data]);

  const number = target.number;
  const tz = optionsQuery.data?.timezone || target.timezone;
  const refresh = () => {
    if (kind === "order") {
      void queryClient.invalidateQueries({ queryKey: ["order", orgId, target.id] });
      void queryClient.invalidateQueries({ queryKey: ["reissue-history", orgId, target.id] });
    } else {
      void queryClient.invalidateQueries({ queryKey: ["invitations", orgId] });
    }
    void queryClient.invalidateQueries({ queryKey: ["reissue-options", kind, orgId, target.id] });
  };

  const send = async () => {
    if (busyRef.current || reason === "") return; // one press at a time
    const plan: ReissuePlan = { email, reason, note };
    held.current = reissueKeyFor(held.current, plan);
    busyRef.current = true;
    setBusy(true);
    try {
      const body = reissueRequest(plan);
      const response =
        kind === "order"
          ? await reissueOrderTickets(orgId, target.id, body, held.current.key)
          : await reissueInvitationTickets(orgId, target.id, body, held.current.key);
      setOutcome({ kind: "done", response });
      setStep("result");
      refresh();
    } catch (error) {
      if (reissueOutcomeUnknown(error)) {
        setStep("unknown");
      } else if (backToAddress(error)) {
        setProblem("badEmail");
        setStep("address");
      } else {
        setOutcome({ kind: "refused", error });
        setStep("result");
        refresh();
      }
    } finally {
      busyRef.current = false;
      setBusy(false);
      setWordOpen(false);
    }
  };

  const resendLetter = async (reissueId: string) => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    try {
      if (kind === "order") await resendReissueLetter(orgId, target.id, reissueId);
      else await resendInvitationReissueLetter(orgId, target.id, reissueId);
      setResent("done");
      refresh();
    } catch (error) {
      setResent({ error });
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  };

  const onAddress = (e: FormEvent) => {
    e.preventDefault();
    const opts = optionsQuery.data;
    if (opts === undefined) return;
    const c = checkReissueAddress(typed, opts.email);
    if (!c.ok) {
      setProblem(c.note);
      return;
    }
    setProblem(null);
    setEmail(c.email);
    setSame(c.same);
    setStep("reason");
  };

  const onReason = (e: FormEvent) => {
    e.preventDefault();
    const c = checkReissueReason(reason, note);
    if (!c.ok) {
      setProblem(c.note);
      return;
    }
    setProblem(null);
    setStep("check");
  };

  const close = (
    <button type="button" className="btn" onClick={onClose} disabled={busy} data-testid="reissue-close">
      {t("reissue.close")}
    </button>
  );

  const renderBody = () => {
    if (optionsQuery.isLoading) return <p role="status">{t("common.loading")}</p>;
    const opts = optionsQuery.data;
    if (opts === undefined) {
      return (
        <div data-testid="reissue-load-failed">
          <p className="notice notice-bad" role="alert">
            {t("reissue.loadFailed")}
          </p>
          <div className="dialog-actions">{close}</div>
        </div>
      );
    }

    if (step === "result" && outcome !== null) {
      if (outcome.kind === "done") {
        const r = outcome.response;
        return (
          <div data-testid="reissue-result" aria-live="polite">
            <p className="notice notice-ok" role="status" data-testid="reissue-done">
              {rt("reissue.done", { email: r.recipient_masked })}
            </p>
            {r.replayed && <p data-testid="reissue-replayed">{t("reissue.done.replayed")}</p>}
            <p data-testid="reissue-done-pairs">{t("reissue.done.pairs", { pairs: pairsText(r.tickets) })}</p>
            <p className="muted">{rt("reissue.check.address")}</p>
            <div className="dialog-actions">{close}</div>
          </div>
        );
      }
      const text = describeApiError(outcome.error, t);
      const code = outcome.error instanceof ApiError ? outcome.error.code : "";
      return (
        <div data-testid="reissue-result" aria-live="polite">
          <p className="notice notice-bad" role="alert" data-testid="reissue-refused" data-code={code}>
            {text.title} {text.next}
          </p>
          <div className="dialog-actions">{close}</div>
        </div>
      );
    }

    if (step === "unknown") {
      return (
        <div data-testid="reissue-unknown">
          <p className="notice notice-bad" role="alert">
            {t("reissue.unknown")}
          </p>
          <div className="dialog-actions">
            <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void send()} data-testid="reissue-check-again">
              {busy ? t("reissue.unknown.checking") : t("reissue.unknown.check")}
            </button>
            {close}
          </div>
        </div>
      );
    }

    const start = reissueStart(opts);
    if (start.kind === "site") {
      return (
        <div>
          <p className="notice" role="status" data-testid="reissue-site">
            {t("reissue.site", { number })}
          </p>
          <div className="dialog-actions">{close}</div>
        </div>
      );
    }
    if (start.kind === "pending") {
      const key = start.state === "send_failed" ? "reissue.pending.send_failed" : "reissue.pending.pending";
      return (
        <div data-testid="reissue-pending" data-state={start.state}>
          <p className={`notice ${start.state === "send_failed" ? "notice-bad" : ""}`}>{rt(key, { email: maskEmail(start.email) })}</p>
          {resent === "done" && (
            <p className="notice notice-ok" role="status" data-testid="reissue-resent">
              {t("reissue.pending.resent")}
            </p>
          )}
          {resent !== null && resent !== "done" && (
            <p className="notice notice-bad" role="alert" data-testid="reissue-resend-failed">
              {describeApiError(resent.error, t).title} {describeApiError(resent.error, t).next}
            </p>
          )}
          <div className="dialog-actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || resent === "done"}
              onClick={() => void resendLetter(start.id)}
              data-testid="reissue-resend-letter"
            >
              {busy ? t("reissue.pending.sending") : t("reissue.pending.resend")}
            </button>
            {close}
          </div>
        </div>
      );
    }
    if (start.kind === "blocked") {
      const text = describeApiError(new ApiError(409, start.code, ""), t);
      return (
        <div>
          <p className="notice notice-bad" role="alert" data-testid="reissue-blocked" data-code={start.code}>
            {rt("reissue.blocked")} {text.title} {text.next}
          </p>
          <div className="dialog-actions">{close}</div>
        </div>
      );
    }

    const orderEmail = opts.email.trim();
    if (step === "case") {
      return (
        <div data-testid="reissue-case">
          <p>{t("reissue.case.question")}</p>
          {orderEmail !== "" && <p>{rt("reissue.case.orderAddress", { email: maskEmail(orderEmail) })}</p>}
          <ul className="plain-list">
            <li>
              <button type="button" className="btn" onClick={() => onUseResend?.()} data-testid="reissue-from-order">
                {t("reissue.case.fromOrder")}
              </button>
              <p className="muted small">{t("reissue.case.fromOrderHint")}</p>
            </li>
            <li>
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  setProblem(null);
                  setStep("address");
                }}
                data-testid="reissue-from-other"
              >
                {t("reissue.case.fromOther")}
              </button>
              <p className="muted small">{t("reissue.case.fromOtherHint")}</p>
            </li>
          </ul>
          <div className="dialog-actions">{close}</div>
        </div>
      );
    }

    if (step === "address") {
      return (
        <form onSubmit={onAddress} noValidate data-testid="reissue-address-form">
          {orderEmail !== "" && <p>{rt("reissue.case.orderAddress", { email: maskEmail(orderEmail) })}</p>}
          <div className="facts" data-testid="reissue-compare">
            <p className="muted small">{rt("reissue.address.compare")}</p>
            {opts.buyerName.trim() !== "" && <p>{t("reissue.address.name", { name: opts.buyerName })}</p>}
            {opts.buyerPhone.trim() !== "" && <p>{t("reissue.address.phone", { phone: opts.buyerPhone })}</p>}
          </div>
          <label htmlFor={`${ids}-email`}>{rt("reissue.address.label")}</label>
          <input
            id={`${ids}-email`}
            type="email"
            inputMode="email"
            autoComplete="off"
            spellCheck={false}
            value={typed}
            placeholder={t("reissue.address.hint")}
            onChange={(e) => setTyped(e.target.value)}
            aria-invalid={problem === "badEmail"}
            aria-describedby={problem === "badEmail" ? `${ids}-note` : undefined}
            data-testid="reissue-email"
          />
          {problem === "badEmail" && (
            <p id={`${ids}-note`} className="notice notice-bad" role="alert" data-testid="reissue-email-note">
              {t("reissue.address.bad")}
            </p>
          )}
          <div className="dialog-actions">
            <button type="submit" className="btn btn-primary" data-testid="reissue-continue">
              {t("reissue.continue")}
            </button>
            {withCase ? (
              <button type="button" className="btn" onClick={() => setStep("case")} data-testid="reissue-back">
                {t("reissue.back")}
              </button>
            ) : (
              close
            )}
          </div>
        </form>
      );
    }

    if (step === "reason") {
      const length = noteLength(note);
      return (
        <form onSubmit={onReason} noValidate data-testid="reissue-reason-form">
          <fieldset>
            <legend>{t("reissue.reason.legend")}</legend>
            {REISSUE_REASONS.map((r) => (
              <label key={r} className="check">
                <input
                  type="radio"
                  name={`${ids}-reason`}
                  value={r}
                  checked={reason === r}
                  onChange={() => setReason(r)}
                  data-testid={`reissue-reason-${r}`}
                />
                <span>{t(reasonKey(r))}</span>
              </label>
            ))}
          </fieldset>
          {problem === "reasonRequired" && (
            <p className="notice notice-bad" role="alert" data-testid="reissue-reason-note">
              {t("reissue.reason.required")}
            </p>
          )}
          <label htmlFor={`${ids}-note`}>{t("reissue.note.label")}</label>
          <textarea
            id={`${ids}-note`}
            rows={3}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            aria-describedby={`${ids}-note-hint`}
            data-testid="reissue-note"
          />
          <p id={`${ids}-note-hint`} className="muted small">
            {t("reissue.note.hint", { max: MAX_REISSUE_NOTE })}
          </p>
          {problem === "noteTooLong" && (
            <p className="notice notice-bad" role="alert" data-testid="reissue-note-long">
              {t("reissue.note.tooLong", { count: length, max: MAX_REISSUE_NOTE })}
            </p>
          )}
          <div className="dialog-actions">
            <button type="submit" className="btn btn-primary" data-testid="reissue-continue">
              {t("reissue.continue")}
            </button>
            <button type="button" className="btn" onClick={() => setStep("address")} data-testid="reissue-back">
              {t("reissue.back")}
            </button>
          </div>
        </form>
      );
    }

    // step === "check"
    const tickets = reissuableTickets(opts);
    const scanned = scannedTickets(opts);
    const provider = providerKeepsOldAddress(opts);
    return (
      <div data-testid="reissue-check">
        <h3>{t("reissue.check.title")}</h3>
        <p data-testid="reissue-check-tickets">{rt("reissue.check.tickets", { number, count: tickets.length })}</p>
        <ul className="plain-list small">
          {tickets.map((tk) => (
            <li key={tk.ticket_id}>
              №{tk.number}
              {tk.seat !== "" ? ` · ${tk.seat}` : ""}
              {tk.category !== "" ? ` · ${tk.category}` : ""}
            </li>
          ))}
        </ul>
        <p data-testid="reissue-check-to">{rt("reissue.check.to", { email })}</p>
        {same && (
          <p className="notice" data-testid="reissue-check-same">
            {rt("reissue.check.same")}
          </p>
        )}
        <p>{rt("reissue.check.unchanged")}</p>
        <p>{rt("reissue.check.address")}</p>
        {reason !== "" && <p className="muted">{t("reissue.check.reason", { reason: t(reasonKey(reason)) })}</p>}
        {scanned.map((tk) => (
          <p key={tk.ticket_id} className="notice notice-warn" data-testid="reissue-check-scanned">
            {t("reissue.check.scanned", { ticket: tk.number, time: formatDateTime(tk.scanned_at, tz, locale) })}
          </p>
        ))}
        {provider !== null && (
          <p className="notice notice-warn" data-testid="reissue-check-provider">
            {t("reissue.check.provider", { provider })}
          </p>
        )}
        <p>
          <strong>{t("reissue.check.final")}</strong>
        </p>
        <div className="dialog-actions">
          <button type="button" className="btn btn-primary" disabled={busy} onClick={() => setWordOpen(true)} data-testid="reissue-go">
            {rt("reissue.check.go")}
          </button>
          <button type="button" className="btn" disabled={busy} onClick={() => setStep("reason")} data-testid="reissue-back">
            {t("reissue.back")}
          </button>
        </div>
      </div>
    );
  };

  return (
    <>
      <div className="dialog-backdrop">
        <div
          ref={box}
          className="dialog resend-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby={`${ids}-title`}
          data-testid="reissue-dialog"
          data-step={step}
          onKeyDown={onKeyDown}
        >
          <h2 id={`${ids}-title`} ref={title} tabIndex={-1}>
            {rt("reissue.title", { number })}
          </h2>
          {renderBody()}
        </div>
      </div>
      {wordOpen && (
        <ConfirmWordDialog
          kind="reissue"
          subject={rt("reissue.word.subject", { number })}
          explain={kind === "invitation" ? t("reissue.inv.word.explain") : undefined}
          onConfirm={send}
          onCancel={() => setWordOpen(false)}
        />
      )}
    </>
  );
}
