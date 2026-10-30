/**
 * The actions of a shop order card (spec 47, section 6):
 *
 * - "Mark shipped" with an optional tracking note. A shipped order only
 *   offers to change the note.
 * - "Cancel the order" for an order that still waits for payment. The
 *   typed word of an order (CANCEL ORDER) and the password again (`withStepUp`).
 * - "Refund": an amount (empty = everything that is left) and a reason, then
 *   the typed word REFUND and the password again. The answer says whether the
 *   money went back, is on its way, or was refused. One refund operation has
 *   one Idempotency-Key (SA-01), kept while the amount and the reason stay the
 *   same and the answer is lost, so a retry never returns the money twice.
 *   After a failure the order is read again and what is left is shown, taking
 *   the server's `refundable_minor` over the card's own figure (SA-19).
 * - "Fulfil the order" / "Refund and cancel" for an order in "needs a check" (`manual_review`): the operator's
 *   decision, each with the typed word, the password again and an Idempotency-Key kept in the tab like a refund's.
 *   The route belongs to the backend lane (`POST .../shop/orders/{id}/resolve`).
 * - Focus: a closed dialog gives the focus back to its opener, and when the opener is gone (the panel closed after
 *   a success) the focus goes to the result line, so a keyboard user is never dropped on the page top.
 * - "Copy for the courier": the text the SERVER builds (the same one the
 *   logistician gets in Telegram, SA-35) to the clipboard. When the browser
 *   refuses, the text is shown to be copied by hand. Only when the server
 *   cannot be reached the card builds the text itself.
 * - "Send the e-mail again" (A-11): queues the buyer's confirmation or, for a
 *   shipped order, the shipping letter once more, for a refunded one, the
 *   refund letter. Asks which one only when several exist. One request per order per minute: the API answers 429 and the
 *   card says to wait.
 *
 * Every write needs `shop.manage`; the courier text needs only `shop.read`.
 */
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextAreaField, TextField } from "@/components/FormField";
import { useStepUp } from "@/components/StepUpDialog";
import { cancelShopOrder, fetchCourierText, fetchShopOrder, refundShopOrder, resendShopOrderEmail, resolveShopOrder, shipShopOrder } from "@/lib/api/shopApi";
import { ApiError } from "@/lib/api/http";
import type { ShopOrder, ShopOrderResolveAction, ShopRefund } from "@/lib/api/types";
import { formatMoney } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import { forgetRefundKey, refundKeyFor } from "@/lib/shopRefundKeys";
import { StepUpCancelled, withStepUp } from "@/lib/stepUp";
import {
  SHOP_LIMITS,
  canCancelShopOrder,
  canResolveShopOrder,
  canEditTracking,
  canRefundShopOrder,
  canShip,
  checkRefund,
  copyText,
  courierText,
  isShipped,
  isShopOrderStatus,
  itemLabel,
  refundBody,
  refundOutcome,
  refundableFromError,
  refundableNow,
  resendLetters,
  shopOrderNumber,
  shortItems,
  type OrderLetter,
} from "@/lib/shop";

/** A failure after which the request may still have been recorded (no answer, a timeout, a server error). */
function mayHaveBeenRecorded(err: unknown): boolean {
  return !(err instanceof ApiError) || err.status === 0 || err.status === 408 || err.status >= 500;
}

export interface ShopOrderActionsProps {
  orgId: string;
  order: ShopOrder;
  canManage: boolean;
  /** refund.create and refund.approve: the refund engine needs both next to shop.manage. */
  canRefund: boolean;
  /** The shop's country: an address in it needs no country line in the courier text. */
  country?: string | null;
}

type Panel = "ship" | "refund" | "resend" | null;

