/**
 * The new promo code (task 4-3): the form, then a check screen, then the
 * create. The rules are the bot's (lib/promo.ts): the name is upper-cased and
 * checked against the organization's codes before any call, a fixed discount
 * names a currency and only sessions sold in it can be ticked, a code for
 * chosen sessions needs at least one (an empty list would be a club code),
 * the last day ends at 23:59:59 UTC. Limits, last day and currency are set
 * here only: the API cannot clear them afterwards.
 */
import { useCallback, useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { createPromoCode, fetchPromoCode, listPromoCodes, listSessions } from "@/lib/api/endpoints";
import { filterEvents } from "@/lib/events";
import { formatMoney } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { buildCreateRequest, currencyDigits, dropOtherCurrencySessions, parseAmountMinor, readPromoForm, type PromoDraft, type PromoFieldErrors, type PromoFormRaw } from "@/lib/promo";
import { DateField } from "@/components/DateField";
import { ErrorNotice } from "@/components/ErrorNotice";
import { PromoSessionPicker } from "@/components/PromoSessionPicker";
import { useOrg } from "@/screens/OrgLayout";
import { useEventsList } from "@/screens/EventsScreen";
import { NoPromoAccess } from "@/screens/PromoNoAccess";
import { promoDiscountText, promoLimitsText, promoSessionsText, promoValidText } from "@/lib/promoView";
import { copySeed, eventNames } from "@/lib/promoScope";
import type { PromoCodeItem } from "@/lib/api/types";

const START: PromoFormRaw = { code: "", type: "percent", value: "", currency: "", all: true, total: "", per: "", until: null, status: "active" };

function FieldError({ id, error }: { id: string; error?: MessageKey }) {
  const { t } = useI18n();
  if (error === undefined) return null;
  return (
    <p id={id} className="field-error" role="alert">
      {t(error)}
    </p>
  );
}

function CreateForm() {
  const { t, locale } = useI18n();
  const ids = useId();
  const { orgId, access } = useOrg();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [raw, setRaw] = useState<PromoFormRaw>(START);
  const [sessionIds, setSessionIds] = useState<string[]>([]);
  const [sessionCurrencies, setSessionCurrencies] = useState<Record<string, string>>({});
  const [droppedNote, setDroppedNote] = useState<{ count: number; fixed: string } | null>(null);
  const [errors, setErrors] = useState<PromoFieldErrors>({});
  const [draft, setDraft] = useState<PromoDraft | null>(null); // set = the check screen
  // A copy starts from another code's discount and limits, never its sessions or last day: the person
  // must choose both again and tick that they did (the way one code ended up on the wrong event).
  const { copyFrom } = useSearch({ from: "/o/$orgId/promo/new" });
  const original = useQuery({ queryKey: ["promoCode", orgId, copyFrom], queryFn: () => fetchPromoCode(orgId, copyFrom ?? ""), enabled: copyFrom !== undefined });
  const [seeded, setSeeded] = useState(false);
  const [copyConfirmed, setCopyConfirmed] = useState(false);
  const [copyError, setCopyError] = useState(false);
  useEffect(() => {
    if (seeded || original.data === undefined) return;
    const o = original.data.promo_code;
    const seed = copySeed(o, currencyDigits(o.currency ?? ""));
    setRaw({ ...START, ...seed, all: false, until: null });
    setSeeded(true);
  }, [seeded, original.data]);

  const names = useQuery({ queryKey: ["promoCodes", orgId], queryFn: () => listPromoCodes(orgId) });
  const existing = useMemo(() => (names.data?.promo_codes ?? []).map((c) => c.code), [names.data]);

  // The currencies the organization sells in: read from its first running events.
  const events = useEventsList(orgId);
  const running = useMemo(() => filterEvents(events.data?.events ?? [], "run").slice(0, 6), [events.data]);
  const sessionLists = useQueries({
    queries: running.map((e) => ({ queryKey: ["sessions", orgId, e.id], queryFn: () => listSessions(orgId, e.id), staleTime: 30_000 })),
  });
  const currencies = useMemo(() => {
    const seen: string[] = [];
    for (const q of sessionLists) {
      for (const s of q.data?.sessions ?? []) {
        const c = s.currency.trim().toUpperCase();
        if (c !== "" && !seen.includes(c)) seen.push(c);
      }
    }
    return seen.slice(0, 4);
  }, [sessionLists]);
  // One currency in the whole organization: nobody is asked for it.
  useEffect(() => {
    if (raw.type === "fixed_amount" && raw.currency === "" && currencies.length === 1) setRaw((r) => ({ ...r, currency: currencies[0] }));
  }, [raw.type, raw.currency, currencies]);

  const create = useMutation({
    mutationFn: (d: PromoDraft) => {
      const body = buildCreateRequest(d);
      if (body === null) throw new Error("the draft is not complete");
      return createPromoCode(orgId, body);
    },
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: ["promoCodes", orgId] });
      await navigate({ to: "/o/$orgId/promo/$codeId", params: { orgId, codeId: res.promo_code.id }, search: { created: true } });
    },
  });

  const set = <K extends keyof PromoFormRaw>(key: K, value: PromoFormRaw[K]) => setRaw((r) => ({ ...r, [key]: value }));

  const onReview = (e: FormEvent) => {
    e.preventDefault();
    const read = readPromoForm(raw, sessionIds, existing, sessionCurrencies);
    setErrors(read.errors);
    setCopyError(false);
    if (copyFrom !== undefined && !copyConfirmed) {
      setCopyError(true);
      return;
    }
    create.reset();
    if (read.draft !== null) setDraft(read.draft);
  };

  const fixed = raw.type === "fixed_amount";
  // Only a complete three-letter currency judges sessions: "EU" is still being typed.
  const fixedCurrency = fixed && raw.currency.trim().length === 3 ? raw.currency.trim().toUpperCase() : "";

  // A session sold in another currency than a fixed discount cannot stay ticked: the code would never apply there.
  useEffect(() => {
    const { kept, dropped } = dropOtherCurrencySessions(sessionIds, sessionCurrencies, fixedCurrency);
    if (dropped.length === 0) return;
    setSessionIds(kept);
    setDroppedNote({ count: dropped.length, fixed: fixedCurrency });
  }, [sessionIds, sessionCurrencies, fixedCurrency]);
  const rememberCurrencies = useCallback((seen: Record<string, string>) => {
    setSessionCurrencies((prev) => (Object.entries(seen).every(([id, c]) => prev[id] === c) ? prev : { ...prev, ...seen }));
  }, []);

  if (draft !== null) {
    const preview = {
      code: draft.code,
      discount_type: draft.type,
      discount_value: draft.value,
      currency: draft.type === "fixed_amount" ? draft.currency : null,
      applies_to_session_ids: draft.all ? [] : [...draft.sessionIds],
      max_uses: draft.maxUses,
      max_uses_per_customer: draft.perBuyer,
    } as PromoCodeItem;
    return (
      <section aria-labelledby="promo-create-title" data-testid="promo-review">
        <h1 id="promo-create-title">{t("promo.review.title")}</h1>
        <dl className="facts-grid">
          <dt>{t("promo.f.code")}</dt>
          <dd className="mono">{draft.code}</dd>
          <dt>{t("promo.card.discount")}</dt>
          <dd>
            {promoDiscountText(preview, t, locale)}
            {draft.type === "fixed_amount" && <span className="muted"> {t("promo.review.fixedNote", { currency: draft.currency })}</span>}
          </dd>
          <dt>{t("promo.card.sessions")}</dt>
          <dd>{promoSessionsText(preview, t)}</dd>
          <dt>{t("promo.card.limits")}</dt>
          <dd>{promoLimitsText(preview, t)}</dd>
          <dt>{t("promo.f.until")}</dt>
          <dd>{draft.until ?? t("promo.valid.none")}</dd>
          <dt>{t("promo.f.status")}</dt>
          <dd>{t(draft.status === "active" ? "promo.status.active" : "promo.status.paused")}</dd>
        </dl>
        {create.error !== null && <ErrorNotice error={create.error} testId="promo-error" />}
        <div className="actions">
          <button type="button" className="btn btn-primary" disabled={create.isPending} onClick={() => create.mutate(draft)} data-testid="promo-create-go">
            {create.isPending ? t("promo.creating") : t("promo.create.go")}
          </button>
          <button
            type="button"
            className="btn"
            disabled={create.isPending}
            onClick={() => {
              create.reset();
              setDraft(null);
            }}
            data-testid="promo-create-back"
          >
            {t("promo.create.back")}
          </button>
        </div>
      </section>
    );
  }

  const describedBy = (k: keyof PromoFieldErrors) => (errors[k] !== undefined ? `${ids}-${k}-err` : undefined);
  return (
    <section aria-labelledby="promo-create-title">
      <p>
        <Link to="/o/$orgId/promo" params={{ orgId }} search={{}}>
          {t("promo.back")}
        </Link>
      </p>
      <h1 id="promo-create-title">{original.data !== undefined ? t("promo.copy.title", { code: original.data.promo_code.code }) : t("promo.create.title")}</h1>
      {original.data !== undefined && (
        <div className="notice" data-testid="promo-copy-note">
          <p>{t("promo.copy.note")}</p>
          <p className="muted">
            {t("promo.copy.was", {
              scope: original.data.promo_code.sessions.length === 0 ? t("promo.sessions.all") : eventNames(original.data.promo_code).join(", "),
              until: promoValidText({ ...original.data.promo_code, valid_from: null }, t, locale),
            })}
          </p>
        </div>
      )}
      <form onSubmit={onReview} noValidate data-testid="promo-form">
        <label htmlFor={`${ids}-code`}>{t("promo.f.code")}</label>
        <input
          id={`${ids}-code`}
          type="text"
          autoComplete="off"
          autoCapitalize="characters"
          spellCheck={false}
          value={raw.code}
          onChange={(e) => set("code", e.target.value)}
          aria-invalid={errors.code !== undefined}
          aria-describedby={[`${ids}-code-hint`, describedBy("code")].filter(Boolean).join(" ")}
          data-testid="promo-f-code"
        />
        <p id={`${ids}-code-hint`} className="muted">
          {t("promo.f.codeHint")}
        </p>
        <FieldError id={`${ids}-code-err`} error={errors.code} />

        <fieldset>
          <legend>{t("promo.f.type")}</legend>
          {(["percent", "fixed_amount"] as const).map((k) => (
            <label key={k} className="check">
              <input type="radio" name={`${ids}-type`} checked={raw.type === k} onChange={() => set("type", k)} data-testid={`promo-f-type-${k}`} />
              <span>{t(k === "percent" ? "promo.type.percent" : "promo.type.fixed")}</span>
            </label>
          ))}
        </fieldset>

        {fixed && (
          <>
            <label htmlFor={`${ids}-currency`}>{t("promo.f.currency")}</label>
            <input
              id={`${ids}-currency`}
              type="text"
              list={`${ids}-currencies`}
              maxLength={3}
              autoComplete="off"
              spellCheck={false}
              value={raw.currency}
              onChange={(e) => set("currency", e.target.value.toUpperCase())}
              aria-invalid={errors.currency !== undefined}
              aria-describedby={describedBy("currency")}
              data-testid="promo-f-currency"
            />
            <datalist id={`${ids}-currencies`}>
              {currencies.map((c) => (
                <option key={c} value={c} />
              ))}
            </datalist>
            <FieldError id={`${ids}-currency-err`} error={errors.currency} />
          </>
        )}

        <label htmlFor={`${ids}-value`}>{t(fixed ? "promo.f.amount" : "promo.f.percent")}</label>
        <input
          id={`${ids}-value`}
          type="text"
          inputMode="decimal"
          autoComplete="off"
          value={raw.value}
          onChange={(e) => set("value", e.target.value)}
          aria-invalid={errors.value !== undefined}
          aria-describedby={[fixed ? `${ids}-value-hint` : "", describedBy("value")].filter(Boolean).join(" ") || undefined}
          data-testid="promo-f-value"
        />
        {fixed && (
          <p id={`${ids}-value-hint`} className="muted">
            {t("promo.f.amountHint")}
            {raw.value.trim() !== "" && fixedCurrency.length === 3 && (() => {
              const a = parseAmountMinor(raw.value, fixedCurrency);
              return a.ok ? ` ${t("promo.f.amountRead", { amount: formatMoney(a.value, fixedCurrency, locale) })}` : "";
            })()}
          </p>
        )}
        <FieldError id={`${ids}-value-err`} error={errors.value} />

        <fieldset>
          <legend>{t("promo.f.scope")}</legend>
          <label className="check">
            <input type="radio" name={`${ids}-scope`} checked={raw.all} onChange={() => set("all", true)} data-testid="promo-f-scope-all" />
            <span>{t("promo.scope.all")}</span>
          </label>
          <label className="check">
            <input type="radio" name={`${ids}-scope`} checked={!raw.all} onChange={() => set("all", false)} data-testid="promo-f-scope-pick" />
            <span>{t("promo.scope.pick")}</span>
          </label>
          {!raw.all && (
            <PromoSessionPicker
              orgId={orgId}
              selected={sessionIds}
              onChange={(next) => {
                setDroppedNote(null);
                setSessionIds(next);
              }}
              fixedCurrency={fixedCurrency}
              canSummary={access.can("order.read")}
              onCurrencies={rememberCurrencies}
            />
          )}
          {!raw.all && droppedNote !== null && (
            <p className="muted" role="status" data-testid="promo-pick-dropped">
              {t("promo.pick.dropped", { count: droppedNote.count, fixed: droppedNote.fixed })}
            </p>
          )}
          <FieldError id={`${ids}-scope-err`} error={errors.scope} />
        </fieldset>

        <label htmlFor={`${ids}-total`}>{t("promo.f.total")}</label>
        <input
          id={`${ids}-total`}
          type="text"
          inputMode="numeric"
          autoComplete="off"
          value={raw.total}
          onChange={(e) => set("total", e.target.value)}
          aria-invalid={errors.total !== undefined}
          aria-describedby={[`${ids}-limit-hint`, describedBy("total")].filter(Boolean).join(" ")}
          data-testid="promo-f-total"
        />
        <FieldError id={`${ids}-total-err`} error={errors.total} />
        <label htmlFor={`${ids}-per`}>{t("promo.f.per")}</label>
        <input
          id={`${ids}-per`}
          type="text"
          inputMode="numeric"
          autoComplete="off"
          value={raw.per}
          onChange={(e) => set("per", e.target.value)}
          aria-invalid={errors.per !== undefined}
          aria-describedby={[`${ids}-limit-hint`, describedBy("per")].filter(Boolean).join(" ")}
          data-testid="promo-f-per"
        />
        <p id={`${ids}-limit-hint`} className="muted">
          {t("promo.f.limitHint")}
        </p>
        <FieldError id={`${ids}-per-err`} error={errors.per} />

        <fieldset>
          <legend>{t("promo.f.until")}</legend>
          <p className="muted">{t("promo.f.untilHint")}</p>
          {raw.until === null ? (
            <p data-testid="promo-f-until-none">{t("promo.valid.none")}</p>
          ) : (
            <p data-testid="promo-f-until-value">
              {raw.until}{" "}
              <button type="button" className="btn btn-link" onClick={() => set("until", null)} data-testid="promo-f-until-clear">
                {t("promo.f.untilClear")}
              </button>
            </p>
          )}
          <DateField value={raw.until} onChange={(iso) => set("until", iso)} label={t("promo.f.untilPick")} />
        </fieldset>

        <fieldset>
          <legend>{t("promo.f.status")}</legend>
          {(["active", "paused"] as const).map((k) => (
            <label key={k} className="check">
              <input type="radio" name={`${ids}-status`} checked={raw.status === k} onChange={() => set("status", k)} data-testid={`promo-f-status-${k}`} />
              <span>{t(k === "active" ? "promo.status.active" : "promo.status.paused")}</span>
            </label>
          ))}
        </fieldset>

        {copyFrom !== undefined && (
          <>
            <label className="check">
              <input type="checkbox" checked={copyConfirmed} onChange={(e) => setCopyConfirmed(e.target.checked)} data-testid="promo-copy-confirm" />
              <span>{t("promo.copy.confirm")}</span>
            </label>
            {copyError && (
              <p className="field-error" role="alert" data-testid="promo-copy-error">
                {t("promo.err.copyConfirm")}
              </p>
            )}
          </>
        )}
        <div className="actions">
          <button type="submit" className="btn btn-primary" data-testid="promo-review">
            {t("promo.review")}
          </button>
        </div>
      </form>
    </section>
  );
}

export function PromoCreateScreen() {
  const { access } = useOrg();
  if (!access.can("promo.create")) return <NoPromoAccess create />;
  return <CreateForm />;
}
