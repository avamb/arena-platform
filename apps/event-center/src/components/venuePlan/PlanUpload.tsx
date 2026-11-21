/**
 * The upload of a seating plan (spec 64 section 2.1): a file picker, the list of
 * files, the checks before any request (lib/venuePlan/upload.ts), "Send the
 * files" and the note that says what the files are used for.
 *
 * One press sends ONE request. The Idempotency-Key belongs to the attempt: when
 * the answer is lost, the next press sends the same key and the server makes no
 * second intake.
 */
import { useRef, useState, type ChangeEvent } from "react";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ApiError } from "@/lib/api/http";
import { createVenuePlanIntake } from "@/lib/api/endpoints";
import type { VenuePlanIntake } from "@/lib/api/types";
import { useI18n } from "@/lib/i18n";
import { makeId } from "@/lib/wizard/ids";
import {
  ACCEPT,
  HEAD_BYTES,
  MAX_FILES,
  MAX_TOTAL_MB,
  PLAN_TERMS_URL,
  TAIL_BYTES,
  addCandidates,
  attemptFor,
  keepKeyAfter,
  problemName,
  problemText,
  sizeLabel,
  uploadSignature,
  type Entry,
  type UploadAttempt,
  type UploadProblem,
} from "@/lib/venuePlan/upload";

export interface PlanUploadProps {
  orgId: string;
  venueId: string;
  /** The intake the server made (or made before, for the same attempt). */
  onSent: (intake: VenuePlanIntake) => void;
  /** Shows "Close" when given. */
  onClose?: () => void;
  testId?: string;
}

/** The first and the last bytes of a file: what the kind is read from. */
async function bytesOf(file: File): Promise<{ head: Uint8Array; tail?: Uint8Array }> {
  const head = new Uint8Array(await file.slice(0, HEAD_BYTES).arrayBuffer());
  if (file.size <= HEAD_BYTES) return { head };
  return { head, tail: new Uint8Array(await file.slice(Math.max(HEAD_BYTES, file.size - TAIL_BYTES)).arrayBuffer()) };
}

export function PlanUpload({ orgId, venueId, onSent, onClose, testId = "plan-upload" }: PlanUploadProps) {
  const { t } = useI18n();
  const [entries, setEntries] = useState<Entry<File>[]>([]);
  const [problems, setProblems] = useState<UploadProblem[]>([]);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<{ error: unknown } | null>(null);
  const [lost, setLost] = useState(false);
  const attempt = useRef<UploadAttempt | null>(null);
  const sending = useRef(false);
  const input = useRef<HTMLInputElement>(null);

  const pick = async (e: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    e.target.value = "";
    if (files.length === 0) return;
    const readable: Array<{ name: string; size: number; head: Uint8Array; tail?: Uint8Array; ref: File }> = [];
    const unreadable: UploadProblem[] = [];
    for (const f of files) {
      try {
        readable.push({ name: f.name, size: f.size, ...(await bytesOf(f)), ref: f });
      } catch {
        unreadable.push({ kind: "unreadable", name: f.name });
      }
    }
    const out = addCandidates(entries, readable);
    setEntries(out.entries);
    setProblems([...unreadable, ...out.problems]);
    setFailure(null);
    setLost(false);
  };

  const send = async () => {
    if (sending.current || entries.length === 0) return;
    sending.current = true;
    setBusy(true);
    setFailure(null);
    const files = entries.map((x) => x.ref);
    attempt.current = attemptFor(attempt.current, uploadSignature(venueId, files), makeId);
    try {
      const res = await createVenuePlanIntake(orgId, venueId, files, attempt.current.key);
      attempt.current = null;
      setEntries([]);
      setProblems([]);
      setLost(false);
      onSent(res.intake);
    } catch (error) {
      const status = error instanceof ApiError ? error.status : 0;
      const keep = keepKeyAfter(status);
      if (!keep) attempt.current = null;
      setLost(keep);
      setFailure({ error });
    } finally {
      sending.current = false;
      setBusy(false);
    }
  };

  const remove = (i: number) => {
    setEntries((list) => list.filter((_, at) => at !== i));
    setProblems([]);
    setFailure(null);
    setLost(false);
  };

  return (
    <section className="plan-upload" aria-labelledby={`${testId}-title`} data-testid={testId}>
      <h3 id={`${testId}-title`}>{t("venuePlan.upload.title")}</h3>
      <p>{t("venuePlan.upload.intro")}</p>
      <p className="field-hint">{t("venuePlan.upload.limits", { files: MAX_FILES, total: MAX_TOTAL_MB })}</p>

      <label htmlFor={`${testId}-input`}>{t("venuePlan.upload.pick")}</label>
      <input
        id={`${testId}-input`}
        ref={input}
        type="file"
        multiple
        accept={ACCEPT}
        disabled={busy}
        onChange={(e) => void pick(e)}
        data-testid={`${testId}-input`}
      />

      {problems.length > 0 && (
        <div className="notice notice-bad" role="alert" data-testid={`${testId}-problems`}>
          <ul>
            {problems.map((p, i) => {
              const text = problemText(p);
              const name = problemName(p);
              return (
                <li key={`${p.kind}-${name ?? i}-${i}`}>
                  {name !== null && <strong>{name} </strong>}
                  {t(text.key, text.params)}
                </li>
              );
            })}
          </ul>
        </div>
      )}

      {entries.length === 0 ? (
        <p className="muted" data-testid={`${testId}-none`}>
          {t("venuePlan.upload.none")}
        </p>
      ) : (
        <ul className="plan-upload-list" data-testid={`${testId}-list`}>
          {entries.map((e, i) => (
            <li key={`${e.name}-${e.size}-${i}`} data-testid={`${testId}-file-${i}`}>
              <span className="plan-upload-file">{e.name}</span>
              <span className="muted">{t("venuePlan.source.meta", { kind: e.kind.toUpperCase(), size: sizeLabel(e.size) })}</span>
              <button type="button" className="btn btn-link" disabled={busy} onClick={() => remove(i)} aria-label={t("venuePlan.upload.removeFile", { name: e.name })} data-testid={`${testId}-remove-${i}`}>
                {t("venuePlan.upload.remove")}
              </button>
            </li>
          ))}
        </ul>
      )}

      {failure !== null && <ErrorNotice error={failure.error} testId={`${testId}-error`} />}
      {lost && (
        <p className="notice notice-warn" role="status" data-testid={`${testId}-retry`}>
          {t("venuePlan.upload.retry")}
        </p>
      )}

      <div className="actions">
        <button type="button" className="btn btn-primary" disabled={busy || entries.length === 0} onClick={() => void send()} data-testid={`${testId}-send`}>
          {busy ? t("venuePlan.upload.sending") : t("venuePlan.upload.send")}
        </button>
        {onClose !== undefined && (
          <button type="button" className="btn" disabled={busy} onClick={onClose} data-testid={`${testId}-close`}>
            {t("venuePlan.upload.hide")}
          </button>
        )}
      </div>

      <p className="field-hint" data-testid={`${testId}-note`}>
        {t("venuePlan.upload.note")}{" "}
        <a href={PLAN_TERMS_URL} target="_blank" rel="noopener noreferrer" data-testid={`${testId}-terms`}>
          {t("venuePlan.upload.terms")}
        </a>
      </p>
    </section>
  );
}
