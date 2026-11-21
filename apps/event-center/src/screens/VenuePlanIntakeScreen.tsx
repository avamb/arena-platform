/**
 * /o/{org}/venues/{venue}/plan-intakes/{intake}: one seating plan upload (spec 64
 * sections 2.2 to 2.4). It shows the panel of the intake's state and asks the
 * server again while the intake waits. Opened with ?event=<id>&session=<id>
 * (from a date) the next step after the save goes back to that date.
 * The screen is keyed by organization and intake: another intake never shows
 * the answers of the one before.
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { ErrorBox, Loading } from "@/components/ui";
import { IntakeReview } from "@/components/venuePlan/IntakeReview";
import { IntakeStatus } from "@/components/venuePlan/IntakeStatus";
import { CancelIntake } from "@/components/venuePlan/CancelIntake";
import { PlanUpload } from "@/components/venuePlan/PlanUpload";
import { SplitSeatsLink } from "@/components/venuePlan/SplitSeatsLink";
import { getVenuePlanIntake } from "@/lib/api/endpoints";
import type { VenuePlanIntake } from "@/lib/api/types";
import { wizardGate } from "@/lib/features";
import { useI18n } from "@/lib/i18n";
import { actionsOf, isStaleIntakeError, nextStepOf, panelOf, parseIntakeSearch, planIntakeKey, pollDelayMs, type IntakeSearch } from "@/lib/venuePlan/intakeState";
import { useOrg } from "@/screens/OrgLayout";

export { planIntakeKey };

export function SavedPanel({ orgId, intake, search, canSplit }: { orgId: string; intake: VenuePlanIntake; search: IntakeSearch; canSplit: boolean }) {
  const { t } = useI18n();
  const { access } = useOrg();
  const seats = intake.draft?.summary.seats;
  const next = nextStepOf(search);
  return (
    <div className="notice notice-ok" role="status" data-testid="intake-saved">
      <h2>{t("venuePlan.saved.title")}</h2>
      <p data-testid="intake-saved-body">{seats !== undefined ? t("venuePlan.saved.body", { seats }) : t("venuePlan.saved.bodyNoCount")}</p>
      {canSplit && next.kind === "split" && (
        <p>
          <SplitSeatsLink orgId={orgId} eventId={next.eventId} sessionId={next.sessionId}>
            {t("venuePlan.saved.split")}
          </SplitSeatsLink>
        </p>
      )}
      {canSplit && next.kind === "createEvent" && (
        <>
          <p data-testid="intake-saved-no-event">{t("venuePlan.saved.noEvent")}</p>
          {wizardGate(access.can) === "ok" && (
            <p>
              <Link to="/o/$orgId/events/new" params={{ orgId }} className="btn btn-primary" data-testid="intake-create-event">
                {t("venuePlan.saved.createEvent")}
              </Link>
            </p>
          )}
        </>
      )}
      <p>
        <Link to="/o/$orgId/venues/$venueId" params={{ orgId, venueId: intake.venue_id }} data-testid="intake-to-venue">
          {t("venuePlan.saved.toVenue")}
        </Link>
      </p>
    </div>
  );
}

function IntakeBody({ orgId, intakeId, canCreate, search }: { orgId: string; intakeId: string; canCreate: boolean; search: IntakeSearch }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const client = useQueryClient();
  const [another, setAnother] = useState(false);
  const key = planIntakeKey(orgId, intakeId);
  const q = useQuery({
    queryKey: key,
    queryFn: () => getVenuePlanIntake(orgId, intakeId),
    retry: false,
    refetchInterval: (query) => pollDelayMs(query.state.data?.intake.state, query.state.fetchFailureCount),
  });

  if (q.isLoading) return <Loading />;
  if (q.data === undefined) return <ErrorBox error={q.error} onRetry={() => void q.refetch()} />;
  const intake = q.data.intake;
  const panel = panelOf(intake.state);
  const act = actionsOf(intake.state, { create: canCreate, read: true });

  const changed = (next: VenuePlanIntake) => {
    client.setQueryData(key, { intake: next });
    // The plans and the intakes of the venue changed with it.
    void client.invalidateQueries({ queryKey: ["venuePlan", "intakes", orgId] });
    void client.invalidateQueries({ queryKey: ["venuePlan", "previews", orgId] });
    void client.invalidateQueries({ queryKey: ["venueCard"] });
    void client.invalidateQueries({ queryKey: ["wizard", "venuePlans"] });
  };

  return (
    <div data-testid="venue-plan-intake" data-state={intake.state}>
      <p>
        <Link to="/o/$orgId/venues/$venueId" params={{ orgId, venueId: intake.venue_id }} data-testid="intake-back">
          {t("venuePlan.screen.back")}
        </Link>
      </p>
      <p data-testid="intake-venue">{t("venuePlan.screen.for", { venue: intake.venue_name })}</p>
      {q.isError && <ErrorBox error={q.error} onRetry={() => void q.refetch()} />}

      {panel === "review" && <IntakeReview orgId={orgId} intake={intake} canCreate={canCreate} onChanged={changed} />}
      {panel === "saved" && <SavedPanel orgId={orgId} intake={intake} search={search} canSplit={act.nextStep} />}
      {panel !== "review" && panel !== "saved" && <IntakeStatus intake={intake} />}

      {(panel === "waitingGenerator" || panel === "waitingOperator") && act.cancel && (
        <div className="actions">
          <CancelIntake
            orgId={orgId}
            intakeId={intake.id}
            onChanged={changed}
            onFailed={(error) => {
              if (isStaleIntakeError(error)) void client.invalidateQueries({ queryKey: key });
            }}
          />
        </div>
      )}
      {act.sendAnother && (
        <div>
          {!another ? (
            <div className="actions">
              <button type="button" className="btn btn-primary" onClick={() => setAnother(true)} data-testid="intake-send-another">
                {panel === "rejected" ? t("venuePlan.rejected.another") : t("venuePlan.upload.button")}
              </button>
            </div>
          ) : (
            <PlanUpload
              orgId={orgId}
              venueId={intake.venue_id}
              onSent={(sent) => {
                void client.invalidateQueries({ queryKey: ["venuePlan", "intakes", orgId] });
                void navigate({
                  to: "/o/$orgId/venues/$venueId/plan-intakes/$intakeId",
                  params: { orgId, venueId: sent.venue_id, intakeId: sent.id },
                });
              }}
              onClose={() => setAnother(false)}
            />
          )}
        </div>
      )}
    </div>
  );
}

export function VenuePlanIntakeScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const { intakeId } = useParams({ strict: false }) as { intakeId?: string };
  const search = parseIntakeSearch(useSearch({ strict: false }) as Record<string, unknown>);

  if (!access.can("seating_plan.read") || intakeId === undefined || intakeId === "") {
    return (
      <section>
        <h1>{t("venuePlan.screen.title")}</h1>
        <p className="notice notice-bad" role="status" data-testid="intake-no-access">
          {t("venuePlan.screen.noAccess")}
        </p>
      </section>
    );
  }
  return (
    <section data-testid="venue-plan-intake-screen">
      <h1>{t("venuePlan.screen.title")}</h1>
      <IntakeBody key={`${orgId}:${intakeId}`} orgId={orgId} intakeId={intakeId} canCreate={access.can("seating_plan.create")} search={search} />
    </section>
  );
}
