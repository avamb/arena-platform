/** "Cancel the upload": a button and one confirm press (the intake is closed, the files stay private). */
import { useState } from "react";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { cancelPlanIntake } from "@/lib/api/endpoints";
import type { VenuePlanIntake } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";

export function CancelIntake({
  orgId,
  intakeId,
  onChanged,
  onFailed,
}: {
  orgId: string;
  intakeId: string;
  onChanged: (intake: VenuePlanIntake) => void;
  /** Called with the error before it shows in the dialog (the screen reads the intake again on a 409). */
  onFailed?: (error: unknown) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" className="btn" onClick={() => setOpen(true)} data-testid="intake-cancel">
        {t("venuePlan.review.cancel")}
      </button>
      {open && (
        <ConfirmPressDialog
          testId="intake-cancel-dialog"
          title={t("venuePlan.review.cancel.title")}
          explain={t("venuePlan.review.cancel.explain")}
          confirmLabel={t("venuePlan.review.cancel.confirm")}
          onConfirm={async () => {
            let res;
            try {
              res = await cancelPlanIntake(orgId, intakeId);
            } catch (error) {
              onFailed?.(error);
              throw error;
            }
            setOpen(false);
            onChanged(res.intake);
          }}
          onCancel={() => setOpen(false)}
        />
      )}
    </>
  );
}
