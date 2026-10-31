/**
 * Marks an invitation of the list that came from a CSV upload (INV-06b) and
 * offers the result file of that upload. The list needs `complimentary.read`,
 * which is also what the result file needs.
 */
import { useState } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { Badge, saveFile } from "@/components/ui";
import { downloadInvitationBatchResult } from "@/lib/api/endpoints";
import { useI18n } from "@/lib/i18n";

export function BatchMark({ orgId, batchId, systemId }: { orgId: string; batchId: string; systemId: number }) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<unknown>(null);
  const download = async () => {
    setBusy(true);
    setFailed(null);
    try {
      saveFile(await downloadInvitationBatchResult(orgId, batchId));
    } catch (err) {
      setFailed(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <span className="inv-batch-mark" data-testid="inv-batch" data-batch={batchId}>
      <Badge tone="neutral">{t("invBatch.list.fromFile")}</Badge>{" "}
      <button
        type="button"
        className="btn"
        disabled={busy}
        aria-label={t("invBatch.list.resultFile.aria", { number: systemId })}
        onClick={() => void download()}
        data-testid="inv-batch-result"
      >
        {t("invBatch.list.resultFile")}
      </button>
      {failed !== null && <ErrorNotice error={failed} testId="inv-batch-error" />}
    </span>
  );
}
