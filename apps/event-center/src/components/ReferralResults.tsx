/**
 * "Refer a friend" (spec 52, section 5.2): the totals of an event's programme and the list of its
 * rewards. Read-only, `promo.read`.
 *
 * Money is never added across currencies: the API answers per currency and each currency is its own
 * block. A reward shows the personal code, the friend's order number and amount, the cashback, where it
 * stands in words and why it ended without money. No buyer name, e-mail or phone is part of the answer,
 * and none is shown.
 */
import { useId, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { SelectField } from "@/components/FormField";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { fetchReferralRewards, fetchReferralSummary, referralTargetKey, type ReferralTarget } from "@/lib/api/endpoints";
import type { ReferralRewardItem, ReferralSummary } from "@/lib/api/types";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { REWARD_STATES, groupByCurrency, rewardReasonKey, rewardStateKey, rewardStateTone, reversalKey } from "@/lib/referral";

export const REWARD_PAGE_SIZE = 20;

export function ReferralSummaryBlock({ summary }: { summary: ReferralSummary }) {
  const { t, locale } = useI18n();
  const groups = groupByCurrency(summary.discount_given, summary.cashback);
  const n = (v: number) => formatNumber(v, locale);
  return (
    <div data-testid="referral-summary">
      <ul className="figures">
        <li data-testid="referral-codes">{t("referral.summary.codes", { count: n(summary.codes_issued) })}</li>
        <li data-testid="referral-friends">{t("referral.summary.friends", { count: n(summary.friends) })}</li>
        {summary.friend_reversals > 0 && (
          <li data-testid="referral-reversals">{t("referral.summary.reversals", { count: n(summary.friend_reversals) })}</li>
        )}
      </ul>
      {groups.length === 0 ? (
        <p className="muted" data-testid="referral-summary-empty">
          {t("referral.summary.noMoney")}
        </p>
      ) : (
        groups.map((g) => (
          <section key={g.currency} className="panel" aria-label={g.currency} data-testid="referral-currency" data-currency={g.currency}>
            <h4>{g.currency}</h4>
            <ul className="figures">
              {g.discount > 0 && (
                <li data-testid="referral-discount">{t("referral.summary.discount", { amount: formatMoney(g.discount, g.currency, locale) })}</li>
              )}
              {g.cashback.map((l) => (
                <li key={l.state} data-testid={`referral-cashback-${l.state}`}>
                  {t(`referral.summary.cashback.${l.state}`, { amount: formatMoney(l.amount, g.currency, locale) })}
                </li>
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  );
}

export function RewardRow({ r, zone, target = { kind: "event", eventId: "" } }: { r: ReferralRewardItem; zone: string | undefined; target?: ReferralTarget }) {
  const { t, locale } = useI18n();
  const reason = rewardReasonKey(r.reason, target);
  const reversal = reversalKey(r.friend_reversal);
  return (
    <li className="card" data-testid="referral-reward" data-state={r.state}>
      <span className="card-title">
        <code data-testid="referral-reward-code">{r.code}</code>
      </span>
      <span className="card-meta">
        <Badge tone={rewardStateTone(r.state)}>{t(rewardStateKey(r.state))}</Badge>
        {reversal !== null && (
          <Badge tone="bad">
            <span data-testid="referral-reward-reversal">{t(reversal)}</span>
          </Badge>
        )}
      </span>
      <dl className="facts-grid compact">
        <dt>{t("referral.reward.friendOrder")}</dt>
        <dd data-testid="referral-reward-friend-order">
          {r.friend_order_number === null || r.friend_order_number === undefined ? t("referral.reward.none") : `№ ${r.friend_order_number}`}
        </dd>
        <dt>{t("referral.reward.friendPaid")}</dt>
        <dd>
          {r.friend_amount_minor === null || r.friend_amount_minor === undefined || r.friend_currency === null || r.friend_currency === undefined
            ? t("referral.reward.none")
            : formatMoney(r.friend_amount_minor, r.friend_currency, locale)}
        </dd>
        <dt>{t("referral.reward.cashback", { pct: r.cashback_pct })}</dt>
        <dd data-testid="referral-reward-cashback">
          <strong>{formatMoney(r.cashback_minor, r.currency, locale)}</strong>
        </dd>
        {r.referrer_order_number !== null && r.referrer_order_number !== undefined && (
          <>
            <dt>{t("referral.reward.referrerOrder")}</dt>
            <dd>№ {r.referrer_order_number}</dd>
          </>
        )}
        <dt>{t("referral.reward.when")}</dt>
        <dd>{formatDateTime(r.created_at, zone, locale)}</dd>
      </dl>
      {reason !== null && (
        <p className="muted" data-testid="referral-reward-reason" title={r.reason ?? undefined}>
          {t(reason)}
        </p>
      )}
      {reversal !== null && (
        <p className="notice notice-warn" role="note" data-testid="referral-reward-reversal-note">
          {t("referral.reversal.note")}
        </p>
      )}
    </li>
  );
}

export function ReferralResults({ orgId, target, timezone }: { orgId: string; target: ReferralTarget; timezone?: string }) {
  const { t } = useI18n();
  const targetId = referralTargetKey(target);
  const ids = useId();
  const [state, setState] = useState("");

  const summary = useQuery({
    queryKey: ["referralSummary", orgId, targetId],
    queryFn: () => fetchReferralSummary(orgId, target),
  });
  const rewards = useInfiniteQuery({
    queryKey: ["referralRewards", orgId, targetId, state],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      fetchReferralRewards(orgId, target, { state: state === "" ? undefined : state, cursor: pageParam, limit: REWARD_PAGE_SIZE }),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const rows = rewards.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div data-testid="referral-results">
      <h3 id={`${ids}-sum`}>{t("referral.summary.title")}</h3>
      {summary.isLoading && <Loading />}
      {summary.error !== null && <ErrorBox error={summary.error} onRetry={() => void summary.refetch()} />}
      {summary.data !== undefined && <ReferralSummaryBlock summary={summary.data} />}

      <h3 id={`${ids}-list`}>{t("referral.rewards.title")}</h3>
      <SelectField label={t("referral.rewards.filter")} value={state} onChange={setState} testId="referral-filter">
        <option value="">{t("referral.rewards.all")}</option>
        {REWARD_STATES.map((s) => (
          <option key={s} value={s}>
            {t(rewardStateKey(s))}
          </option>
        ))}
      </SelectField>
      {rewards.isLoading && <Loading />}
      {rewards.error !== null && <ErrorBox error={rewards.error} onRetry={() => void rewards.refetch()} />}
      {rewards.data !== undefined && rows.length === 0 && <Empty>{t(state === "" ? "referral.rewards.empty" : "referral.rewards.emptyFiltered")}</Empty>}
      {rows.length > 0 && (
        <ul className="cards" aria-labelledby={`${ids}-list`} data-testid="referral-rewards">
          {rows.map((r) => (
            <RewardRow key={r.id} r={r} zone={timezone} target={target} />
          ))}
        </ul>
      )}
      {rewards.hasNextPage && (
        <p>
          <button
            type="button"
            className="btn"
            disabled={rewards.isFetchingNextPage}
            onClick={() => void rewards.fetchNextPage()}
            data-testid="referral-rewards-more"
          >
            {t("referral.rewards.more")}
          </button>
        </p>
      )}
    </div>
  );
}
