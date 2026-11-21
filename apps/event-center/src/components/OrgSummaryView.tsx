/**
 * The cards of the organization summary (EC-V1, spec 62 §12.2): one card per
 * session and currency with sold, invitations, refunds, active tickets, revenue,
 * refunded and net; under it an expandable "prices and categories" table with
 * the promo codes used. Pure view over the API answer, so a test mounts it
 * without a server.
 */
import { Link } from "@tanstack/react-router";
import type { OrgSummary, OrgSummaryCard, OrgSummarySession } from "@/lib/api/types";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { groupBreakdown, type PriceGroup } from "@/lib/orgSummary";

type Figured = Omit<OrgSummaryCard, "breakdown" | "promos">;

function Figures({ card }: { card: Figured }) {
  const { t, locale } = useI18n();
  const n = (v: number) => formatNumber(v, locale);
  const m = (v: number) => formatMoney(v, card.currency, locale);
  return (
    <ul className="figures" data-testid="summary-figures">
      <li>{t("summary.card.revenue", { amount: m(card.revenue) })}</li>
      {(card.refunded > 0 || card.refunded_tickets > 0) && <li>{t("summary.card.refunded", { amount: m(card.refunded) })}</li>}
      {card.cashback > 0 && <li>{t("summary.card.cashback", { amount: m(card.cashback) })}</li>}
      <li>
        <strong>{t("summary.card.net", { amount: m(card.net) })}</strong>
      </li>
      <li>{t("summary.card.orders", { count: n(card.orders) })}</li>
      <li>{t("summary.card.sold", { count: n(card.sold) })}</li>
      {card.complimentary > 0 && <li>{t("summary.card.complimentary", { count: n(card.complimentary) })}</li>}
      {card.refunded_tickets > 0 && <li>{t("summary.card.refundedTickets", { count: n(card.refunded_tickets) })}</li>}
      <li>{t("summary.card.active", { count: n(card.active_tickets) })}</li>
    </ul>
  );
}

function PriceRows({ group, money }: { group: PriceGroup; money: (v: number) => string }) {
  const { locale } = useI18n();
  const n = (v: number) => formatNumber(v, locale);
  return (
    <>
      <tr>
        <th scope="row">{group.tierName}</th>
        <td>{money(group.unitPrice)}</td>
        <td>–</td>
        <td>{n(group.tickets)}</td>
        <td>{money(group.revenue)}</td>
      </tr>
      {group.promos.map((p) => (
        <tr key={p.promo_code} data-testid="summary-promo-row">
          <td />
          <td />
          <td>{p.promo_code}</td>
          <td>{n(p.tickets)}</td>
          <td>{money(p.revenue)}</td>
        </tr>
      ))}
    </>
  );
}

function Breakdown({ card }: { card: OrgSummaryCard }) {
  const { t, locale } = useI18n();
  const groups = groupBreakdown(card);
  if (groups.length === 0) return null;
  const money = (v: number) => formatMoney(v, card.currency, locale);
  return (
    <details data-testid="summary-breakdown">
      <summary>{t("summary.breakdown")}</summary>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th scope="col">{t("summary.breakdown.category")}</th>
              <th scope="col">{t("summary.breakdown.price")}</th>
              <th scope="col">{t("summary.breakdown.promo")}</th>
              <th scope="col">{t("summary.breakdown.tickets")}</th>
              <th scope="col">{t("summary.breakdown.revenue")}</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <PriceRows key={g.tierId + "|" + String(g.unitPrice)} group={g} money={money} />
            ))}
          </tbody>
        </table>
      </div>
    </details>
  );
}

function SessionCards({ orgId, session }: { orgId: string; session: OrgSummarySession }) {
  const { t, locale } = useI18n();
  return (
    <>
      {session.cards.map((card) => (
        <li key={session.id + "|" + card.currency} className="card" data-testid="summary-card">
          <Link
            to="/o/$orgId/events/$eventId/sessions/$sessionId"
            params={{ orgId, eventId: session.event_id, sessionId: session.id }}
            className="card-link"
          >
            <span className="card-title">{session.event_name}</span>
            <span className="card-meta">
              {formatDateTime(session.start_at, session.venue_timezone, locale)} · {card.currency}
            </span>
          </Link>
          <Figures card={card} />
          <Breakdown card={card} />
          {session.test_orders > 0 && <p className="muted">{t("summary.test", { count: formatNumber(session.test_orders, locale) })}</p>}
        </li>
      ))}
    </>
  );
}

export function OrgSummaryView({ orgId, summary }: { orgId: string; summary: OrgSummary }) {
  const { t, locale } = useI18n();
  const testLine =
    summary.test_orders > 0 ? (
      <p className="muted" data-testid="summary-test-orders">
        {t("summary.test", { count: formatNumber(summary.test_orders, locale) })}
      </p>
    ) : null;
  if (summary.sessions.length === 0) {
    return (
      <>
        <p className="notice" data-testid="summary-empty">
          {t("summary.empty")}
        </p>
        {testLine}
      </>
    );
  }
  return (
    <>
      <ul className="cards" data-testid="summary-totals">
        {summary.totals.map((total) => (
          <li key={total.currency} className="card" data-testid="summary-total">
            <span className="card-title">
              {t("summary.totals")} · {total.currency}
            </span>
            <Figures card={total} />
          </li>
        ))}
      </ul>
      <ul className="cards" data-testid="summary-cards">
        {summary.sessions.map((s) => (
          <SessionCards key={s.id} orgId={orgId} session={s} />
        ))}
      </ul>
      {summary.truncated && (
        <p className="notice notice-warn" data-testid="summary-truncated">
          {t("summary.truncated")}
        </p>
      )}
      {testLine}
    </>
  );
}
