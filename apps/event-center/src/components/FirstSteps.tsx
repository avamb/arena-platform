/**
 * "First steps": the card at the top of the events list (the home). The
 * server lists only the steps this person may do (`GET .../getting-started`)
 * and says which are done. A done step is plain text with a check mark, an
 * open step is a link to the screen that does it. When the last step is done
 * the card is gone, and a failed read shows nothing: the card is a courtesy.
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { fetchGettingStarted } from "@/lib/api/endpoints";
import type { OrgAccess } from "@/lib/auth";
import { wizardGate } from "@/lib/features";
import type { GettingStarted, GettingStartedStep } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";

type StepKey = GettingStartedStep["key"];

const LABELS: Record<StepKey, MessageKey> = {
  org_data: "firstSteps.orgData",
  payments: "firstSteps.payments",
  event: "firstSteps.event",
  sales_link: "firstSteps.salesLink",
};

/** Whether the card shows: a read that worked, steps to show and not all of them done. */
export function firstStepsShown(data: GettingStarted | undefined): data is GettingStarted {
  return data !== undefined && data.steps.length > 0 && !data.complete;
}

function StepLink({ orgId, step, children }: { orgId: string; step: StepKey; children: string }) {
  switch (step) {
    case "org_data":
      return (
        <Link to="/o/$orgId/org-data" params={{ orgId }} data-testid="first-step-org_data">
          {children}
        </Link>
      );
    case "payments":
      return (
        <Link to="/o/$orgId/org-data" params={{ orgId }} hash="payments" data-testid="first-step-payments">
          {children}
        </Link>
      );
    case "event":
      return (
        <Link to="/o/$orgId/events/new" params={{ orgId }} data-testid="first-step-event">
          {children}
        </Link>
      );
    case "sales_link":
      return (
        <Link to="/o/$orgId/links/new" params={{ orgId }} search={{}} data-testid="first-step-sales_link">
          {children}
        </Link>
      );
  }
}

export function FirstSteps({ orgId, access }: { orgId: string; access: Pick<OrgAccess, "can"> }) {
  const { t } = useI18n();
  const canAddEvent = wizardGate(access.can) === "ok";
  const steps = useQuery({
    queryKey: ["gettingStarted", orgId],
    queryFn: () => fetchGettingStarted(orgId),
    retry: false,
    staleTime: 0,
  });
  const data = steps.data;
  if (!firstStepsShown(data)) return null;
  const done = data.steps.filter((s) => s.done).length;
  return (
    <section className="card first-steps" aria-labelledby="first-steps-title" data-testid="first-steps">
      <h2 id="first-steps-title">{t("firstSteps.title")}</h2>
      <p className="muted" data-testid="first-steps-progress">
        {t("firstSteps.progress", { done, total: data.steps.length })}
      </p>
      <ul className="plain-list first-steps-list">
        {data.steps.map((s) => (
          <li key={s.key} data-testid={`first-step-row-${s.key}`} data-done={s.done ? "true" : "false"}>
            {s.done ? (
              <span>
                <span aria-hidden="true">✓ </span>
                <span className="visually-hidden">{t("firstSteps.stepDone")}: </span>
                {t(LABELS[s.key])}
              </span>
            ) : s.key === "event" && !canAddEvent ? (
              <span>{t(LABELS[s.key])}</span>
            ) : (
              <StepLink orgId={orgId} step={s.key}>
                {t(LABELS[s.key])}
              </StepLink>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}
