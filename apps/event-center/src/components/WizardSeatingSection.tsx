/**
 * The section "Hall plan" of the wizard (spec 40 §5.S, S3a; lib/wizard/seating.ts).
 * Shown once an existing venue is chosen. It reads the venue's plans (their
 * current versions) and offers "sell by this plan" or "without a plan" (free
 * seating). A venue without a plan is said so plainly. A person who may create
 * seating plans (spec 64) gets the way forward: copy the verified plan of the
 * venue when there is one, or upload their own files, or continue without a
 * plan (lib/venuePlan/wizardFlow.ts). A person who may not gets the old text:
 * another venue, or free seating. The state of a running upload of the venue
 * shows with a link to its page, and the plans are read again when an upload
 * becomes approved. A chosen plan brings its categories to
 * price; extra price categories (name, price, colour) can be added over them
 * and get their seats on the seat map after the save.
 *
 * Nothing is written here: the bundle carries the plan (bundle.ts).
 */
import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextField } from "@/components/FormField";
import { PlanUpload } from "@/components/venuePlan/PlanUpload";
import { ApiError } from "@/lib/api/http";
import { forkSeatingPlan, listPlanTemplates, listSeatingPlanVersions, listVenuePlanIntakes, listVenueSeatingPlans } from "@/lib/api/endpoints";
import type { PlanTemplate } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { pollDelayMs, runningIntake, stateKey } from "@/lib/venuePlan/intakeState";
import { NO_PLAN, approvedKey, noPlanView, reduceNoPlan, templateToOffer, type NoPlanAction } from "@/lib/venuePlan/wizardFlow";
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
  const client = useQueryClient();
  const canCreate = access.can("seating_plan.create");
  const canFork = access.can("seating_plan.fork");
  const noPlans = plans.isSuccess && (plans.data?.length ?? 0) === 0;
  const intakes = useQuery({
    queryKey: ["venuePlan", "intakes", orgId, venueId],
    queryFn: () => listVenuePlanIntakes(orgId, { venueId, limit: 20 }),
    enabled: venueId !== "" && canRead,
    retry: false,
    refetchInterval: (query) => pollDelayMs(runningIntake(query.state.data?.intakes ?? [])?.state, query.state.fetchFailureCount),
  });
  const templates = useQuery({
    queryKey: ["wizard", "planTemplates", orgId, venueId],
    queryFn: () => listPlanTemplates(orgId, venueId),
    enabled: venueId !== "" && canRead && canCreate && canFork && noPlans,
    retry: false,
    staleTime: 60_000,
  });
  const [flowState, setFlowState] = useState({ venue: "", s: NO_PLAN });
  const flow = flowState.venue === venueId ? flowState.s : NO_PLAN;
  const step = (a: NoPlanAction) => setFlowState({ venue: venueId, s: reduceNoPlan(flow, a) });
  const [forking, setForking] = useState(false);
  const [forkFailure, setForkFailure] = useState<{ error: unknown } | null>(null);
  // The copy was made, but it cannot be selected yet: the offer stays hidden, a second copy is never made.
  const [copyNote, setCopyNote] = useState<{ venue: string; kind: "noVersion" | "listFailed" } | null>(null);
  // The venue the person looks at now: an answer that comes after a change of venue is dropped.
  const venueNow = useRef(venueId);
  venueNow.current = venueId;

  // An upload of this venue became approved: read the plans again, so the new plan can be chosen.
  const approved = approvedKey(intakes.data?.intakes ?? []);
  const seen = useRef<{ venue: string; key: string } | null>(null);
  useEffect(() => {
    if (!intakes.isSuccess) return;
    const before = seen.current;
    seen.current = { venue: venueId, key: approved };
    if (before !== null && before.venue === venueId && before.key !== approved) void plans.refetch();
    // `plans` is a new object on every render: the key of the approved intakes says when to read.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [approved, intakes.isSuccess, venueId]);

  if (venueId === "" || !canRead) return null;
  const list = plans.data ?? [];
  const note = copyNote !== null && copyNote.venue === venueId ? copyNote.kind : null;
  const offer: PlanTemplate | null = canCreate && canFork && note === null ? templateToOffer(templates.data?.plan_templates) : null;
  const view = noPlanView(flow, offer !== null, templates.isLoading);
  const running = runningIntake(intakes.data?.intakes ?? []);

  /**
   * Copies the verified plan and selects the copy. The copy is taken from the ANSWER of the
   * copy request (its plan id and current version), never searched for in a list. After a
   * successful copy no second copy is ever made from here.
   */
  const useTemplate = async (tpl: PlanTemplate) => {
    const forVenue = venueId;
    const key = ["wizard", "venuePlans", orgId, forVenue];
    const here = () => venueNow.current === forVenue;
    setForking(true);
    setForkFailure(null);
    let made: Awaited<ReturnType<typeof forkSeatingPlan>> | null = null;
    try {
      made = await forkSeatingPlan(tpl.plan_id, { owner_org_id: orgId, venue_id: forVenue });
      if (made.seating_plan.current_version_id === undefined || made.seating_plan.current_version_id === null || made.seating_plan.current_version_id === "") {
        if (here()) setCopyNote({ venue: forVenue, kind: "noVersion" });
        return;
      }
      const versions = (await listSeatingPlanVersions(made.seating_plan.id)).seating_plan_versions ?? [];
      const mine = planChoiceOf(made.seating_plan, versions, orgId);
      void client.invalidateQueries({ queryKey: ["venueCard"] });
      if (mine === null) {
        if (here()) setCopyNote({ venue: forVenue, kind: "noVersion" });
        return;
      }
      let fresh: PlanChoice[];
      try {
        fresh = await readVenuePlans(forVenue, orgId);
      } catch {
        fresh = [];
      }
      // The list may not hold the copy yet: the answer of the copy request wins.
      client.setQueryData(key, fresh.some((c) => c.versionId === mine.versionId) ? fresh : [...fresh, mine]);
      if (here()) dispatch({ type: "seatingPlan", plan: mine });
    } catch (error) {
      if (!here()) return;
      if (made !== null) {
        // The copy exists; only the read after it failed.
        setCopyNote({ venue: forVenue, kind: "listFailed" });
        return;
      }
      setForkFailure({ error });
      // 409: the organization already has a copy. The list shows it.
      if (error instanceof ApiError && error.code === "seating_plan.fork_own_plan") void plans.refetch();
    } finally {
      setForking(false);
    }
  };
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
      {running !== null && (
        <div className="notice" role="status" data-testid="wizard-seating-intake" data-state={running.state}>
          <p>
            {t("venuePlan.wizard.running", { state: t(stateKey(running.state)) })}{" "}
            <Link to="/o/$orgId/venues/$venueId/plan-intakes/$intakeId" params={{ orgId, venueId, intakeId: running.id }} data-testid="wizard-seating-intake-open">
              {t("venuePlan.slot.open")}
            </Link>
          </p>
          <p className="field-hint">{t("venuePlan.wizard.draftSaved")}</p>
        </div>
      )}
      {noPlans && !canCreate && (
        <p className="notice" role="status" data-testid="wizard-seating-none">
          {t("wizard.seating.none", { venue: v.existingName })}
        </p>
      )}
      {noPlans && canCreate && (
        <>
          <div className="notice" role="status" data-testid="wizard-seating-none">
            {note !== null ? (
              <div data-testid="wizard-seating-copy-note" data-kind={note}>
                <p>{note === "noVersion" ? t("venuePlan.wizard.template.noVersion") : t("venuePlan.wizard.template.copyExists")}</p>
                <button type="button" className="btn btn-primary" onClick={() => void plans.refetch()} data-testid="wizard-seating-reload">
                  {t("venuePlan.wizard.template.reload")}
                </button>
              </div>
            ) : view === "wait" ? (
              <p className="field-hint" data-testid="wizard-seating-templates-loading">
                {t("venuePlan.wizard.template.loading")}
              </p>
            ) : view === "template" && offer !== null ? (
              <p data-testid="wizard-seating-template">{t("venuePlan.wizard.template.offer", { name: offer.name, seats: offer.capacity_seated ?? 0 })}</p>
            ) : (
              <p>{view === "continued" ? t("wizard.seating.noPlan") : t("venuePlan.wizard.none", { venue: v.existingName })}</p>
            )}
            <div className="actions">
              {view === "template" && offer !== null && (
                <>
                  <button type="button" className="btn btn-primary" disabled={forking} onClick={() => void useTemplate(offer)} data-testid="wizard-seating-template-use">
                    {forking ? t("venuePlan.wizard.template.working") : t("venuePlan.wizard.template.use")}
                  </button>
                  <button type="button" className="btn" disabled={forking} onClick={() => step("declineTemplate")} data-testid="wizard-seating-template-decline">
                    {t("venuePlan.wizard.template.decline")}
                  </button>
                </>
              )}
              {view !== "wait" && view !== "template" && view !== "upload" && (
                <button type="button" className="btn btn-primary" onClick={() => step("openUpload")} data-testid="wizard-seating-upload">
                  {t("venuePlan.upload.button")}
                </button>
              )}
              {(view === "ask" || view === "upload") && (
                <button type="button" className="btn" onClick={() => step("continue")} data-testid="wizard-seating-continue">
                  {t("venuePlan.wizard.continue")}
                </button>
              )}
            </div>
            {forkFailure !== null && <ErrorNotice error={forkFailure.error} testId="wizard-seating-template-error" />}
          </div>
          {view === "upload" && (
            <PlanUpload
              orgId={orgId}
              venueId={venueId}
              testId="wizard-plan-upload"
              onClose={() => step("closeUpload")}
              onSent={() => {
                step("closeUpload");
                void intakes.refetch();
              }}
            />
          )}
        </>
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
