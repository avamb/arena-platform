/**
 * B2-7: "Billing → Usage". What the platform charged the organization, month
 * by month, read-only and PRELIMINARY: no invoice is issued yet, and a
 * correction arrives as its own reversal line.
 *
 * Seen by who holds `billing.read` in THIS organization (the owner), decided
 * from `GET .../me` (`can`), never from the role: the manager gets an
 * explanation and no request is sent. Money is shown per currency, never added
 * across currencies. The month totals come from the API (the whole month); the
 * "By event" table is counted from the charge rows already loaded and says so
 * when more rows wait.
 */
import { useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchBillingEventSummary, listBillingCharges, listEvents } from "@/lib/api/endpoints";
import type { BillingChargeItem, BillingChargeTotal } from "@/lib/api/types";
import {
  PAGE_SIZE,
  formatMonth,
  groupByEvent,
  isReversal,
  kindKey,
  monthOf,
  monthRange,
  reasonKey,
  ruleKey,
  shiftMonth,
  type EventLine,
} from "@/lib/billing";
import { formatDateTime, formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { useOrg } from "@/screens/OrgLayout";

function Totals({ totals }: { totals: readonly BillingChargeTotal[] }) {
  const { t, locale } = useI18n();
  if (totals.length === 0) return <Empty>{t("billing.charges.empty")}</Empty>;
  return (
    <ul className="cards" data-testid="billing-totals">
      {totals.map((tot) => (
        <li key={tot.currency} className="card" data-testid={`billing-total-${tot.currency}`}>
          <span className="card-title">{formatMoney(tot.net_minor, tot.currency, locale)}</span>
          <span className="muted">
            {t("billing.totals.charged", { amount: formatMoney(tot.charged_minor, tot.currency, locale) })}
            {" · "}
            {t("billing.totals.reversed", { amount: formatMoney(tot.reversed_minor, tot.currency, locale) })}
            {" · "}
            {t("billing.totals.count", { count: tot.count })}
          </span>
        </li>
      ))}
    </ul>
  );
}

function EventTable({
  lines,
  names,
  onPick,
}: {
  lines: readonly EventLine[];
  names: ReadonlyMap<string, string>;
  onPick: (eventId: string) => void;
}) {
  const { t, locale } = useI18n();
  return (
    <ul className="cards" data-testid="billing-events">
      {lines.map((l) => (
        <li key={l.eventId ?? "none"} className="card" data-testid="billing-event-row">
          <span className="card-title">
            {l.eventId === null ? (
              t("billing.events.noEvent")
            ) : (
              <button type="button" className="btn btn-link" onClick={() => onPick(l.eventId as string)} data-testid={`billing-event-${l.eventId}`}>
                {names.get(l.eventId) ?? t("billing.events.unknown")}
              </button>
            )}
          </span>
          <span className="muted">
            {t("billing.totals.count", { count: l.charges })}
            {l.net.map((n) => ` · ${formatMoney(n.netMinor, n.currency, locale)}`).join("")}
          </span>
        </li>
      ))}
    </ul>
  );
}

function ChargeRow({ c, eventName }: { c: BillingChargeItem; eventName: string }) {
  const { t, locale } = useI18n();
  const reason = reasonKey(c.reason);
  return (
    <li className="card" data-testid="billing-row" data-reversal={isReversal(c) ? "true" : "false"}>
      <span className="card-title">
        {t(kindKey(c.kind))}
        {isReversal(c) && (
          <>
            {" "}
            <Badge tone="wait">{t("billing.reversal")}</Badge>
          </>
        )}
      </span>
      <span className="muted">
        {formatDateTime(c.occurred_at, "UTC", locale)} · {eventName}
      </span>
      <span className="card-meta">
        <strong data-testid="billing-row-amount">{formatMoney(c.amount_minor, c.currency, locale)}</strong>
        <span className="muted">
          {t(ruleKey(c.rule_type))}
          {reason !== null && ` · ${t(reason)}`}
        </span>
      </span>
    </li>
  );
}

function EventSummaryPanel({ orgId, eventId, name, onClear }: { orgId: string; eventId: string; name: string; onClear: () => void }) {
  const { t, locale } = useI18n();
  const summary = useQuery({
    queryKey: ["billingEventSummary", orgId, eventId],
    queryFn: () => fetchBillingEventSummary(orgId, eventId),
  });
  return (
    <section aria-labelledby="billing-summary-title" data-testid="billing-event-summary">
      <h2 id="billing-summary-title">{t("billing.event.title", { name })}</h2>
      <p>
        <button type="button" className="btn" onClick={onClear} data-testid="billing-event-clear">
          {t("billing.event.clear")}
        </button>
      </p>
      {summary.isLoading && <Loading />}
      {summary.error !== null && <ErrorBox error={summary.error} onRetry={() => void summary.refetch()} />}
      {summary.data !== undefined && (
        <>
          <p className="muted" data-testid="billing-event-alltime">
            {t("billing.event.allTime")}
          </p>
          <Totals totals={summary.data.totals} />
          {summary.data.by_rule.length > 0 && (
            <>
              <h3>{t("billing.event.byRule")}</h3>
              <ul className="cards" data-testid="billing-by-rule">
                {summary.data.by_rule.map((r) => (
                  <li key={`${r.currency}|${r.rule_type}|${r.reason ?? ""}`} className="card">
                    <span className="card-title">
                      {t(ruleKey(r.rule_type))}
                      {reasonKey(r.reason) !== null && ` · ${t(reasonKey(r.reason) as NonNullable<ReturnType<typeof reasonKey>>)}`}
                    </span>
                    <span className="muted">
                      {t("billing.totals.count", { count: r.count })} · {formatMoney(r.net_minor, r.currency, locale)}
                    </span>
                  </li>
                ))}
              </ul>
            </>
          )}
          {summary.data.by_session.length > 0 && (
            <>
              <h3>{t("billing.event.bySession")}</h3>
              <ul className="cards" data-testid="billing-by-session">
                {summary.data.by_session.map((s) => (
                  <li key={`${s.session_id ?? "none"}|${s.currency}`} className="card">
                    <span className="card-title">
                      {s.session_start_at !== null ? formatDateTime(s.session_start_at, "UTC", locale) : t("billing.event.noSession")}
                    </span>
                    <span className="muted">
                      {t("billing.totals.count", { count: s.count })} · {formatMoney(s.net_minor, s.currency, locale)}
                    </span>
                  </li>
                ))}
              </ul>
            </>
          )}
        </>
      )}
    </section>
  );
}

export function BillingScreen() {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const canRead = access.can("billing.read");
  const thisMonth = monthOf(new Date());
  const [month, setMonth] = useState(thisMonth);
  const [eventId, setEventId] = useState<string | null>(null);

  const range = monthRange(month);
  const charges = useInfiniteQuery({
    queryKey: ["billingCharges", orgId, month, eventId],
    enabled: canRead,
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listBillingCharges(orgId, {
        from: range.from,
        to: range.to,
        eventId: eventId ?? undefined,
        limit: PAGE_SIZE,
        cursor: pageParam === "" ? undefined : pageParam,
      }),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const events = useQuery({ queryKey: ["billingEventNames", orgId], enabled: canRead, queryFn: () => listEvents(orgId) });

  if (!canRead) {
    return (
      <article aria-labelledby="billing-title">
        <h1 id="billing-title">{t("billing.title")}</h1>
        <p className="notice notice-bad" role="alert" data-testid="billing-no-access">
          {t("billing.noAccess")}
        </p>
      </article>
    );
  }

  const names = new Map<string, string>((events.data?.events ?? []).map((e) => [e.id, e.name]));
  const rows = charges.data?.pages.flatMap((p) => p.items) ?? [];
  const totals = charges.data?.pages[0]?.totals ?? [];
  const lines = groupByEvent(rows);
  const nameOf = (id: string | null | undefined) => (id === null || id === undefined ? t("billing.events.noEvent") : (names.get(id) ?? t("billing.events.unknown")));

  return (
    <article aria-labelledby="billing-title">
      <h1 id="billing-title">{t("billing.title")}</h1>
      <p className="muted">{t("billing.intro")}</p>
      <p className="notice" role="status" data-testid="billing-preliminary">
        {t("billing.preliminary")}
      </p>

      <div className="tabs" role="group" aria-label={t("billing.month.label")}>
        <button type="button" className="btn" onClick={() => setMonth(shiftMonth(month, -1))} data-testid="billing-month-prev">
          {t("billing.month.prev")}
        </button>
        <strong data-testid="billing-month">{formatMonth(month, locale)}</strong>
        <button type="button" className="btn" disabled={month >= thisMonth} onClick={() => setMonth(shiftMonth(month, 1))} data-testid="billing-month-next">
          {t("billing.month.next")}
        </button>
      </div>

      {charges.isLoading && <Loading />}
      {charges.error !== null && <ErrorBox error={charges.error} onRetry={() => void charges.refetch()} />}

      {charges.data !== undefined && (
        <>
          <h2>{t("billing.totals.title")}</h2>
          <Totals totals={totals} />

          {eventId !== null && <EventSummaryPanel orgId={orgId} eventId={eventId} name={nameOf(eventId)} onClear={() => setEventId(null)} />}

          {eventId === null && rows.length > 0 && (
            <section aria-labelledby="billing-events-title">
              <h2 id="billing-events-title">{t("billing.events.title")}</h2>
              <EventTable lines={lines} names={names} onPick={setEventId} />
              {charges.hasNextPage && (
                <p className="muted" data-testid="billing-partial">
                  {t("billing.events.partial", { shown: rows.length })}
                </p>
              )}
            </section>
          )}

          <section aria-labelledby="billing-charges-title">
            <h2 id="billing-charges-title">{t("billing.charges.title")}</h2>
            {rows.length === 0 ? (
              <Empty>{t("billing.charges.empty")}</Empty>
            ) : (
              <ul className="cards" data-testid="billing-rows">
                {rows.map((c) => (
                  <ChargeRow key={c.id} c={c} eventName={nameOf(c.event_id)} />
                ))}
              </ul>
            )}
            {charges.hasNextPage && (
              <p>
                <button type="button" className="btn" disabled={charges.isFetchingNextPage} onClick={() => void charges.fetchNextPage()} data-testid="billing-more">
                  {t("billing.charges.more")}
                </button>
              </p>
            )}
          </section>
        </>
      )}
    </article>
  );
}
