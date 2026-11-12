/**
 * The review of a draft (spec 64 section 2.3): the draft large with pan and
 * zoom, what it holds, the files the person sent beside it to compare, the box
 * "Write what to fix" and the buttons "The plan is correct", "Send a correction", "Ask an
 * administrator" and "Cancel the upload". The rules of the buttons are
 * lib/venuePlan/intakeState.ts.
 */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { CancelIntake } from "@/components/venuePlan/CancelIntake";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { TextAreaField } from "@/components/FormField";
import { DownloadButton } from "@/components/ui";
import { PanZoom } from "@/components/venuePlan/PanZoom";
import { asSvg, useBlobUrl } from "@/components/venuePlan/useBlobUrl";
import {
  approvePlanIntake,
  correctPlanIntakeText,
  downloadPlanDraftSvg,
  downloadPlanSource,
  escalatePlanIntake,
} from "@/lib/api/endpoints";
import type { VenuePlanDraft, VenuePlanIntake, VenuePlanIntakeSource } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import {
  ESCALATE_TEXT_MAX,
  FIX_TEXT_MAX,
  actionsOf,
  escalateTextProblem,
  fixTextProblem,
  isStaleIntakeError,
  planIntakeKey,
  sourceViewOf,
  suggestAdministrator,
  warningKey,
} from "@/lib/venuePlan/intakeState";
import { sizeLabel } from "@/lib/venuePlan/upload";

type Dialog = "approve" | "escalate" | null;

function SourceCard({ orgId, intakeId, source }: { orgId: string; intakeId: string; source: VenuePlanIntakeSource }) {
  const { t } = useI18n();
  const view = sourceViewOf(source);
  const blob = useBlobUrl(
    `${intakeId}:${source.id}:${view}`,
    view === "download"
      ? null
      : async () => {
          const file = await downloadPlanSource(orgId, intakeId, source.id, source.file_name, view === "png_copy");
          return source.kind === "svg" && view === "file" ? asSvg(file.blob) : file.blob;
        },
  );
  return (
    <li data-testid={`intake-source-${source.id}`}>
      {blob.url !== null && <img className="plan-source-img" src={blob.url} alt={t("venuePlan.review.sourceAlt", { name: source.file_name })} data-testid={`intake-source-img-${source.id}`} />}
      {blob.failed && (
        <p className="notice notice-warn" role="status">
          {t("venuePlan.review.sourceFailed")}
        </p>
      )}
      <p>
        <strong>{source.file_name}</strong> <span className="muted">{t("venuePlan.source.meta", { kind: source.kind.toUpperCase(), size: sizeLabel(source.byte_size) })}</span>
      </p>
      <DownloadButton
        label="venuePlan.source.download"
        fetchFile={() => downloadPlanSource(orgId, intakeId, source.id, source.file_name)}
        testId={`intake-source-download-${source.id}`}
      />
    </li>
  );
}

function DraftView({ orgId, intakeId, draft }: { orgId: string; intakeId: string; draft: VenuePlanDraft }) {
  const { t } = useI18n();
  const blob = useBlobUrl(`${intakeId}:${draft.n}`, async () => asSvg((await downloadPlanDraftSvg(orgId, intakeId, draft.n)).blob));
  if (blob.failed) {
    return (
      <p className="notice notice-bad" role="alert" data-testid="intake-draft-failed">
        {t("venuePlan.review.draftFailed")}
      </p>
    );
  }
  return (
    <PanZoom testId="intake-draft">
      {blob.url !== null && <img src={blob.url} alt={t("venuePlan.review.draftAlt", { n: draft.n })} data-testid="intake-draft-img" />}
    </PanZoom>
  );
}

export interface IntakeReviewProps {
  orgId: string;
  intake: VenuePlanIntake;
  /** seating_plan.create: the buttons. Without it the person only looks. */
  canCreate: boolean;
  /** The server's new card after an action. */
  onChanged: (intake: VenuePlanIntake) => void;
}

