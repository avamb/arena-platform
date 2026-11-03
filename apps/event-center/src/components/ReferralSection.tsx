/**
 * "Refer a friend" on the event card and on the shop settings (spec 52, sections 0 and 7.1). The same
 * section serves both: `target` says whether the programme belongs to one event or to the organizer's shop. A buyer who paid gets a personal
 * code. A friend who uses it gets a discount, and the buyer gets a percent of what the friend paid back
 * on their own card, at once and without anyone confirming each payout.
 *
 * Who sees what: `promo.read` sees the state and the results. Starting, changing, pausing and ending
 * need `promo.update` and both refund rights (the organizer allows the cashback refunds in their own
 * name); the owner and the manager hold them, an agent does not.
 *
 * Starting a programme asks for the rules first (ConfirmPressDialog with one tick): the cashback is
 * paid at once and is not taken back when the friend later returns the ticket, a card that cannot take
 * the money leaves it with the organizer, the provider's fee is not returned, and payouts are
 * automatic. The tick is sent as `accept_terms: true`. If no sales channel of the event can return a
 * part of a payment the form stays closed and says why.
 */
import { useEffect, useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { DateField } from "@/components/DateField";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";
import { ReferralResults } from "@/components/ReferralResults";
import { Badge, ErrorBox, Loading } from "@/components/ui";
import { ApiError } from "@/lib/api/http";
import {
  fetchReferralProgram,
  referralAction,
  referralTargetKey,
  saveReferralProgram,
  type ReferralAction,
  type ReferralState,
  type ReferralTarget,
} from "@/lib/api/endpoints";
import { formatDateTime } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import {
  buildReferralRequest,
  capabilityReasonKey,
  capabilityVerdict,
  draftFromProgram,
  endsDayOf,
  endsInstantOf,
  isDirty,
  isStarting,
  phaseKey,
  referralText,
  programActions,
  programPhase,
  warningKey,
  type BuildResult,
  type ReferralDraft,
  type ReferralField,
} from "@/lib/referral";
import type { ReferralProgram } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";

export interface ReferralSectionProps {
  orgId: string;
  /** The event, or the shop (one programme per organization). */
  target: ReferralTarget;
  /** `promo.update` + `refund.create` + `refund.approve` (canManageReferral). */
  canManage: boolean;
  /** The venue's time zone: the last day is a day of the venue. UTC when missing. */
  timezone?: string;
}

type Busy = ReferralAction | "save" | null;

const RULES: readonly MessageKey[] = ["referral.rules.payout", "referral.rules.cardClosed", "referral.rules.fee", "referral.rules.automatic"];

export function ReferralSection({ orgId, target, canManage, timezone }: ReferralSectionProps) {
  const { t: translate, locale } = useI18n();
  // The words that differ for the shop are told for it; the rest are shared.
  const t = (key: MessageKey, vars?: Parameters<typeof translate>[1]) => translate(referralText(target, key), vars);
  const targetId = referralTargetKey(target);
  const ids = useId();
  const queryClient = useQueryClient();
  const key = ["referral", orgId, targetId] as const;
  const state = useQuery({ queryKey: key, queryFn: () => fetchReferralProgram(orgId, target) });

  const program: ReferralProgram | null = state.data?.program ?? null;
  const stamp = program === null ? "" : `${program.id}:${program.updated_at}`;
  const [draft, setDraft] = useState<ReferralDraft>(() => draftFromProgram(program));
  // The form follows the stored programme while the person has typed nothing since (another tab, the bot).
  const [synced, setSynced] = useState<{ stamp: string; program: ReferralProgram | null }>({ stamp, program });
  if (stamp !== synced.stamp) {
    if (!isDirty(draft, synced.program)) setDraft(draftFromProgram(program));
    setSynced({ stamp, program });
  }

  const [busy, setBusy] = useState<Busy>(null);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [showErrors, setShowErrors] = useState(false);
  const [rules, setRules] = useState(false);
  const [ticked, setTicked] = useState(false);
  const [ending, setEnding] = useState(false);
  useEffect(() => {
    if (!rules) setTicked(false);
  }, [rules]);

  if (state.isLoading) return <Loading />;
  if (state.error !== null || state.data === undefined) {
    return (
      <section aria-labelledby={`${ids}-title`} data-testid="referral">
        <h2 id={`${ids}-title`}>{t("referral.title")}</h2>
        <ErrorBox error={state.error} onRetry={() => void state.refetch()} />
      </section>
    );
  }

  const { capability, warnings } = state.data;
  const verdict = capabilityVerdict(capability);
  const now = new Date();
  const starting = isStarting(program);
  const phase = program === null ? null : programPhase(program, now);
  const actions = programActions(program);
  const formOpen = canManage && (verdict.canStart || !starting);
  const dirty = isDirty(draft, program);
  const dayValue = endsDayOf(draft.endsAt, timezone);

  const judge = (accepted: boolean): BuildResult =>
    buildReferralRequest(draft, { starting, accepted, original: program, now: new Date() });
  const preview = judge(true);
  const fieldError = (f: ReferralField): string | undefined => {
    if (!showErrors || preview.ok) return undefined;
    const k = preview.errors[f];
    return k === undefined ? undefined : t(k);
  };

  const setNew = (next: ReferralState) => {
    queryClient.setQueryData(key, next);
    void queryClient.invalidateQueries({ queryKey: ["referralSummary", orgId, targetId] });
    void queryClient.invalidateQueries({ queryKey: ["referralRewards", orgId, targetId] });
  };

  const run = async (which: Busy, work: () => Promise<string>) => {
    if (busy !== null) return;
    setBusy(which);
    setFailure(null);
    setStatus(null);
    try {
      setStatus(await work());
    } catch (error) {
      setFailure({ error });
    } finally {
      setBusy(null);
    }
  };

  const submit = (accepted: boolean) =>
    run("save", async () => {
      const built = judge(accepted);
      if (!built.ok) throw new ApiError(422, "referral.invalid_value", "");
      const r = await saveReferralProgram(orgId, target, built.request);
      setNew({ program: r.program, capability: r.capability, warnings: r.warnings });
      setDraft(draftFromProgram(r.program));
      setShowErrors(false);
      return t(starting ? "referral.started" : "referral.saved");
    });

  const onSave = () => {
    setShowErrors(true);
    if (!preview.ok) return;
    if (starting) {
      setRules(true);
      return;
    }
    void submit(true);
  };

  const act = (which: ReferralAction) =>
    run(which, async () => {
      const r = await referralAction(orgId, target, which);
      setNew({ program: r.program, capability: r.capability, warnings: r.warnings });
      return t(which === "pause" ? "referral.paused" : which === "resume" ? "referral.resumed" : "referral.ended");
    });

  const confirmRules = async () => {
    if (!ticked) throw new ApiError(422, "referral.terms_not_accepted", "");
    const built = judge(true);
    if (!built.ok) throw new ApiError(422, "referral.invalid_value", "");
    const r = await saveReferralProgram(orgId, target, built.request);
    setNew({ program: r.program, capability: r.capability, warnings: r.warnings });
    setDraft(draftFromProgram(r.program));
    setShowErrors(false);
    setStatus(t("referral.started"));
    setRules(false);
  };

  const confirmEnd = async () => {
    const r = await referralAction(orgId, target, "end");
    setNew({ program: r.program, capability: r.capability, warnings: r.warnings });
    setStatus(t("referral.ended"));
    setEnding(false);
  };

  return (
    <section aria-labelledby={`${ids}-title`} data-testid="referral" data-target={target.kind}>
      <h2 id={`${ids}-title`}>{t("referral.title")}</h2>
      <p>{t("referral.intro")}</p>

      {program === null ? (
        <p className="muted" data-testid="referral-none">
          {t("referral.none")}
        </p>
      ) : (
        <div data-testid="referral-state">
          <p>
            <Badge tone={phase === "active" ? "ok" : phase === "paused" || phase === "scheduled" ? "wait" : "neutral"}>
              <span data-testid="referral-status">{t(phaseKey(phase as NonNullable<typeof phase>))}</span>
            </Badge>{" "}
            <span data-testid="referral-terms">
              {t("referral.line", {
                friend: program.friend_discount_pct,
                cashback: program.cashback_pct,
                cap: program.cashback_cap_pct,
              })}
            </span>
          </p>
          <p className="muted small">
            {program.max_friends === null || program.max_friends === undefined
              ? t("referral.maxFriends.none")
              : t("referral.maxFriends.some", { count: program.max_friends })}
            {" · "}
            {program.ends_at === null || program.ends_at === undefined
              ? t("referral.ends.sales")
              : t("referral.ends.at", { time: formatDateTime(program.ends_at, timezone, locale) })}
          </p>
          {program.status === "ended" && <p className="muted">{t("referral.endedFinal")}</p>}
        </div>
      )}

      {!verdict.canStart && (
        <div className="notice notice-bad" role="note" data-testid="referral-blocked">
          <p>
            <strong>{t("referral.blocked.title")}</strong>
          </p>
          <p>{t("referral.blocked.next")}</p>
          <ul>
            {verdict.blocked.map((c) => (
              <li key={c.channel_id} data-testid="referral-blocked-channel">
                {c.provider === "" ? t("referral.channel.none") : c.provider}: {t(capabilityReasonKey(c.reason))}
              </li>
            ))}
          </ul>
        </div>
      )}
      {warnings.map((w) => {
        const k = warningKey(w, target);
        return (
          <p key={w.code} className="notice notice-warn" role="note" data-testid="referral-warning" data-code={w.code}>
            {k === null ? w.message : t(k)}
          </p>
        );
      })}
      {verdict.partial && (
        <ul className="muted small" data-testid="referral-partial">
          {verdict.blocked.map((c) => (
            <li key={c.channel_id}>
              {c.provider === "" ? t("referral.channel.none") : c.provider}: {t(capabilityReasonKey(c.reason))}
            </li>
          ))}
        </ul>
      )}

      {!canManage && (
        <p className="muted" data-testid="referral-readonly">
          {t("referral.readOnly")}
        </p>
      )}

      {formOpen && (
        <form
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            onSave();
          }}
          data-testid="referral-form"
        >
          <h3>{t(starting ? "referral.form.start" : "referral.form.change")}</h3>
          <TextField
            label={t("referral.f.friend")}
            hint={t("referral.f.friend.hint")}
            error={fieldError("friend")}
            inputMode="numeric"
            value={draft.friend}
            onChange={(v) => setDraft({ ...draft, friend: v })}
            testId="referral-friend"
          />
          <TextField
            label={t("referral.f.cashback")}
            hint={t("referral.f.cashback.hint")}
            error={fieldError("cashback")}
            inputMode="numeric"
            value={draft.cashback}
            onChange={(v) => setDraft({ ...draft, cashback: v })}
            testId="referral-cashback"
          />
          <TextField
            label={t("referral.f.cap")}
            hint={t("referral.f.cap.hint")}
            error={fieldError("cap")}
            inputMode="numeric"
            value={draft.cap}
            onChange={(v) => setDraft({ ...draft, cap: v })}
            testId="referral-cap"
          />
          <TextField
            label={t("referral.f.max")}
            hint={t("referral.f.max.hint")}
            error={fieldError("max")}
            inputMode="numeric"
            value={draft.max}
            onChange={(v) => setDraft({ ...draft, max: v })}
            testId="referral-max"
          />
          <div data-testid="referral-ends">
            <DateField
              label={t("referral.f.ends")}
              value={dayValue}
              timezone={timezone}
              onChange={(iso) => setDraft({ ...draft, endsAt: endsInstantOf(iso, timezone) })}
              disabled={busy !== null}
            />
            <p className="field-hint">
              {draft.endsAt === null
                ? t("referral.f.ends.none")
                : dayValue === null
                  ? t("referral.ends.at", { time: formatDateTime(draft.endsAt, timezone, locale) })
                  : t("referral.f.ends.hint")}
            </p>
            {fieldError("ends") !== undefined && (
              <p className="field-error" data-testid="referral-ends-error">
                {fieldError("ends")}
              </p>
            )}
            {draft.endsAt !== null && (
              <p>
                <button type="button" className="btn" onClick={() => setDraft({ ...draft, endsAt: null })} data-testid="referral-ends-clear">
                  {t("referral.f.ends.clear")}
                </button>
              </p>
            )}
          </div>
          {!starting && <p className="field-hint">{t("referral.f.newCodesOnly")}</p>}
          <div className="actions">
            <button
              type="submit"
              className="btn btn-primary"
              disabled={busy !== null || (!starting && !dirty)}
              data-testid="referral-save"
            >
              {busy === "save" ? t("referral.saving") : t(starting ? "referral.start" : "referral.save")}
            </button>
          </div>
        </form>
      )}

      {canManage && actions.length > 0 && (
        <div className="actions" data-testid="referral-actions">
          {actions.includes("pause") && (
            <button type="button" className="btn" disabled={busy !== null} onClick={() => void act("pause")} data-testid="referral-pause">
              {busy === "pause" ? t("referral.working") : t("referral.pause")}
            </button>
          )}
          {actions.includes("resume") && (
            <button type="button" className="btn" disabled={busy !== null} onClick={() => void act("resume")} data-testid="referral-resume">
              {busy === "resume" ? t("referral.working") : t("referral.resume")}
            </button>
          )}
          {actions.includes("end") && (
            <button type="button" className="btn" disabled={busy !== null} onClick={() => setEnding(true)} data-testid="referral-end">
              {t("referral.end")}
            </button>
          )}
        </div>
      )}

      {status !== null && (
        <p role="status" className="notice" data-testid="referral-status-note">
          {status}
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure.error} testId="referral-error" />}

      {rules && (
        <ConfirmPressDialog
          testId="referral-rules"
          title={t("referral.rules.title")}
          explain={t("referral.rules.intro")}
          confirmLabel={t("referral.rules.confirm")}
          onConfirm={confirmRules}
          onCancel={() => setRules(false)}
        >
          <ul data-testid="referral-rules-list">
            {RULES.map((k) => (
              <li key={k}>{t(k)}</li>
            ))}
          </ul>
          <label className="check">
            <input type="checkbox" checked={ticked} onChange={(e) => setTicked(e.target.checked)} data-testid="referral-rules-tick" />
            {t("referral.rules.tick")}
          </label>
        </ConfirmPressDialog>
      )}
      {ending && (
        <ConfirmPressDialog
          testId="referral-end-dialog"
          title={t("referral.end.title")}
          explain={t("referral.end.explain")}
          confirmLabel={t("referral.end.confirm")}
          onConfirm={confirmEnd}
          onCancel={() => setEnding(false)}
        />
      )}

      {program !== null && (
        <ReferralResults orgId={orgId} target={target} timezone={timezone} />
      )}
    </section>
  );
}
