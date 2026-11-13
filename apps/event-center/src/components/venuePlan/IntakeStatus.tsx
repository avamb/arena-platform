/**
 * The panels of an intake that wait for something or ended without a plan
 * (spec 64 sections 2.2 and 2.5): "we are building it, up to 5 minutes", "an
 * administrator builds it, within one working day", "we could not build it",
 * "you canceled it". The buttons of a panel belong to the screen.
 */
import type { VenuePlanIntake } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { operatorWait, panelOf } from "@/lib/venuePlan/intakeState";

export type IntakeStatusFields = Pick<VenuePlanIntake, "state" | "escalation_reason" | "reject_reason" | "sources">;

export function IntakeStatus({ intake }: { intake: IntakeStatusFields }) {
  const { t } = useI18n();
  const panel = panelOf(intake.state);
  const files = <p className="muted">{t("venuePlan.wait.files", { count: intake.sources.length })}</p>;

  switch (panel) {
    case "waitingGenerator":
      return (
        <div className="notice" role="status" data-testid="intake-waiting-generator">
          <h2>{t("venuePlan.wait.generator.title")}</h2>
          <p>{t("venuePlan.wait.generator.body")}</p>
          {files}
        </div>
      );
    case "waitingOperator":
      return (
        <div className="notice" role="status" data-testid="intake-waiting-operator">
          <h2>{t("venuePlan.wait.operator.title")}</h2>
          <p>{t(operatorWait(intake.escalation_reason) === "request" ? "venuePlan.wait.operator.request" : "venuePlan.wait.operator.auto")}</p>
          {files}
        </div>
      );
    case "rejected": {
      const reason = intake.reject_reason?.trim() ?? "";
      return (
        <div className="notice notice-bad" role="status" data-testid="intake-rejected">
          <h2>{t("venuePlan.rejected.title")}</h2>
          <p data-testid="intake-rejected-body">{reason !== "" ? t("venuePlan.rejected.body", { reason }) : t("venuePlan.rejected.noReason")}</p>
        </div>
      );
    }
    case "cancelled":
      return (
        <div className="notice" role="status" data-testid="intake-cancelled">
          <h2>{t("venuePlan.cancelled.title")}</h2>
          <p>{t("venuePlan.cancelled.body")}</p>
        </div>
      );
    case "unknown":
      return (
        <p className="notice notice-warn" role="status" data-testid="intake-unknown">
          {t("venuePlan.screen.unknown")}
        </p>
      );
    default:
      return null;
  }
}