export function IntakeReview({ orgId, intake, canCreate, onChanged }: IntakeReviewProps) {
  const { t } = useI18n();
  const [dialog, setDialog] = useState<Dialog>(null);
  const [fix, setFix] = useState("");
  const [fixShown, setFixShown] = useState(false);
  const [note, setNote] = useState("");
  const [sending, setSending] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const client = useQueryClient();
  /** The server moved the intake on (409): the error shows AND the screen reads the intake again. */
  const refreshIfStale = (error: unknown) => {
    if (isStaleIntakeError(error)) void client.invalidateQueries({ queryKey: planIntakeKey(orgId, intake.id) });
  };
  const draft = intake.draft ?? null;
  const act = actionsOf(intake.state, { create: canCreate, read: true });
  const fixProblem = fix === "" ? null : fixTextProblem(fix);

  if (draft === null) {
    return (
      <p className="notice notice-warn" role="status" data-testid="intake-no-draft">
        {t("venuePlan.review.noDraft")}
      </p>
    );
  }
  const s = draft.summary;
  const returned = (intake.return_note ?? "").trim();

  const sendFix = async () => {
    setFixShown(true);
    if (fixTextProblem(fix) !== null) return;
    setSending(true);
    setFailure(null);
    try {
      const res = await correctPlanIntakeText(orgId, intake.id, draft.n, fix.trim());
      setFix("");
      onChanged(res.intake);
    } catch (error) {
      setFailure({ error });
      refreshIfStale(error);
    } finally {
      setSending(false);
    }
  };

  return (
    <div data-testid="intake-review">
      <h2>{t("venuePlan.review.title")}</h2>
      <p data-testid="intake-ready">{t("venuePlan.review.ready", { venue: intake.venue_name, seats: s.seats, rows: s.rows })}</p>
      {returned !== "" && (
        <p className="notice" role="status" data-testid="intake-returned">
          {t("venuePlan.review.returned", { venue: intake.venue_name, note: returned })}
        </p>
      )}
      <dl className="plan-stats" data-testid="intake-stats">
        <div>
          <dt>{t("venuePlan.review.stat.seats")}</dt>
          <dd data-testid="intake-stat-seats">{s.seats}</dd>
        </div>
        <div>
          <dt>{t("venuePlan.review.stat.rows")}</dt>
          <dd data-testid="intake-stat-rows">{s.rows}</dd>
        </div>
        <div>
          <dt>{t("venuePlan.review.stat.sections")}</dt>
          <dd data-testid="intake-stat-sections">{s.sections}</dd>
        </div>
      </dl>
      {s.warnings.length > 0 && (
        <div className="notice notice-warn" data-testid="intake-warnings">
          <p>
            <strong>{t("venuePlan.review.warnings")}</strong>
          </p>
          <ul>
            {s.warnings.map((code) => (
              <li key={code}>{t(warningKey(code), { code })}</li>
            ))}
          </ul>
        </div>
      )}

      <div className="plan-compare">
        <DraftView orgId={orgId} intakeId={intake.id} draft={draft} />
        <div>
          <h3>{t("venuePlan.review.sources")}</h3>
          <ul className="plan-sources" data-testid="intake-sources">
            {intake.sources.map((src) => (
              <SourceCard key={src.id} orgId={orgId} intakeId={intake.id} source={src} />
            ))}
          </ul>
        </div>
      </div>

      {!act.approve && (
        <p className="notice" role="status" data-testid="intake-read-only">
          {t("venuePlan.review.readOnly")}
        </p>
      )}

      {act.fix && (
        <div className="org-form">
          <TextAreaField
            label={t("venuePlan.review.fix.label")}
            hint={t("venuePlan.review.fix.hint")}
            value={fix}
            onChange={(v) => {
              setFix(v);
              setFixShown(false);
            }}
            maxLength={FIX_TEXT_MAX + 200}
            error={
              fixProblem === "tooLong"
                ? t("venuePlan.review.fix.tooLong")
                : fixShown && fixTextProblem(fix) === "empty"
                  ? t("venuePlan.review.fix.empty")
                  : undefined
            }
            testId="intake-fix-text"
          />
        </div>
      )}

      {suggestAdministrator(intake.state, intake.review_rounds) && act.escalate && (
        <p className="notice notice-warn" role="status" data-testid="intake-many-rounds">
          {t("venuePlan.review.manyRounds")}
        </p>
      )}
      {failure !== null && <ErrorNotice error={failure.error} testId="intake-error" />}

      <div className="actions">
        {act.approve && (
          <button type="button" className="btn btn-primary" onClick={() => setDialog("approve")} data-testid="intake-approve">
            {t("venuePlan.review.approve")}
          </button>
        )}
        {act.fix && (
          <button type="button" className="btn" disabled={sending} onClick={() => void sendFix()} data-testid="intake-fix">
            {t("venuePlan.review.fix.send")}
          </button>
        )}
        {act.escalate && (
          <button type="button" className="btn" onClick={() => setDialog("escalate")} data-testid="intake-escalate">
            {t("venuePlan.review.escalate")}
          </button>
        )}
        {act.cancel && <CancelIntake orgId={orgId} intakeId={intake.id} onChanged={onChanged} onFailed={refreshIfStale} />}
      </div>

      {dialog === "approve" && (
        <ConfirmPressDialog
          testId="intake-approve-dialog"
          title={t("venuePlan.review.approve.title")}
          explain={t("venuePlan.review.approve.explain", { seats: s.seats })}
          confirmLabel={t("venuePlan.review.approve.confirm")}
          onConfirm={async () => {
            let res;
            try {
              res = await approvePlanIntake(orgId, intake.id, draft.n);
            } catch (error) {
              refreshIfStale(error);
              throw error;
            }
            setDialog(null);
            onChanged(res.intake);
          }}
          onCancel={() => setDialog(null)}
        />
      )}
      {dialog === "escalate" && (
        <ConfirmPressDialog
          testId="intake-escalate-dialog"
          title={t("venuePlan.review.escalate.title")}
          explain={t("venuePlan.review.escalate.explain")}
          confirmLabel={t("venuePlan.review.escalate")}
          onConfirm={async () => {
            if (escalateTextProblem(note) !== null) throw new Error("note too long");
            let res;
            try {
              res = await escalatePlanIntake(orgId, intake.id, note);
            } catch (error) {
              refreshIfStale(error);
              throw error;
            }
            setDialog(null);
            setNote("");
            onChanged(res.intake);
          }}
          onCancel={() => setDialog(null)}
        >
          <TextAreaField
            label={t("venuePlan.review.escalate.note")}
            value={note}
            onChange={setNote}
            maxLength={ESCALATE_TEXT_MAX}
            testId="intake-escalate-text"
          />
        </ConfirmPressDialog>
      )}
    </div>
  );
}
