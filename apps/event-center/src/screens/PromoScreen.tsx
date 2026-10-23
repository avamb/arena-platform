/**
 * The card of one promo code (task 4-3): its rule, state and usage, pause and
 * activate, the sessions it is for, the usage report by buyer NAME, the CSV
 * and the delete with the typed word. Every action is shown only to a person
 * whose role in THIS organization holds its permission: promo.update for
 * pause and sessions, promo.delete for delete.
 */
import { useState } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  deletePromoCode,
  downloadPromoCsv,
  fetchPromoCode,
  listPromoRedemptions,
  setPromoSessions,
  setPromoStatus,
  setPromoValidUntil,
} from "@/lib/api/endpoints";
import type { PromoCodeItem, PromoRedemptionItem } from "@/lib/api/types";
import { formatDate, formatDateTime, formatMoney } from "@/lib/format";
import { useI18n, type Params } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { orderNumber } from "@/lib/orders";
import { isClubCode, promoState } from "@/lib/promo";
import { canExtend, extendTarget, promoZone } from "@/lib/promoScope";
import { promoDiscountText, promoLimitsText, promoSessionsText, promoUsageText, promoValidText } from "@/lib/promoView";
import { ConfirmWordDialog } from "@/components/ConfirmWordDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { PromoSessionPicker } from "@/components/PromoSessionPicker";
import { PromoScopeList, PromoWarnings } from "@/components/PromoScope";
import { PromoStateBadge } from "@/components/PromoStateBadge";
import { DownloadButton, Empty, ErrorBox, Loading } from "@/components/ui";
import { NoPromoAccess } from "@/screens/PromoNoAccess";
import { useOrg } from "@/screens/OrgLayout";

export const USAGE_PAGE_SIZE = 10;

interface Notice {
  key: MessageKey;
  params?: Params;
}