export function ShopOrderActions({ orgId, order, canManage, canRefund, country }: ShopOrderActionsProps) {
  const { t, locale } = useI18n();
  const stepUp = useStepUp();
  const queryClient = useQueryClient();
  const [panel, setPanel] = useState<Panel>(null);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [wordOpen, setWordOpen] = useState(false);
  const [tracking, setTracking] = useState(order.tracking_note);
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [refundProblem, setRefundProblem] = useState<MessageKey | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<unknown>(null);
  const [note, setNote] = useState<{ key: MessageKey; params?: Record<string, string>; bad?: boolean } | null>(null);
  const [courier, setCourier] = useState<{ copied: boolean; text: string } | null>(null);
  // What the server said is left to refund (it counts refunds still on their way; the card cannot see them).
  const [serverLeft, setServerLeft] = useState<number | null>(null);
  const [refundFailure, setRefundFailure] = useState<{ error: unknown; retrySafe: boolean } | null>(null);
  const refundBusy = useRef(false);
  // The operator's decision on an order that waits for a check: which one is being confirmed.
  const [resolveOpen, setResolveOpen] = useState<ShopOrderResolveAction | null>(null);
  const resolveBusy = useRef(false);
  // After a success the opener of a dialog is gone: the result line takes the focus (see the header).
  const noteRef = useRef<HTMLParagraphElement>(null);
  const focusNote = useRef(false);
  useEffect(() => {
    if (focusNote.current && note !== null) {
      focusNote.current = false;
      noteRef.current?.focus();
    }
  }, [note]);

  const money = (v: number) => formatMoney(v, order.currency, locale);
  const left = refundableNow(order, serverLeft);

  const store = (o: ShopOrder) => {
    queryClient.setQueryData(["shopOrder", orgId, o.id], o);
    void queryClient.invalidateQueries({ queryKey: ["shopOrders", orgId] });
    void queryClient.invalidateQueries({ queryKey: ["shopOrderCounts", orgId] });
  };

  const letters = resendLetters(order, canManage);
  const sendLetter = async (kind: OrderLetter) => {
    setBusy(true);
    setFailure(null);
    setNote(null);
    try {
      await resendShopOrderEmail(orgId, order.id, kind);
      setPanel(null);
      setNote({ key: "shop.act.resend.done" });
    } catch (error) {
      // One request per order per minute: say when to come back instead of a generic "too many requests".
      if (error instanceof ApiError && error.status === 429) {
        setPanel(null);
        setNote({ key: "shop.act.resend.wait", bad: true });
      } else if (kind === "refunded" && error instanceof ApiError && error.status === 409) {
        // The refund letter needs a succeeded refund: say so calmly instead of showing a raw error.
        setPanel(null);
        setNote({ key: "shop.act.resend.refundedUnavailable", bad: true });
      } else setFailure(error);
    } finally {
      setBusy(false);
    }
  };

  const onShip = async (e: FormEvent) => {
    e.preventDefault();
    if ([...tracking.trim()].length > SHOP_LIMITS.trackingNote) {
      setNote({ key: "shop.act.ship.tooLong", params: { max: String(SHOP_LIMITS.trackingNote) }, bad: true });
      return;
    }
    setBusy(true);
    setFailure(null);
    setNote(null);
    try {
      store(await shipShopOrder(orgId, order.id, tracking));
      setPanel(null);
      setNote({ key: "shop.act.ship.done" });
    } catch (error) {
      setFailure(error);
      // The order may have moved on (a late payment, a refund): read it again so the card shows its real state.
      if (error instanceof ApiError && error.status === 409) {
        try {
          store(await fetchShopOrder(orgId, order.id));
        } catch {
          /* the failure above is what the person sees */
        }
      }
    } finally {
      setBusy(false);
    }
  };

  const onCancel = async () => {
    try {
      const done = await withStepUp(
        orgId,
        (headers) => cancelShopOrder(orgId, order.id, headers),
        // An unpaid order moves no money: the password question says what really happens (C-06).
        (org) => stepUp.ask(org, "stepUp.introNoMoney"),
      );
      store(done);
    } catch (err) {
      // The person closed the password question: nothing was sent, the dialog stays.
      if (err instanceof StepUpCancelled) return;
      if (err instanceof ApiError && err.status === 409) {
        // The status moved while the card was open (the buyer paid, or the hold ended): show what it is now.
        setCancelOpen(false);
        const fresh = await fetchShopOrder(orgId, order.id);
        store(fresh);
        const label = isShopOrderStatus(fresh.status) ? t(`shop.order.status.${fresh.status}` as MessageKey) : fresh.status;
        setNote({ key: "shop.act.cancel.changed.next", params: { status: label }, bad: true });
        return;
      }
      throw err; // the dialog shows what went wrong and what to do
    }
    setCancelOpen(false);
    focusNote.current = true;
    setNote({ key: "shop.act.cancel.done" });
  };

  const onResolve = async () => {
    const action = resolveOpen;
    if (action === null || resolveBusy.current) return;
    // One decision, one key: kept in the tab while the answer is unknown, dropped once it is final.
    const held = `${order.id}:resolve:${action}`;
    const key = refundKeyFor(held, action);
    resolveBusy.current = true;
    let done: ShopOrder;
    try {
      done = await withStepUp(orgId, (headers) => resolveShopOrder(orgId, order.id, action, headers, key), stepUp.ask);
    } catch (err) {
      if (err instanceof StepUpCancelled) return;
      if (!mayHaveBeenRecorded(err)) forgetRefundKey(held);
      if (err instanceof ApiError && err.status === 409) {
        // The order moved on meanwhile (a colleague decided, the payment came): show what it is now.
        try {
          store(await fetchShopOrder(orgId, order.id));
        } catch {
          /* the failure below is what the person sees */
        }
      }
      throw err; // the dialog shows what went wrong and what to do
    } finally {
      resolveBusy.current = false;
    }
    forgetRefundKey(held);
    store(done);
    setResolveOpen(null);
    focusNote.current = true;
    setNote({ key: action === "fulfil" ? "shop.act.resolve.fulfil.done" : "shop.act.resolve.refundCancel.done" });
  };

  const onRefundContinue = (e: FormEvent) => {
    e.preventDefault();
    const c = checkRefund(order, amount, reason, serverLeft);
    if (!c.ok) {
      setRefundProblem(c.problem === "amount" ? "shop.act.refund.err.amount" : c.problem === "tooMuch" ? "shop.act.refund.err.tooMuch" : "shop.act.refund.err.reason");
      return;
    }
    setRefundProblem(null);
    setRefundFailure(null);
    setWordOpen(true);
  };

  const refundCheck = checkRefund(order, amount, reason, serverLeft);
  const refundAmount = refundCheck.ok ? (refundCheck.amountMinor ?? left) : left;

  const onRefund = async () => {
    if (!refundCheck.ok || refundBusy.current) return;
    const body = refundBody(refundCheck, reason);
    // One operation, one key: the same amount and reason keep the key a lost answer left behind.
    const fingerprint = JSON.stringify(body);
    // The key outlives the dialog AND the page (sessionStorage, SB-12): a lost answer followed by a reload must
    // retry with the same key, or the server would treat the retry as a second refund.
    const key = refundKeyFor(order.id, fingerprint);
    let answer;
    refundBusy.current = true;
    try {
      answer = await withStepUp(orgId, (headers) => refundShopOrder(orgId, order.id, body, headers, key), stepUp.ask);
    } catch (err) {
      if (err instanceof StepUpCancelled) return;
      const unsure = mayHaveBeenRecorded(err);
      // A refusal means nothing was recorded: the next try is a new operation. An unsure failure keeps the key.
      if (!unsure) forgetRefundKey(order.id);
      const serverSays = refundableFromError(err);
      if (serverSays !== null) setServerLeft(serverSays);
      setRefundFailure({ error: err, retrySafe: unsure });
      setWordOpen(false);
      // Read the order again: the card must show what is really left, not the figure it had before.
      try {
        store(await fetchShopOrder(orgId, order.id));
      } catch {
        /* the refund failure above is what the person sees */
      }
      return;
    } finally {
      refundBusy.current = false;
    }
    forgetRefundKey(order.id);
    setServerLeft(null);
    setRefundFailure(null);
    store(answer.order);
    const r: ShopRefund = answer.refund;
    const outcome = refundOutcome(r.state);
    setWordOpen(false);
    setPanel(null);
    setAmount("");
    setReason("");
    focusNote.current = true;
    if (outcome === "done") setNote({ key: "shop.act.refund.done", params: { amount: formatMoney(r.amount, r.currency, locale) } });
    else if (outcome === "pending") setNote({ key: "shop.act.refund.pending" });
    else if (outcome === "review") setNote({ key: "shop.act.refund.review", bad: true });
    else setNote({ key: "shop.act.refund.failed", params: { reason: r.failure_reason ?? "" }, bad: true });
  };

  const onCourier = async () => {
    setFailure(null);
    setNote(null);
    try {
      // The server builds the text (SA-35): it is the one the logistician gets in Telegram. The card builds its own
      // only when the server cannot be reached at all.
      let text: string;
      try {
        text = await fetchCourierText(orgId, order.id);
      } catch (error) {
        if (!(error instanceof ApiError) || (error.status !== 0 && error.status < 500)) throw error;
        text = courierText(order, {
          locale,
          shopCountry: country,
          labels: { order: t("shop.courier.order", { number: shopOrderNumber(order.number) }), comment: (note) => t("shop.courier.comment", { note }) },
        });
      }
      const copied = await copyText(text);
      setCourier({ copied, text });
      setNote(copied ? { key: "shop.act.courier.done" } : { key: "shop.act.courier.manual", bad: true });
    } catch (error) {
      setFailure(error);
    }
  };

  const shipLabel = canEditTracking(order.status, canManage, order.shipped_at) ? "shop.act.editTracking" : "shop.act.ship";
  // M-03: no delivery (a file, a service): nothing to ship and no parcel for a courier.
  const noDelivery = order.delivery_kind === "none";
  const showShip = canShip(order.status, canManage, order.delivery_kind, order.shipped_at) || canEditTracking(order.status, canManage, order.shipped_at);

  return (
    <section aria-labelledby="shop-order-actions" data-testid="shop-order-actions">
      <h2 id="shop-order-actions" className="visually-hidden">
        {t("shop.act.ship")}
      </h2>
      <div className="actions">
        {showShip && (
          <button type="button" className="btn btn-primary" onClick={() => setPanel(panel === "ship" ? null : "ship")} data-testid="shop-order-ship">
            {t(shipLabel)}
          </button>
        )}
        {canRefundShopOrder(order, canManage && canRefund) && (
          <button type="button" className="btn" onClick={() => {
              setServerLeft(null);
              setRefundFailure(null);
              setPanel(panel === "refund" ? null : "refund");
            }}
            data-testid="shop-order-refund"
          >
            {t("shop.act.refund")}
          </button>
        )}
        {canCancelShopOrder(order.status, canManage) && (
          <button type="button" className="btn" onClick={() => setCancelOpen(true)} data-testid="shop-order-cancel">
            {t("shop.act.cancel")}
          </button>
        )}
        {canResolveShopOrder(order.status, canManage && canRefund) && (
          <>
            <button type="button" className="btn btn-primary" onClick={() => setResolveOpen("fulfil")} data-testid="shop-order-resolve-fulfil">
              {t("shop.act.resolve.fulfil")}
            </button>
            <button type="button" className="btn" onClick={() => setResolveOpen("refund_and_cancel")} data-testid="shop-order-resolve-refund">
              {t("shop.act.resolve.refundCancel")}
            </button>
          </>
        )}
        {!noDelivery && (
          <button type="button" className="btn" onClick={() => void onCourier()} data-testid="shop-order-courier">
            {t("shop.act.courier")}
          </button>
        )}
        {letters.length > 0 && (
          <button
            type="button"
            className="btn"
            disabled={busy}
            onClick={() => (letters.length === 1 ? void sendLetter(letters[0]) : setPanel(panel === "resend" ? null : "resend"))}
            data-testid="shop-order-resend"
          >
            {t("shop.act.resend")}
          </button>
        )}
      </div>

      {noDelivery && (
        <p className="muted" data-testid="shop-order-no-delivery">
          {t("shop.act.noDelivery")}
        </p>
      )}

      {panel === "resend" && letters.length > 1 && (
        <div role="group" aria-labelledby="shop-resend-title" data-testid="shop-resend-panel">
          <h3 id="shop-resend-title">{t("shop.act.resend.title")}</h3>
          <p className="field-hint">{t("shop.act.resend.hint", { email: order.buyer.email })}</p>
          <div className="actions">
            {letters.map((k) => (
              <button key={k} type="button" className="btn btn-primary" disabled={busy} onClick={() => void sendLetter(k)} data-testid={`shop-resend-${k}`}>
                {t(`shop.act.resend.${k}` as MessageKey)}
              </button>
            ))}
            <button type="button" className="btn" onClick={() => setPanel(null)} data-testid="shop-resend-close">
              {t("confirmWord.cancel")}
            </button>
          </div>
        </div>
      )}

      {panel === "ship" && (
        <form onSubmit={(e) => void onShip(e)} noValidate data-testid="shop-ship-form">
          <h3>{t("shop.act.ship.title")}</h3>
          <TextAreaField
            label={t("shop.act.ship.field")}
            hint={t("shop.act.ship.hint")}
            value={tracking}
            maxLength={SHOP_LIMITS.trackingNote}
            rows={3}
            onChange={setTracking}
            testId="shop-ship-note"
          />
          <p>
            <button type="submit" className="btn btn-primary" disabled={busy} data-testid="shop-ship-submit">
              {isShipped(order) ? t("shop.act.ship.save") : t("shop.act.ship.confirm")}
            </button>
          </p>
        </form>
      )}

      {panel === "refund" && (
        <form onSubmit={onRefundContinue} noValidate data-testid="shop-refund-form">
          <h3>{t("shop.act.refund.title")}</h3>
          <TextField
            label={t("shop.act.refund.amount", { currency: order.currency })}
            hint={t("shop.act.refund.amountHint", { left: money(left) })}
            error={
              refundProblem === "shop.act.refund.err.amount" || refundProblem === "shop.act.refund.err.tooMuch"
                ? t(refundProblem, { left: money(left) })
                : undefined
            }
            value={amount}
            onChange={setAmount}
            testId="shop-refund-amount"
          />
          <TextAreaField
            label={t("shop.act.refund.reason")}
            error={refundProblem === "shop.act.refund.err.reason" ? t(refundProblem, { max: SHOP_LIMITS.refundReason }) : undefined}
            value={reason}
            maxLength={SHOP_LIMITS.refundReason}
            rows={2}
            onChange={setReason}
            testId="shop-refund-reason"
          />
          <p>
            <button type="submit" className="btn btn-primary" data-testid="shop-refund-continue">
              {t("shop.act.refund.next")}
            </button>
          </p>
          {refundFailure !== null && (
            <div data-testid="shop-refund-failure">
              <ErrorNotice error={refundFailure.error} testId="shop-refund-error" />
              <p className="notice notice-warn" role="status" data-testid="shop-refund-left-now">
                {t("shop.act.refund.leftNow", { left: money(left) })}
              </p>
              {refundFailure.retrySafe && (
                <p className="muted" data-testid="shop-refund-retry-safe">
                  {t("shop.act.refund.retrySafe")}
                </p>
              )}
            </div>
          )}
        </form>
      )}

      {note !== null && (
        <p ref={noteRef} tabIndex={-1} className={note.bad === true ? "notice notice-bad" : "notice notice-ok"} role={note.bad === true ? "alert" : "status"} data-testid="shop-order-note">
          {t(note.key, note.params)}
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure} testId="shop-order-error" />}
      {failure !== null && shortItems(failure).length > 0 && (
        <div data-testid="shop-order-short" role="status">
          <p>{t("shop.act.ship.shortIntro")}</p>
          <ul className="facts">
            {shortItems(failure).map((it) => {
              const line = order.items.find((x) => x.variant_id === it.variant_id);
              return (
                <li key={it.variant_id}>
                  {t("shop.act.ship.shortLine", { name: line === undefined ? it.variant_id : itemLabel(line), ordered: it.ordered, available: it.available })}
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {courier !== null && !courier.copied && (
        <pre className="mono" tabIndex={0} data-testid="shop-courier-text">
          {courier.text}
        </pre>
      )}

      {cancelOpen && (
        <ConfirmWordDialog
          kind="cancel_order"
          title={t("shop.act.cancel.title")}
          explain={t("shop.act.cancel.explain")}
          subject={t("shop.order.title", { number: shopOrderNumber(order.number) })}
          onConfirm={onCancel}
          onCancel={() => setCancelOpen(false)}
        />
      )}
      {resolveOpen !== null && (
        <ConfirmWordDialog
          kind={resolveOpen === "fulfil" ? "shop_fulfil" : "refund"}
          title={resolveOpen === "fulfil" ? t("shop.act.resolve.fulfil.title") : t("shop.act.resolve.refund.title")}
          explain={resolveOpen === "fulfil" ? t("shop.act.resolve.fulfil.explain") : t("shop.act.resolve.refund.explain", { amount: money(left) })}
          subject={t("shop.order.title", { number: shopOrderNumber(order.number) })}
          onConfirm={onResolve}
          onCancel={() => setResolveOpen(null)}
        />
      )}
      {wordOpen && (
        <ConfirmWordDialog
          kind="refund"
          title={t("shop.act.refund.title")}
          explain={t("shop.act.refund.explain", { amount: money(refundAmount) })}
          subject={t("shop.order.title", { number: shopOrderNumber(order.number) })}
          onConfirm={onRefund}
          onCancel={() => setWordOpen(false)}
        />
      )}
    </section>
  );
}
