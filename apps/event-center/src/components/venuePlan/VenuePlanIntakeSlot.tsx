/**
 * The place on the venue card for the seating plan intake (spec 64): the state
 * of the running intake with a link to its page, the button "Upload a seating
 * plan" and small previews of the venue's plans.
 *
 * Rights: seating_plan.read shows the state and the previews, seating_plan.create
 * shows the button. A person with neither sees nothing.
 */
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Loading } from "@/components/ui";
import { PlanUpload } from "@/components/venuePlan/PlanUpload";
import { listSeatingPlanVersions, listVenuePlanIntakes, listVenueSeatingPlans } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";
import { usablePlans } from "@/lib/venues/card";
import { pollDelayMs, runningIntake, stateKey } from "@/lib/venuePlan/intakeState";
import { previewOf, type Preview } from "@/lib/venuePlan/preview";
import { useOrg } from "@/screens/OrgLayout";

export interface VenuePlanIntakeSlotProps {
  orgId: string;
  venueId: string;
  venueName: string;
}

export interface PlanPreview {
  id: string;
  name: string;
  preview: Preview;
}

async function readPreviews(venueId: string, orgId: string): Promise<PlanPreview[]> {
  const { seating_plans } = await listVenueSeatingPlans(venueId);
  const out: PlanPreview[] = [];
  for (const p of usablePlans(seating_plans ?? [], orgId)) {
    try {
      const versions = (await listSeatingPlanVersions(p.id)).seating_plan_versions ?? [];
      const current = versions.find((v) => v.id === p.current_version_id);
      const preview = current === undefined ? null : previewOf(current.geometry);
      if (preview !== null) out.push({ id: p.id, name: p.name, preview });
    } catch {
      // A plan we cannot read has no preview. The card still lists it by name.
    }
  }
  return out;
}

/** One small picture: the seats of a plan as dots. */
export function PlanPreviewImage({ preview, label, testId }: { preview: Preview; label: string; testId?: string }) {
  const v = preview.viewBox;
  return (
    <svg className="plan-preview" viewBox={`${v.x} ${v.y} ${v.w} ${v.h}`} role="img" aria-label={label} data-testid={testId}>
      {preview.dots.map((d, i) => (
        <circle key={i} className="plan-preview-dot" cx={d.x} cy={d.y} r={preview.radius} />
      ))}
    </svg>
  );
}

export function PlanPreviews({ previews }: { previews: readonly PlanPreview[] }) {
  const { t } = useI18n();
  if (previews.length === 0) return null;
  return (
    <>
      <h4>{t("venuePlan.slot.previews")}</h4>
      <ul className="plan-previews" data-testid="venue-plan-previews">
        {previews.map((p) => (
          <li key={p.id}>
            <PlanPreviewImage preview={p.preview} label={t("venuePlan.slot.previewAlt", { name: p.name })} testId={`venue-plan-preview-${p.id}`} />
          </li>
        ))}
      </ul>
    </>
  );
}

export const venueIntakesKey = (orgId: string, venueId: string) => ["venuePlan", "intakes", orgId, venueId] as const;

export function VenuePlanIntakeSlot({ orgId, venueId }: VenuePlanIntakeSlotProps) {
  const { t } = useI18n();
  const { access } = useOrg();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const canRead = access.can("seating_plan.read");
  const canCreate = access.can("seating_plan.create");

  const intakes = useQuery({
    queryKey: venueIntakesKey(orgId, venueId),
    queryFn: () => listVenuePlanIntakes(orgId, { venueId, limit: 20 }),
    enabled: canRead,
    retry: false,
    refetchInterval: (query) => pollDelayMs(runningIntake(query.state.data?.intakes ?? [])?.state, query.state.fetchFailureCount),
  });
  const previews = useQuery({
    queryKey: ["venuePlan", "previews", orgId, venueId],
    queryFn: () => readPreviews(venueId, orgId),
    enabled: canRead,
    retry: false,
    staleTime: 60_000,
  });
  if (!canRead && !canCreate) return null;

  const running = runningIntake(intakes.data?.intakes ?? []);
  return (
    <section className="venue-plan-slot" aria-labelledby="venue-plan-slot-title" data-testid="venue-plan-slot">
      <h3 id="venue-plan-slot-title">{t("venuePlan.slot.title")}</h3>
      {canRead && intakes.isLoading && <Loading />}
      {canRead && intakes.isError && (
        <p className="notice notice-bad" role="alert" data-testid="venue-plan-slot-failed">
          {t("venuePlan.slot.failed")}
        </p>
      )}
      {canRead && intakes.isSuccess && running !== null && (
        <p data-testid="venue-plan-slot-running" data-state={running.state}>
          {t("venuePlan.slot.running", { state: t(stateKey(running.state)) })}{" "}
          <Link
            to="/o/$orgId/venues/$venueId/plan-intakes/$intakeId"
            params={{ orgId, venueId, intakeId: running.id }}
            data-testid="venue-plan-slot-open"
          >
            {t("venuePlan.slot.open")}
          </Link>
        </p>
      )}
      {canRead && intakes.isSuccess && running === null && (
        <p className="muted" data-testid="venue-plan-slot-none">
          {t("venuePlan.slot.none")}
        </p>
      )}

      {canCreate && !open && (
        <div className="actions">
          <button type="button" className="btn btn-primary" onClick={() => setOpen(true)} data-testid="venue-plan-upload-button">
            {t("venuePlan.upload.button")}
          </button>
        </div>
      )}
      {canCreate && open && (
        <PlanUpload
          orgId={orgId}
          venueId={venueId}
          onClose={() => setOpen(false)}
          onSent={(sent) => {
            setOpen(false);
            void intakes.refetch();
            void navigate({ to: "/o/$orgId/venues/$venueId/plan-intakes/$intakeId", params: { orgId, venueId, intakeId: sent.id } });
          }}
        />
      )}

      {canRead && previews.isSuccess && <PlanPreviews previews={previews.data} />}
    </section>
  );
}