function UsageTable({ orgId, rows, canOrders }: { orgId: string; rows: readonly PromoRedemptionItem[]; canOrders: boolean }) {
  const { t, locale } = useI18n();
  const [page, setPage] = useState(1);
  const pages = Math.max(1, Math.ceil(rows.length / USAGE_PAGE_SIZE));
  const at = Math.min(page, pages);
  const visible = rows.slice((at - 1) * USAGE_PAGE_SIZE, at * USAGE_PAGE_SIZE);
  return (
    <>
      <div className="table-wrap">
        <table className="table" data-testid="promo-usage-table">
          <caption>{t("promo.usage.title")}</caption>
          <thead>
            <tr>
              <th scope="col">{t("promo.col.order")}</th>
              <th scope="col">{t("promo.col.buyer")}</th>
              <th scope="col">{t("promo.col.discount")}</th>
              <th scope="col">{t("promo.col.orderTotal")}</th>
              <th scope="col">{t("promo.col.date")}</th>
            </tr>
          </thead>
          <tbody>
            {visible.map((r) => {
              const cur = r.currency ?? "";
              const number = r.order_number !== null ? orderNumber(r.order_number) : "—";
              return (
                <tr key={r.id} data-testid="promo-usage-row">
                  <th scope="row" data-label={t("promo.col.order")}>
                    {canOrders && r.order_id !== null ? (
                      <Link to="/o/$orgId/orders/$orderId" params={{ orgId, orderId: r.order_id }}>
                        {number}
                      </Link>
                    ) : (
                      number
                    )}
                  </th>
                  {/* The buyer's NAME only: the e-mail is never shown on a screen. */}
                  <td data-label={t("promo.col.buyer")}>{r.buyer_name ?? t("promo.buyerUnknown")}</td>
                  <td data-label={t("promo.col.discount")}>{cur !== "" ? formatMoney(r.discount_amount, cur, locale) : r.discount_amount}</td>
                  <td data-label={t("promo.col.orderTotal")}>{cur !== "" ? formatMoney(r.order_amount, cur, locale) : r.order_amount}</td>
                  <td data-label={t("promo.col.date")}>{formatDate(r.redeemed_at, "UTC", locale)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {pages > 1 && (
        <nav className="pager" aria-label={t("events.page", { page: at, pages })}>
          <button type="button" className="btn" disabled={at <= 1} onClick={() => setPage(at - 1)}>
            {t("events.prev")}
          </button>
          <span aria-live="polite" data-testid="promo-usage-page">
            {t("events.page", { page: at, pages })}
          </span>
          <button type="button" className="btn" disabled={at >= pages} onClick={() => setPage(at + 1)}>
            {t("events.nextPage")}
          </button>
        </nav>
      )}
    </>
  );
}

function SessionsEditor({
  orgId,
  item,
  canSummary,
  onSaved,
  onClose,
}: {
  orgId: string;
  item: PromoCodeItem;
  canSummary: boolean;
  onSaved: (saved: PromoCodeItem, allSessions: boolean) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [selected, setSelected] = useState<string[]>([...item.applies_to_session_ids]);
  const save = useMutation({
    mutationFn: (ids: readonly string[]) => setPromoSessions(orgId, item.id, ids),
    onSuccess: (res, ids) => onSaved(res.promo_code, ids.length === 0),
  });
  const fixedCurrency = item.discount_type === "fixed_amount" ? (item.currency ?? "") : "";
  return (
    <section className="panel" aria-labelledby="promo-sessions-title" data-testid="promo-sessions-editor">
      <h2 id="promo-sessions-title">{t("promo.sessions.title")}</h2>
      <p>{isClubCode(item) ? t("promo.sessions.allNow") : t("promo.sessions.nowN", { count: item.applies_to_session_ids.length })}</p>
      <div className="actions">
        <button
          type="button"
          className="btn"
          disabled={save.isPending || isClubCode(item)}
          onClick={() => save.mutate([])}
          data-testid="promo-sessions-all"
        >
          {t("promo.sessions.allBtn")}
        </button>
      </div>
      <h3>{t("promo.sessions.onlyTitle")}</h3>
      <PromoSessionPicker orgId={orgId} selected={selected} onChange={setSelected} fixedCurrency={fixedCurrency} canSummary={canSummary} disabled={save.isPending} />
      {/* An empty list would silently become "all sessions": it is saved only by the button above. */}
      {selected.length === 0 && <p className="muted">{t("promo.err.pickNone")}</p>}
      {save.error !== null && <ErrorNotice error={save.error} testId="promo-sessions-error" />}
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          disabled={save.isPending || selected.length === 0}
          onClick={() => save.mutate(selected)}
          data-testid="promo-sessions-save"
        >
          {t("promo.sessions.save")}
        </button>
        <button type="button" className="btn" disabled={save.isPending} onClick={onClose}>
          {t("promo.sessions.close")}
        </button>
      </div>
    </section>
  );
}

export function PromoScreen() {
  const { t, locale } = useI18n();
  const { orgId, access } = useOrg();
  const { codeId } = useParams({ from: "/o/$orgId/promo/$codeId" });
  const search = useSearch({ from: "/o/$orgId/promo/$codeId" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const canRead = access.can("promo.read");
  const canOrders = access.can("order.read");
  const [notice, setNotice] = useState<Notice | null>(search.created === true ? { key: "promo.created" } : null);
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const code = useQuery({
    queryKey: ["promoCode", orgId, codeId],
    queryFn: () => fetchPromoCode(orgId, codeId),
    enabled: canRead,
  });
  const usage = useQuery({
    queryKey: ["promoRedemptions", orgId, codeId],
    queryFn: () => listPromoRedemptions(orgId, codeId),
    enabled: canRead,
  });

  const remember = (saved: PromoCodeItem) => {
    queryClient.setQueryData(["promoCode", orgId, codeId], { promo_code: saved });
    void queryClient.invalidateQueries({ queryKey: ["promoCodes", orgId] });
  };
  const toggle = useMutation({
    mutationFn: (status: "active" | "paused") => setPromoStatus(orgId, codeId, status),
    onSuccess: (res) => {
      remember(res.promo_code);
      setNotice({ key: res.promo_code.status === "paused" ? "promo.pausedDone" : "promo.activatedDone", params: { code: res.promo_code.code } });
    },
  });

  const extend = useMutation({
    mutationFn: (until: string) => setPromoValidUntil(orgId, codeId, until),
    onSuccess: (res) => {
      remember(res.promo_code);
      setNotice({
        key: "promo.extended",
        params: { code: res.promo_code.code, date: res.promo_code.valid_until === null ? "" : formatDateTime(res.promo_code.valid_until, promoZone(res.promo_code), locale) },
      });
    },
  });

  if (!canRead) return <NoPromoAccess />;
  const back = (
    <p>
      <Link to="/o/$orgId/promo" params={{ orgId }} search={{}}>
        {t("promo.back")}
      </Link>
    </p>
  );
  if (code.isLoading) return <Loading />;
  if (code.error !== null || code.data === undefined) {
    return (
      <>
        {back}
        <ErrorBox error={code.error} onRetry={() => void code.refetch()} />
      </>
    );
  }
  const item = code.data.promo_code;
  const state = promoState(item);
  const paused = item.status === "paused";

  return (
    <article aria-labelledby="promo-card-title">
      {back}
      <h1 id="promo-card-title" className="mono" data-testid="promo-card-code">
        {item.code}
      </h1>
      <p className="card-meta">
        <PromoStateBadge item={item} />
      </p>
      {notice !== null && (
        <p className="notice notice-ok" role="status" data-testid="promo-notice">
          {t(notice.key, notice.params ?? { code: item.code })}
        </p>
      )}
      {paused && <p className="muted">{t("promo.pausedNote")}</p>}
      {state === "expired" && <p className="muted">{t("promo.expiredNote")}</p>}
      <PromoWarnings item={item} />

      <dl className="facts-grid" data-testid="promo-facts">
        <dt>{t("promo.card.discount")}</dt>
        <dd data-testid="promo-discount">{promoDiscountText(item, t, locale)}</dd>
        {item.discount_type === "fixed_amount" && item.currency !== null && (
          <>
            <dt>{t("promo.card.currency")}</dt>
            <dd>{t("promo.card.onlyCurrency", { currency: item.currency })}</dd>
          </>
        )}
        <dt>{t("promo.card.sessions")}</dt>
        <dd data-testid="promo-sessions">{promoSessionsText(item, t)}</dd>
        <dt>{t("promo.card.limits")}</dt>
        <dd>{promoLimitsText(item, t)}</dd>
        <dt>{t("promo.card.valid")}</dt>
        <dd>{promoValidText(item, t, locale)}</dd>
        <dt>{t("promo.card.usage")}</dt>
        <dd data-testid="promo-usage">{promoUsageText(item, t, locale)}</dd>
      </dl>

      <div className="actions">
        {access.can("promo.update") && (
          <>
            <button
              type="button"
              className="btn"
              disabled={toggle.isPending}
              onClick={() => toggle.mutate(paused ? "active" : "paused")}
              data-testid={paused ? "promo-activate" : "promo-pause"}
            >
              {t(paused ? "promo.activate" : "promo.pause")}
            </button>
            <button type="button" className="btn" aria-expanded={editing} onClick={() => setEditing((v) => !v)} data-testid="promo-sessions-open">
              {t("promo.sessions.attach")}
            </button>
            {canExtend(item, new Date()) && (
              <button
                type="button"
                className="btn"
                disabled={extend.isPending}
                onClick={() => {
                  const until = extendTarget(item, new Date());
                  if (until !== null) extend.mutate(until);
                }}
                data-testid="promo-extend"
              >
                {t("promo.extend")}
              </button>
            )}
          </>
        )}
        {canRead && (
          <DownloadButton label="promo.csv" testId="promo-csv" fetchFile={() => downloadPromoCsv(orgId, item.id, locale)} />
        )}
        {access.can("promo.create") && (
          <Link to="/o/$orgId/promo/new" params={{ orgId }} search={{ copyFrom: item.id }} className="btn" data-testid="promo-copy">
            {t("promo.copy")}
          </Link>
        )}
        {access.can("promo.delete") && (
          <button type="button" className="btn" onClick={() => setDeleting(true)} data-testid="promo-delete">
            {t("promo.delete")}
          </button>
        )}
      </div>
      {toggle.error !== null && <ErrorNotice error={toggle.error} testId="promo-error" />}
      {extend.error !== null && <ErrorNotice error={extend.error} testId="promo-extend-error" />}

      <PromoScopeList item={item} />

      {editing && access.can("promo.update") && (
        <SessionsEditor
          key={item.applies_to_session_ids.join(",")}
          orgId={orgId}
          item={item}
          canSummary={canOrders}
          onClose={() => setEditing(false)}
          onSaved={(saved, all) => {
            remember(saved);
            setNotice(
              all
                ? { key: "promo.sessions.allSaved", params: { code: saved.code } }
                : { key: "promo.sessions.saved", params: { code: saved.code, count: saved.applies_to_session_ids.length } },
            );
            setEditing(false);
          }}
        />
      )}

      <section aria-labelledby="promo-usage-title">
        <h2 id="promo-usage-title">{t("promo.usage.title")}</h2>
        {usage.isLoading && <Loading />}
        {usage.error !== null && <ErrorBox error={usage.error} onRetry={() => void usage.refetch()} />}
        {usage.data !== undefined && usage.data.redemptions.length === 0 && <Empty>{t("promo.usage.empty")}</Empty>}
        {usage.data !== undefined && usage.data.redemptions.length > 0 && (
          <UsageTable orgId={orgId} rows={usage.data.redemptions} canOrders={canOrders} />
        )}
      </section>

      {deleting && (
        <ConfirmWordDialog
          kind="delete_promo"
          subject={t("promo.deleteSubject", { code: item.code, count: item.uses })}
          onCancel={() => setDeleting(false)}
          onConfirm={async () => {
            await deletePromoCode(orgId, item.id);
            await queryClient.invalidateQueries({ queryKey: ["promoCodes", orgId] });
            queryClient.removeQueries({ queryKey: ["promoCode", orgId, codeId] });
            setDeleting(false);
            await navigate({ to: "/o/$orgId/promo", params: { orgId }, search: { deleted: item.code } });
          }}
        />
      )}
    </article>
  );
}
