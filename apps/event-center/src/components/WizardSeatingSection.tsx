/**
 * The section "Hall plan" of the wizard (spec 40 §5.S, S3a; lib/wizard/seating.ts).
 * Shown once an existing venue is chosen. It reads the venue's plans (their
 * current versions) and offers "sell by this plan" or "without a plan" (free
 * seating). A venue without a plan is said so plainly, with the way out:
 * another venue, or free seating. A chosen plan brings its categories to
 * price; extra price categories (name, price, colour) can be added over them
 * and get their seats on the seat map after the save.
 *
 * Nothing is written here: the bundle carries the plan (bundle.ts).
 */
import { useId } from "react";
import { useQuery } from "@tanstack/react-query";
import { TextField } from "@/components/FormField";
import { listSeatingPlanVersions, listVenueSeatingPlans } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { makeId } from "@/lib/wizard/ids";
import type { DraftAction, WizardDraft } from "@/lib/wizard/draft";
import {
  MAX_SEAT_CATEGORIES,
  SEAT_COLORS,
  planChoiceOf,
  seatCatKey,
  seatExtraKey,
  type PlanChoice,
} from "@/lib/wizard/seating";
import { useOrg } from "@/screens/OrgLayout";

/** The plans the venue offers: the organization's own active ones, at their current version. */
export async function readVenuePlans(venueId: string, orgId: string): Promise<PlanChoice[]> {
  const { seating_plans: plans } = await listVenueSeatingPlans(venueId);
  const out: PlanChoice[] = [];
  for (const p of plans ?? []) {
    if (p.owner_org_id !== orgId || p.status !== "active" || !p.current_version_id) continue;
    const { seating_plan_versions: versions } = await listSeatingPlanVersions(p.id);
    const choice = planChoiceOf(p, versions ?? [], orgId);
    if (choice !== null) out.push(choice);
  }
  return out;
}

export interface SeatingSectionProps {
  draft: WizardDraft;
  dispatch: (a: DraftAction) => void;
  touch: (key: string) => void;
  /** The text under a field of the section; undefined = nothing to show. */
  errorOf: (key: string) => string | undefined;
}

