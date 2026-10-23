/**
 * B3-3/B4: the credit packs on offer and the order flow of the owner.
 *
 * Money is shown per pack in the pack's own currency. Ordering follows the
 * state machine of src/lib/billingPacks.ts: ONE Idempotency-Key for one
 * confirmed order, kept while the answer is missing (a lost answer is
 * "Check the order", never a second order), a new key for a different pack or
 * method. A bank transfer shows the issued invoice with its payment
 * reference; a card order opens the Stripe page in the same tab.
 *
 * Who may order is decided by the caller (`canPurchase`, billing.purchase).
 */
import { useEffect, useId, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ErrorNotice } from "@/components/ErrorNotice";
import { PaymentReference } from "@/components/PaymentReference";
import { viewerZone } from "@/components/BillingInvoiceCard";
import { orderBillingPack } from "@/lib/api/endpoints";
import type { BillingAccountInvoice, BillingPackOffer } from "@/lib/api/types";
import { packsByCurrency } from "@/lib/billingAccount";
import {
  IDLE_FLOW,
  choose,
  clearStoredOrder,
  close,
  finishErr,
  finishOk,
  flowFromStored,
  isSafePaymentUrl,
  loadStoredOrder,
  orderFingerprint,
  payableMinor,
  saveStoredOrder,
  startSend,
  type OrderFlow,
  type PayMethod,
} from "@/lib/billingPacks";
import { formatDate, formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";

export interface BillingPacksProps {
  orgId: string;
  packs: readonly BillingPackOffer[];
  canPurchase: boolean;
}

export function BillingPacks({ orgId, packs, canPurchase }: BillingPacksProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const ids = useId();

  // A key whose answer was lost before a reload is found again, so the person
  // checks that order instead of ordering twice.
  const [flow, setFlowState] = useState<OrderFlow>(() => {
    const stored = loadStoredOrder(orgId);
    return stored === null ? IDLE_FLOW : flowFromStored(stored);
  });
  const [resumed] = useState(() => loadStoredOrder(orgId) !== null);
  const flowRef = useRef(flow);
  const setFlow = (f: OrderFlow) => {
    flowRef.current = f;
    setFlowState(f);
  };
  const panel = useRef<HTMLElement>(null);

  // Brings the order panel into view and to the keyboard when a pack is chosen.
  const phase = flow.phase;
  useEffect(() => {
    if (phase === "confirm") panel.current?.focus();
  }, [phase]);

  const money = (minor: number, currency: string) => formatMoney(minor, currency, locale);
  const pack = packs.find((p) => flow.intent !== null && p.id === flow.intent.packId);

  const pick = (p: BillingPackOffer) => {
    const method: PayMethod = flowRef.current.intent?.packId === p.id ? flowRef.current.intent.method : "bank_transfer";
    const next = choose(flowRef.current, { packId: p.id, method });
    if (next.held === null) clearStoredOrder(orgId);
    setFlow(next);
  };

  const setMethod = (method: PayMethod) => {
    const f = flowRef.current;
    if (f.intent === null || f.phase === "sending") return;
    setFlow(choose(f, { packId: f.intent.packId, method }));
  };

  const submit = async () => {
    const started = startSend(flowRef.current);
    if (started === null) return; // a double click: one request
    const intent = flowRef.current.intent;
    if (intent === null) return;
    setFlow(started.flow);
    saveStoredOrder(orgId, { intent, key: started.key, fingerprint: orderFingerprint(intent) });
    try {
      const invoice = await orderBillingPack(orgId, intent.packId, intent.method, started.key);
      clearStoredOrder(orgId);
      setFlow(finishOk(flowRef.current, invoice));
      void queryClient.invalidateQueries({ queryKey: ["billingAccountInvoices", orgId] });
      if (intent.method === "stripe_card" && isSafePaymentUrl(invoice.payment_url)) {
        window.location.assign(invoice.payment_url);
      }
    } catch (error) {
      const next = finishErr(flowRef.current, error);
      if (next.phase === "failed") clearStoredOrder(orgId);
      setFlow(next);
    }
  };

  const groups = packsByCurrency(packs);
  return (
    <section aria-labelledby={`${ids}-title`} data-testid="billing-packs">
      <h2 id={`${ids}-title`}>{t("billing.packs.title")}</h2>
      <p className="muted">{t("billing.packs.intro")}</p>
      {!canPurchase && packs.length > 0 && (
        <p className="notice" role="status" data-testid="billing-packs-owner-only">
          {t("billing.pack.ownerOnly")}
        </p>
      )}
      {groups.length === 0 && (
        <p className="muted" data-testid="billing-packs-none">
          {t("billing.packs.none")}
        </p>
      )}
      {groups.map((g) => (
        <div key={g.currency} data-testid={`billing-packs-${g.currency}`}>
          <h3>{t("billing.packs.currency", { currency: g.currency })}</h3>
          <ul className="cards">
            {g.packs.map((p) => (
              <li key={p.id} className="card" data-testid="billing-pack" data-pack-tier={p.tier}>
                <span className="card-title" data-testid="billing-pack-face">
                  {money(p.face_minor, p.currency)}
                </span>
                <span className="muted">
                  {t("billing.pack.tickets", { count: formatNumber(p.tickets_unseated, locale), rate: money(p.unseated_rate_minor, p.currency) })}
                </span>
                <span className="muted">{t("billing.pack.seated", { amount: money(p.seated_surcharge_minor, p.currency) })}</span>
                <span className="muted" data-testid="billing-pack-card">
                  {p.card_payment_available && p.card_fee_minor !== null && p.card_fee_minor !== undefined
                    ? t("billing.pack.cardFee", { fee: money(p.card_fee_minor, p.currency) })
                    : t("billing.pack.cardUnavailable")}
                </span>
                {canPurchase && (
                  <span className="card-meta">
                    <button
                      type="button"
                      className="btn btn-primary"
                      disabled={flow.phase === "sending"}
                      onClick={() => pick(p)}
                      data-testid="billing-pack-buy"
                    >
                      {t("billing.pack.buy")}
                    </button>
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      ))}

      {canPurchase && flow.phase !== "idle" && flow.phase !== "done" && flow.intent !== null && (
        <section
          ref={panel}
          tabIndex={-1}
          className="card billing-order"
          aria-labelledby={`${ids}-order`}
          data-testid="billing-order"
          data-phase={flow.phase}
        >
          <h3 id={`${ids}-order`}>{t("billing.order.title")}</h3>
          {resumed && flow.phase === "unknown" && flow.invoice === null && flow.error === null && (
            <p className="notice" role="status" data-testid="billing-order-resumed">
              {t("billing.order.pendingFound")}
            </p>
          )}
          {pack !== undefined && (
            <p>
              <strong>{t("billing.order.summary", { amount: money(pack.face_minor, pack.currency) })}</strong>
            </p>
          )}
          {pack !== undefined && (
            <fieldset disabled={flow.phase === "sending" || flow.phase === "unknown"}>
              <legend>{t("billing.order.method")}</legend>
              <label className="check">
                <input
                  type="radio"
                  name={`${ids}-method`}
                  checked={flow.intent.method === "bank_transfer"}
                  onChange={() => setMethod("bank_transfer")}
                  data-testid="billing-method-bank"
                />
                <span>
                  {t("billing.order.bank")}
                  <span className="field-hint"> {t("billing.order.bankHint")}</span>
                </span>
              </label>
              <label className="check">
                <input
                  type="radio"
                  name={`${ids}-method`}
                  checked={flow.intent.method === "stripe_card"}
                  disabled={!pack.card_payment_available}
                  onChange={() => setMethod("stripe_card")}
                  data-testid="billing-method-card"
                />
                <span>
                  {t("billing.order.card")}
                  <span className="field-hint" data-testid="billing-method-card-hint">
                    {" "}
                    {pack.card_payment_available && pack.card_fee_minor !== null && pack.card_fee_minor !== undefined
                      ? t("billing.order.cardHint", { fee: money(pack.card_fee_minor, pack.currency) })
                      : t("billing.order.cardOff")}
                  </span>
                </span>
              </label>
            </fieldset>
          )}
          {pack !== undefined && (
            <p data-testid="billing-order-total">
              <strong>
                {t("billing.order.total", {
                  amount: money(payableMinor(pack.face_minor, pack.card_fee_minor ?? null, flow.intent.method), pack.currency),
                })}
              </strong>
            </p>
          )}
          <p className="muted">{t("billing.order.creditsAfter")}</p>

          {flow.phase === "unknown" && (
            <div className="notice notice-bad" role="alert" data-testid="billing-order-unknown">
              <p>
                <strong>{t("billing.order.unknownTitle")}</strong>
              </p>
              <p>{t("billing.order.unknownText")}</p>
            </div>
          )}
          {flow.phase === "failed" && flow.error !== null && <ErrorNotice error={flow.error} testId="billing-order-error" />}

          <p className="card-meta">
            <button
              type="button"
              className="btn btn-primary"
              disabled={flow.phase === "sending"}
              onClick={() => void submit()}
              data-testid="billing-order-submit"
            >
              {flow.phase === "sending" ? t("billing.order.sending") : flow.phase === "unknown" ? t("billing.order.check") : t("billing.order.submit")}
            </button>
            <button
              type="button"
              className="btn"
              disabled={flow.phase === "sending"}
              onClick={() => setFlow(close(flowRef.current))}
              data-testid="billing-order-close"
            >
              {t("billing.order.close")}
            </button>
          </p>
        </section>
      )}

      {flow.phase === "done" && flow.invoice !== null && (
        <IssuedInvoice orgId={orgId} invoice={flow.invoice} onAnother={() => setFlow(IDLE_FLOW)} />
      )}
    </section>
  );
}

/** The invoice the order produced: what to pay and how, or the card page. */
function IssuedInvoice({ orgId, invoice, onAnother }: { orgId: string; invoice: BillingAccountInvoice; onAnother: () => void }) {
  const { t, locale } = useI18n();
  const isCard = invoice.payment_method === "stripe_card";
  const number = invoice.number ?? "";
  const zone = viewerZone();
  return (
    <section className="card billing-issued" aria-labelledby="billing-issued-title" data-testid="billing-issued" data-invoice-id={invoice.id}>
      <h3 id="billing-issued-title">{t("billing.result.title", { number })}</h3>
      <p data-testid="billing-issued-amount">
        <strong>{t("billing.result.amount", { amount: formatMoney(invoice.total_minor, invoice.currency, locale) })}</strong>
      </p>
      {!isCard && (
        <>
          <h4>{t("billing.result.bankTitle")}</h4>
          {invoice.payment_reference !== null && invoice.payment_reference !== undefined && invoice.payment_reference !== "" && (
            <PaymentReference value={invoice.payment_reference} />
          )}
          <p data-testid="billing-issued-instruction">{t("billing.result.instruction")}</p>
          {invoice.due_at !== null && invoice.due_at !== undefined && (
            <p className="muted">{t("billing.result.due", { date: formatDate(invoice.due_at, zone, locale) })}</p>
          )}
        </>
      )}
      {isCard && (
        <>
          <h4>{t("billing.result.cardTitle")}</h4>
          {isSafePaymentUrl(invoice.payment_url) ? (
            <>
              <p className="muted" role="status">
                {t("billing.result.cardOpening")}
              </p>
              <p>
                <a className="btn btn-primary" href={invoice.payment_url} data-testid="billing-issued-card-link">
                  {t("billing.result.cardOpen")}
                </a>
              </p>
            </>
          ) : (
            <p className="notice notice-bad" role="alert" data-testid="billing-issued-card-error">
              {t("billing.result.cardError")}
            </p>
          )}
        </>
      )}
      <p className="muted">{t("billing.order.creditsAfter")}</p>
      <p className="card-meta">
        <Link to="/o/$orgId/billing/invoices" params={{ orgId }} className="btn" data-testid="billing-issued-invoices">
          {t("billing.result.toInvoices")}
        </Link>
        <button type="button" className="btn" onClick={onAnother} data-testid="billing-issued-another">
          {t("billing.result.another")}
        </button>
      </p>
    </section>
  );
}
