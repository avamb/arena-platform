/**
 * "Smart price" on the card of one category (spec
 * 08_architecture/51_4_2_smart_price_ru.md). The web twin of the bot's
 * smart price screen (eventbot/smart_price.go).
 *
 * The organizer picks a preset, sees the server's table of steps or price
 * windows (the preview route writes nothing) and turns it on with one press.
 * An active preset shows where it stands and can be stopped: the price then
 * stays where it is. Nothing here computes a price: every number on the screen
 * is the server's.
 */
import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { enableSmartPrice, listSmartPrice, previewSmartPrice, stopSmartPrice } from "@/lib/api/endpoints";
import type { SmartPricePreviewResponse, SmartPriceRule, TicketTierItem } from "@/lib/api/types";
import {
  activeRuleOf,
  allowedKinds,
  defaultSmartForm,
  endedRuleOf,
  parseSmartForm,
  smartEligibility,
  type FieldError,
  type SmartForm,
  type SmartKind,
} from "@/lib/smartPrice";
import { formatDateTime, formatMoney, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { DateField } from "@/components/DateField";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";

export interface SmartPriceSectionProps {
  orgId: string;
  eventId: string;
  sessionId: string;
  tier: TicketTierItem;
  /** The category already sells in steps (a chain made by hand or by a preset). */
  inChain: boolean;
  /** The category's places come from a seating plan: it rises by days only. */
  seated: boolean;
  timezone: string | null;
}

type Stage = "idle" | "form" | "preview" | "stopConfirm";

export function smartPriceQueryKey(orgId: string, sessionId: string) {
  return ["smartPrice", orgId, sessionId] as const;
}

export function SmartPriceSection({ orgId, eventId, sessionId, tier, inChain, seated, timezone }: SmartPriceSectionProps) {
  const { t, locale } = useI18n();
  const queryClient = useQueryClient();
  const [stage, setStage] = useState<Stage>("idle");
  const [form, setForm] = useState<SmartForm>(() => defaultSmartForm(seated ? "by_days" : "by_sales"));
  const [errors, setErrors] = useState<Partial<Record<"step" | "every" | "cap", FieldError>>>({});
  const [table, setTable] = useState<SmartPricePreviewResponse | null>(null);
  const [done, setDone] = useState<"enabled" | "stopped" | null>(null);

  const rules = useQuery({
    queryKey: smartPriceQueryKey(orgId, sessionId),
    queryFn: () => listSmartPrice(orgId, sessionId),
  });
  const active = rules.data !== undefined ? activeRuleOf(rules.data.rules, tier.id) : undefined;
  const ended = rules.data !== undefined ? endedRuleOf(rules.data.rules, tier.id) : undefined;

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: smartPriceQueryKey(orgId, sessionId) });
    void queryClient.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] });
    void queryClient.invalidateQueries({ queryKey: ["tierSchedule", orgId, eventId, sessionId] });
  };

  const preview = useMutation({
    mutationFn: (body: Parameters<typeof previewSmartPrice>[2]) => previewSmartPrice(orgId, sessionId, body),
    onSuccess: (res) => {
      setTable(res);
      setStage("preview");
    },
  });
  const enable = useMutation({
    mutationFn: (body: Parameters<typeof enableSmartPrice>[2]) => enableSmartPrice(orgId, sessionId, body),
    onSuccess: () => {
      setDone("enabled");
      setStage("idle");
      setTable(null);
      refresh();
    },
  });
  const stop = useMutation({
    mutationFn: (ruleId: string) => stopSmartPrice(orgId, sessionId, ruleId),
    onSuccess: () => {
      setDone("stopped");
      setStage("idle");
      refresh();
    },
  });

  const busy = preview.isPending || enable.isPending || stop.isPending;
  const money = (minor: number) => formatMoney(minor, tier.currency, locale);
  const eligibility = smartEligibility(tier, inChain);
  const kinds = allowedKinds(seated);

  const reset = () => {
    preview.reset();
    enable.reset();
    stop.reset();
    setDone(null);
    setErrors({});
    setTable(null);
  };
  const setKind = (kind: SmartKind) => {
    setForm(defaultSmartForm(kind));
    setErrors({});
    setTable(null);
    setStage("form");
  };
  const parsed = () => {
    const r = parseSmartForm(form);
    if (!r.ok) {
      setErrors(r.errors);
      return null;
    }
    setErrors({});
    return { ...r.body, tier_ids: [tier.id] };
  };
  const onPreview = (e: FormEvent) => {
    e.preventDefault();
    const body = parsed();
    if (body !== null) preview.mutate(body);
  };
  const onEnable = () => {
    const body = parsed();
    if (body !== null) enable.mutate(body);
  };

  const ruleText = (r: SmartPriceRule): string => {
    const p = r.params;
    return t(`smartPrice.rule.${r.kind as SmartKind}`, {
      step: p.step_percent ?? 0,
      every: formatNumber(p.every ?? 0, locale),
      cap: p.cap_percent ?? 0,
    });
  };

  const err = preview.error ?? enable.error ?? stop.error;

  return (
    <div className="smart-price" data-testid="smart-price">
      <h4>{t("smartPrice.title")}</h4>

      {/* "On" is said only while the preset is still on: a price set by hand ends it meanwhile. */}
      {(done === "stopped" || (done === "enabled" && active !== undefined)) && (
        <p className="notice notice-ok" role="status" data-testid="smart-price-done">
          {t(done === "enabled" ? "smartPrice.enabled" : "smartPrice.stopped")}
        </p>
      )}
      {err !== null && <ErrorNotice error={err} testId="smart-price-error" />}
      {rules.error !== null && <ErrorNotice error={rules.error} testId="smart-price-load-error" />}

      {/* An active preset: where it stands and the way to stop it. */}
      {active !== undefined && (
        <div data-testid="smart-price-active">
          <p>{t("smartPrice.active", { rule: ruleText(active) })}</p>
          {active.now != null && (
            <p data-testid="smart-price-now">
              {t("smartPrice.now.price", { price: money(active.now.price), step: active.now.step, steps: active.now.steps })}{" "}
              {active.now.next_price == null
                ? t("smartPrice.now.last")
                : active.now.remaining_at_price != null
                  ? t("smartPrice.now.left", { next: money(active.now.next_price), left: formatNumber(active.now.remaining_at_price, locale) })
                  : active.now.next_at != null
                    ? t("smartPrice.now.at", { next: money(active.now.next_price), when: formatDateTime(active.now.next_at, timezone, locale) })
                    : null}
            </p>
          )}
          {stage !== "stopConfirm" ? (
            <div className="actions">
              <button type="button" className="btn" disabled={busy} onClick={() => { reset(); setStage("stopConfirm"); }} data-testid="smart-price-stop">
                {t("smartPrice.stop")}
              </button>
            </div>
          ) : (
            <div role="group" aria-labelledby="smart-stop-title" data-testid="smart-price-stop-confirm">
              <p id="smart-stop-title">
                <strong>{t("smartPrice.stopConfirm", { price: money(active.now?.price ?? active.base_price) })}</strong>
              </p>
              <div className="actions">
                <button type="button" className="btn btn-primary" disabled={busy} onClick={() => stop.mutate(active.id)} data-testid="smart-price-stop-yes">
                  {t("smartPrice.stopYes")}
                </button>
                <button type="button" className="btn" disabled={busy} onClick={() => setStage("idle")}>
                  {t("smartPrice.cancel")}
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      {active === undefined && ended !== undefined && done !== "stopped" && (
        <p className="field-hint" data-testid="smart-price-ended">
          {t(ended.status === "overridden" ? "smartPrice.history.overridden" : "smartPrice.history.stopped", {
            when: formatDateTime(ended.stopped_at ?? ended.created_at, timezone, locale),
          })}
        </p>
      )}

      {active === undefined && eligibility !== "ok" && (
        <p className="notice" data-testid="smart-price-unavailable">
          {t(eligibility === "chain" ? "smartPrice.unavailable.chain" : "smartPrice.unavailable.free")}
        </p>
      )}

      {active === undefined && eligibility === "ok" && rules.data !== undefined && (
        <>
          {stage === "idle" && (
            <>
              <p className="field-hint">{t("smartPrice.intro")}</p>
              <div className="actions">
                <button type="button" className="btn" disabled={busy} onClick={() => { reset(); setStage("form"); }} data-testid="smart-price-open">
                  {t("smartPrice.open")}
                </button>
              </div>
            </>
          )}
          {(stage === "form" || stage === "preview") && (
            <form onSubmit={onPreview} noValidate data-testid="smart-price-form">
              <fieldset disabled={busy}>
                <legend>{t("smartPrice.kind.label")}</legend>
                {kinds.map((k) => (
                  <label key={k} className="radio">
                    <input type="radio" name="smart-kind" value={k} checked={form.kind === k} onChange={() => setKind(k)} data-testid={`smart-kind-${k}`} />
                    {t(`smartPrice.kind.${k}`)}
                  </label>
                ))}
                <TextField
                  label={t("smartPrice.step.label")}
                  hint={t("smartPrice.step.hint", { currency: tier.currency })}
                  error={errors.step !== undefined ? t(errors.step.key, errors.step.params) : undefined}
                  value={form.step}
                  onChange={(v) => { setForm({ ...form, step: v }); setTable(null); setStage("form"); }}
                  testId="smart-step"
                />
                <TextField
                  label={t(form.kind === "by_sales" ? "smartPrice.every.tickets" : form.kind === "by_days" ? "smartPrice.every.days" : "smartPrice.every.before")}
                  error={errors.every !== undefined ? t(errors.every.key, errors.every.params) : undefined}
                  value={form.every}
                  onChange={(v) => { setForm({ ...form, every: v }); setTable(null); setStage("form"); }}
                  testId="smart-every"
                />
                {form.kind !== "last_days" && (
                  <TextField
                    label={t("smartPrice.cap.label")}
                    hint={t("smartPrice.cap.hint")}
                    error={errors.cap !== undefined ? t(errors.cap.key, errors.cap.params) : undefined}
                    value={form.cap}
                    onChange={(v) => { setForm({ ...form, cap: v }); setTable(null); setStage("form"); }}
                    testId="smart-cap"
                  />
                )}
                {form.kind === "by_days" && timezone !== null && (
                  <DateField
                    value={form.start}
                    onChange={(day) => { setForm({ ...form, start: day }); setTable(null); setStage("form"); }}
                    timezone={timezone}
                    label={t("smartPrice.start.label")}
                  />
                )}
                <div className="actions">
                  <button type="submit" className="btn btn-primary" data-testid="smart-price-preview">
                    {t("smartPrice.preview")}
                  </button>
                  <button type="button" className="btn" onClick={() => { reset(); setStage("idle"); }}>
                    {t("smartPrice.cancel")}
                  </button>
                </div>
              </fieldset>
            </form>
          )}
          {stage === "preview" && table !== null && (
            <div data-testid="smart-price-table">
              {table.categories.map((c) => (
                <table key={c.tier_id} className="table">
                  <thead>
                    <tr>
                      <th scope="col">{t("smartPrice.table.step")}</th>
                      <th scope="col">{t(table.kind === "by_sales" ? "smartPrice.table.tickets" : "smartPrice.table.from")}</th>
                      <th scope="col">{t("smartPrice.table.price")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {table.kind !== "by_sales" && (
                      <tr>
                        <td>1</td>
                        <td>{t("smartPrice.table.now")}</td>
                        <td>{money(c.base_price)}</td>
                      </tr>
                    )}
                    {c.steps.map((s, i) => (
                      <tr key={i} data-testid="smart-price-row">
                        <td>{table.kind === "by_sales" ? i + 1 : i + 2}</td>
                        <td>
                          {table.kind === "by_sales"
                            ? i === c.steps.length - 1
                              ? t("smartPrice.table.rest", { count: formatNumber(s.places ?? 0, locale) })
                              : formatNumber(s.places ?? 0, locale)
                            : formatDateTime(s.from ?? "", timezone, locale)}
                        </td>
                        <td>{money(s.price)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ))}
              <p className="field-hint">{t("smartPrice.enableHint")}</p>
              <p className="field-hint">{t("smartPrice.buyerNote")}</p>
              <div className="actions">
                <button type="button" className="btn btn-primary" disabled={busy} onClick={onEnable} data-testid="smart-price-enable">
                  {t("smartPrice.enable")}
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