export function WizardSeatingSection({ draft, dispatch, touch, errorOf }: SeatingSectionProps) {
  const { t } = useI18n();
  const ids = useId();
  const { orgId, access } = useOrg();
  const v = draft.venue;
  const s = draft.seating;
  const venueId = v.mode === "existing" ? v.existingId : "";
  const canRead = access.can("seating_plan.read");
  const plans = useQuery({
    queryKey: ["wizard", "venuePlans", orgId, venueId],
    queryFn: () => readVenuePlans(venueId, orgId),
    enabled: venueId !== "" && canRead,
    staleTime: 60_000,
    retry: false,
  });
  if (venueId === "" || !canRead) return null;
  const list = plans.data ?? [];
  const cur = draft.currency.trim().toUpperCase();
  const total = s.categories.length + s.extras.length;

  return (
    <fieldset className="org-form" data-testid="wizard-seating">
      <legend>{t("wizard.section.seating")}</legend>
      {plans.isLoading && (
        <p className="field-hint" data-testid="wizard-seating-loading">
          {t("wizard.seating.loading")}
        </p>
      )}
      {plans.isError && (
        <div className="notice notice-bad" role="alert" data-testid="wizard-seating-failed">
          <p>{t("wizard.seating.failed")}</p>
          <button type="button" className="btn" onClick={() => void plans.refetch()}>
            {t("wizard.check.again")}
          </button>
        </div>
      )}
      {plans.isSuccess && list.length === 0 && (
        <p className="notice" role="status" data-testid="wizard-seating-none">
          {t("wizard.seating.none", { venue: v.existingName })}
        </p>
      )}
      {plans.isSuccess && list.length > 0 && (
        <div role="radiogroup" aria-label={t("wizard.section.seating")}>
          {list.map((p) => (
            <label key={p.versionId} className="check">
              <input
                type="radio"
                name={`${ids}-plan`}
                checked={s.versionId === p.versionId}
                onChange={() => dispatch({ type: "seatingPlan", plan: p })}
                data-testid={`wizard-seating-plan-${p.versionId}`}
              />
              <span>{t("wizard.seating.plan", { name: p.name, seats: p.seats })}</span>
            </label>
          ))}
          <label className="check">
            <input
              type="radio"
              name={`${ids}-plan`}
              checked={s.versionId === ""}
              onChange={() => dispatch({ type: "seatingPlan", plan: null })}
              data-testid="wizard-seating-plan-none"
            />
            <span>{t("wizard.seating.noPlan")}</span>
          </label>
        </div>
      )}

      {s.versionId !== "" && (
        <div data-testid="wizard-seating-categories">
          {s.categories.length === 1 && s.extras.length === 0 && (
            <p className="field-hint" data-testid="wizard-seating-one">
              {t("wizard.seating.oneCategory")}
            </p>
          )}
          {s.categories.map((c) => (
            <TextField
              key={c.index}
              label={t("wizard.seating.catPrice", { name: c.name, places: c.places, currency: cur === "" ? "?" : cur })}
              value={c.price}
              inputMode="decimal"
              onChange={(value) => {
                touch(seatCatKey(c.index));
                dispatch({ type: "seatingPrice", index: c.index, value });
              }}
              onBlur={() => touch(seatCatKey(c.index))}
              error={errorOf(seatCatKey(c.index))}
              testId={`wizard-seating-cat-${c.index}-price`}
            />
          ))}

          {s.extras.map((e, i) => (
            <fieldset key={e.key} className="wizard-date" data-testid={`wizard-seating-extra-${i}`}>
              <legend>{t("wizard.seating.extraTitle", { n: i + 1 })}</legend>
              <TextField
                label={t("wizard.seating.extraName")}
                value={e.name}
                onChange={(value) => {
                  touch(seatExtraKey(e.key, "name"));
                  dispatch({ type: "seatingExtra", key: e.key, field: "name", value });
                }}
                onBlur={() => touch(seatExtraKey(e.key, "name"))}
                error={errorOf(seatExtraKey(e.key, "name"))}
                testId={`wizard-seating-extra-${i}-name`}
              />
              <TextField
                label={t("wizard.seating.extraPrice", { currency: cur === "" ? "?" : cur })}
                value={e.price}
                inputMode="decimal"
                onChange={(value) => {
                  touch(seatExtraKey(e.key, "price"));
                  dispatch({ type: "seatingExtra", key: e.key, field: "price", value });
                }}
                onBlur={() => touch(seatExtraKey(e.key, "price"))}
                error={errorOf(seatExtraKey(e.key, "price"))}
                testId={`wizard-seating-extra-${i}-price`}
              />
              <div role="radiogroup" aria-label={t("wizard.seating.extraColor")} className="wizard-colors">
                <span>{t("wizard.seating.extraColor")}</span>
                {SEAT_COLORS.map((color) => (
                  <label key={color} className="wizard-color" title={color}>
                    <input
                      type="radio"
                      name={`${ids}-color-${e.key}`}
                      checked={e.color === color}
                      onChange={() => dispatch({ type: "seatingExtra", key: e.key, field: "color", value: color })}
                      aria-label={color}
                      data-testid={`wizard-seating-extra-${i}-color-${color.slice(1)}`}
                    />
                    <span className="wizard-color-swatch" ref={(el) => el?.style.setProperty("background-color", color)} />
                  </label>
                ))}
              </div>
              <div className="actions">
                <button
                  type="button"
                  className="btn"
                  onClick={() => dispatch({ type: "seatingExtraRemove", key: e.key })}
                  data-testid={`wizard-seating-extra-${i}-remove`}
                >
                  {t("wizard.seating.extraRemove")}
                </button>
              </div>
            </fieldset>
          ))}

          {total < MAX_SEAT_CATEGORIES ? (
            <div className="actions">
              <button type="button" className="btn" onClick={() => dispatch({ type: "seatingExtraAdd", key: makeId() })} data-testid="wizard-seating-extra-add">
                {t("wizard.seating.extraAdd")}
              </button>
            </div>
          ) : (
            <p className="field-hint">{t("wizard.seating.limit", { max: MAX_SEAT_CATEGORIES })}</p>
          )}
          <p className="field-hint" data-testid="wizard-seating-paint">
            {t("wizard.seating.paintAfter")}
          </p>
        </div>
      )}
    </fieldset>
  );
}
