/**
 * Bulk staff invitations from a CSV file (spec 46, 3.5 and 4.1, task INV-06b):
 * choose a file, check it (a dry run that writes nothing), read the check table,
 * press "Issue", download the result file.
 *
 * The issue is ONE request carrying a request id. The id lives for one pick of
 * a file and one delivery: while the request is open the form is locked, and
 * after a dropped connection, a timeout or a 5xx the only way on is Retry with
 * the same file, delivery and id, which the server answers with the first batch
 * if it had committed. A definite refusal (a 4xx) closes the attempt.
 */
import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ErrorNotice } from "@/components/ErrorNotice";
import { ChargeText } from "@/components/staffInvitations/ChargeText";
import { CsvReportView } from "@/components/staffInvitations/CsvReportView";
import { saveFile } from "@/components/ui";
import { ApiError } from "@/lib/api/http";
import { createInvitationBatch, downloadInvitationBatchResult } from "@/lib/api/endpoints";
import type { InvitationBatch, InvitationBatchReport } from "@/lib/api/types";
import { formatNumber } from "@/lib/format";
import { useI18n, type MessageKey } from "@/lib/i18n";
import {
  BATCH_DELIVERIES,
  CSV_COLUMNS,
  DEFAULT_BATCH_DELIVERY,
  EXAMPLE_FILENAME,
  batchAttemptFor,
  batchError,
  canIssue,
  csvErrorLine,
  exampleCsv,
  fileProblem,
  isReport,
  reportOfError,
  type BatchAttempt,
  type FileProblem,
} from "@/lib/invitationBatch";
import { STAFF_INVITATIONS_KEY, isDefinitiveRefusal, type Delivery } from "@/lib/staffInvitations";

interface Props {
  orgId: string;
  eventId: string;
  sessionId: string;
  /** `complimentary.read`: the result file and the batch's orders can be read. */
  canRead: boolean;
}

const FILE_PROBLEM_KEY: Record<FileProblem, MessageKey> = {
  empty: "invBatch.file.empty",
  tooLarge: "invBatch.file.tooLarge",
  spreadsheet: "invBatch.file.spreadsheet",
};

const DELIVERY_KEY: Record<Delivery, MessageKey> = {
  pdf_only: "invBatch.delivery.pdf_only",
  my_email: "invBatch.delivery.my_email",
  guest_email: "invBatch.delivery.guest_email",
};

interface Picked {
  file: File;
  /** Counts the choosings: choosing the same file again is a new pick (the person may have edited it). */
  pick: number;
}

export function CsvUpload({ orgId, eventId, sessionId, canRead }: Props) {
  const { t, locale } = useI18n();
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const picks = useRef(0);

  const [picked, setPicked] = useState<Picked | null>(null);
  const [delivery, setDelivery] = useState<Delivery>(DEFAULT_BATCH_DELIVERY);
  const [report, setReport] = useState<InvitationBatchReport | null>(null);
  // The real run was refused because the world changed under a file that had passed the check (409).
  const [changed, setChanged] = useState(false);
  const [batch, setBatch] = useState<InvitationBatch | null>(null);
  const [discarded, setDiscarded] = useState(false);
  const attempt = useRef<BatchAttempt | null>(null);
  const [locked, setLocked] = useState(false);

  const problem = picked !== null ? fileProblem(picked.file) : null;

  const refreshLists = () => {
    void qc.invalidateQueries({ queryKey: [STAFF_INVITATIONS_KEY, orgId, sessionId] });
    void qc.invalidateQueries({ queryKey: ["seats-admin", sessionId] });
    void qc.invalidateQueries({ queryKey: ["tiers", orgId, eventId, sessionId] });
    void qc.invalidateQueries({ queryKey: ["sessionSummary", orgId, sessionId] });
  };

  // ── check (dry run): writes nothing, so it can simply be run again ──────
  const check = useMutation({
    mutationFn: () => {
      if (picked === null) throw new Error("no file");
      return createInvitationBatch(orgId, sessionId, { file: picked.file, fileName: picked.file.name, delivery, dryRun: true });
    },
    onSuccess: (res) => {
      setReport(isReport(res) ? res : null);
    },
    onError: () => setReport(null),
  });

  // ── issue ───────────────────────────────────────────────────────────────
  const issue = useMutation({
    mutationFn: () => {
      if (picked === null) throw new Error("no file");
      // The first send fixes the id, a retry sends the same file and delivery under it again.
      attempt.current = batchAttemptFor(attempt.current, picked.pick, delivery);
      return createInvitationBatch(orgId, sessionId, {
        file: picked.file,
        fileName: picked.file.name,
        delivery,
        dryRun: false,
        requestId: attempt.current.id,
      });
    },
    onSuccess: (res) => {
      attempt.current = null;
      setLocked(false);
      if (isReport(res)) return; // a real run never answers a report
      setBatch(res);
      setReport(null);
      setChanged(false);
      refreshLists();
    },
    onError: (err) => {
      // A definite answer (a 4xx) closes the attempt. A dropped connection, a timeout or a 5xx may have been
      // processed: the attempt stays open, locked, and the only way on is the same request again.
      if (!isDefinitiveRefusal(err)) return;
      attempt.current = null;
      setLocked(false);
      const refused = reportOfError(err);
      if (refused !== null) {
        setChanged(err instanceof ApiError && err.status === 409);
        // A refusal with no row to point at leaves nothing to fix: check the file again.
        setReport(refused.errors.length > 0 ? refused : null);
      }
    },
  });

  const resetResults = () => {
    check.reset();
    issue.reset();
    setReport(null);
    setChanged(false);
    setDiscarded(false);
  };

  const onPick = (file: File) => {
    if (locked) return;
    picks.current += 1;
    setPicked({ file, pick: picks.current });
    resetResults();
  };
  const onDelivery = (d: Delivery) => {
    if (locked) return;
    setDelivery(d);
    resetResults();
  };

  const runCheck = () => {
    if (picked === null || problem !== null || check.isPending || locked) return;
    setDiscarded(false);
    issue.reset();
    setChanged(false);
    check.mutate();
  };
  const runIssue = () => {
    if (issue.isPending || picked === null) return;
    // A retry needs no new check: it is the request that was already sent.
    if (attempt.current === null && !canIssue(report)) return;
    setLocked(true);
    setDiscarded(false);
    issue.mutate();
  };
  const discard = () => {
    attempt.current = null;
    setLocked(false);
    issue.reset();
    setDiscarded(true);
    refreshLists();
  };
  const another = () => {
    attempt.current = null;
    setLocked(false);
    setBatch(null);
    setPicked(null);
    setDelivery(DEFAULT_BATCH_DELIVERY);
    resetResults();
  };

  const stalled = locked && !issue.isPending && issue.isError;
  const n = (v: number) => formatNumber(v, locale);

  if (batch !== null) {
    return <BatchResult orgId={orgId} batch={batch} canRead={canRead} onAnother={another} />;
  }

  const issueError = issue.error !== null && reportOfError(issue.error) === null ? issue.error : null;
  const shownError = check.error ?? issueError;
  const line = shownError !== null ? csvErrorLine(shownError) : null;

  return (
    <section className="inv" aria-labelledby="csv-title" data-testid="csv-upload">
      <h2 id="csv-title">{t("invBatch.title")}</h2>
      <p className="muted">{t("invBatch.intro")}</p>

      <fieldset className="inv-fieldset" disabled={locked} data-testid="csv-fields">
        <div className="actions">
          <input
            ref={input}
            type="file"
            hidden
            accept=".csv,.txt,text/csv,text/plain"
            data-testid="csv-input"
            onChange={(e) => {
              const file = e.currentTarget.files?.[0];
              // The same file can be chosen again after it was edited.
              e.currentTarget.value = "";
              if (file !== undefined) onPick(file);
            }}
          />
          <button type="button" className="btn" onClick={() => input.current?.click()} data-testid="csv-pick">
            {picked === null ? t("invBatch.file.choose") : t("invBatch.file.change")}
          </button>
          <button type="button" className="btn" onClick={() => saveFile({ blob: new Blob([exampleCsv()], { type: "text/csv;charset=utf-8" }), filename: EXAMPLE_FILENAME })} data-testid="csv-example">
            {t("invBatch.example")}
          </button>
        </div>
        <p className="muted" aria-live="polite" data-testid="csv-file-name">
          {picked === null ? t("invBatch.file.none") : t("invBatch.file.name", { name: picked.file.name, size: n(Math.max(1, Math.round(picked.file.size / 1024))) })}
        </p>
        {problem !== null && (
          <p className="field-error" role="alert" data-testid="csv-file-problem">
            {t(FILE_PROBLEM_KEY[problem])}
          </p>
        )}

        <details className="inv-help" data-testid="csv-help">
          <summary>{t("invBatch.help.title")}</summary>
          <p>{t("invBatch.help.intro")}</p>
          <p>{t("invBatch.help.places")}</p>
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{t("invBatch.help.col")}</th>
                  <th scope="col">{t("invBatch.help.what")}</th>
                </tr>
              </thead>
              <tbody>
                {CSV_COLUMNS.map((c) => (
                  <tr key={c.name}>
                    <td data-label={t("invBatch.help.col")}>
                      <code>{c.name}</code>
                    </td>
                    <td data-label={t("invBatch.help.what")}>
                      {t(`invBatch.help.col.${c.name}` as MessageKey)}
                      {c.required && ` ${t("invBatch.help.required")}.`}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </details>

        <fieldset className="inv-delivery" data-testid="csv-delivery">
          <legend>{t("invBatch.delivery.title")}</legend>
          {BATCH_DELIVERIES.map((d) => (
            <label key={d} className="inv-radio">
              <input type="radio" name="csv-delivery" value={d} checked={delivery === d} onChange={() => onDelivery(d)} data-testid={`csv-delivery-${d}`} />
              <span>{t(DELIVERY_KEY[d])}</span>
            </label>
          ))}
          <p className="muted">{t("invBatch.delivery.hint")}</p>
        </fieldset>

        <button type="button" className="btn" disabled={picked === null || problem !== null || check.isPending} onClick={runCheck} data-testid="csv-check">
          {check.isPending ? t("invBatch.check.busy") : t("invBatch.check")}
        </button>
      </fieldset>

      {shownError !== null && (
        <>
          <ErrorNotice error={batchError(shownError)} testId="csv-error" />
          {line !== null && (
            <p className="muted" data-testid="csv-error-line">
              {t("invBatch.csvLine", { line })}
            </p>
          )}
        </>
      )}
      {discarded && (
        <p className="notice" role="status" data-testid="csv-discarded">
          {t("invBatch.discarded")}
        </p>
      )}
      {changed && (
        <div className="notice notice-bad" role="alert" data-testid="csv-changed">
          <p>
            <strong>{t("invBatch.changed.title")}</strong>
          </p>
          <p>{t("invBatch.changed.body")}</p>
        </div>
      )}

      {report !== null && <CsvReportView report={report} />}

      {stalled ? (
        <div className="notice notice-warn" role="alert" data-testid="csv-stalled">
          <p>
            <strong>{t("invBatch.retry.title")}</strong>
          </p>
          <p>{t("invBatch.retry.body")}</p>
          <button type="button" className="btn btn-primary" onClick={runIssue} data-testid="csv-retry">
            {t("invBatch.retry.button")}
          </button>{" "}
          <button type="button" className="btn" onClick={discard} data-testid="csv-discard">
            {t("invBatch.retry.discard")}
          </button>
        </div>
      ) : (
        canIssue(report) &&
        report !== null && (
          <div>
            <p className="muted">{t("invBatch.issue.note")}</p>
            <button type="button" className="btn btn-primary" disabled={issue.isPending} onClick={runIssue} data-testid="csv-issue">
              {issue.isPending ? t("invBatch.issue.busy") : t("invBatch.issue", { count: n(report.guests) })}
            </button>
          </div>
        )
      )}
    </section>
  );
}

/** What the upload made: the orders, the billing, the link to the result file. */
function BatchResult({ orgId, batch, canRead, onAnother }: { orgId: string; batch: InvitationBatch; canRead: boolean; onAnother: () => void }) {
  const { t, locale } = useI18n();
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<unknown>(null);
  const n = (v: number) => formatNumber(v, locale);

  const download = async () => {
    setBusy(true);
    setFailed(null);
    try {
      saveFile(await downloadInvitationBatchResult(orgId, batch.id));
    } catch (err) {
      setFailed(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="inv" aria-labelledby="csv-title" data-testid="csv-upload">
      <h2 id="csv-title">{t("invBatch.title")}</h2>
      <div className="notice notice-ok" role="status" data-testid="csv-result" data-batch={batch.id}>
        <p>
          <strong>{t("invBatch.result.title", { guests: n(batch.guests) })}</strong>
        </p>
        <p>{t("invBatch.result.places", { count: n(batch.places) })}</p>
        {batch.from_reserve > 0 && <p>{t("invBatch.result.fromReserve", { count: n(batch.from_reserve) })}</p>}
        {batch.replayed && <p data-testid="csv-replayed">{t("invBatch.result.replayed")}</p>}
        <ChargeText charge={batch.charge} />
        <p>{t("seatInv.result.issuing")}</p>
        <p className="inv-row-actions">
          {canRead && (
            <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void download()} data-testid="csv-result-download">
              {busy ? t("invBatch.result.download.busy") : t("invBatch.result.download")}
            </button>
          )}
          <button type="button" className="btn" onClick={onAnother} data-testid="csv-another">
            {t("invBatch.result.another")}
          </button>
        </p>
        {failed !== null && <ErrorNotice error={failed} testId="csv-result-error" />}
      </div>
      {batch.orders.length > 0 && (
        <div className="table-wrap">
          <table className="table" data-testid="csv-result-orders">
            <caption>{t("invBatch.result.orders")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("invBatch.report.col.guest")}</th>
                <th scope="col">{t("invBatch.report.col.name")}</th>
                <th scope="col">{t("invBatch.result.col.order")}</th>
                <th scope="col">{t("invBatch.report.col.places")}</th>
              </tr>
            </thead>
            <tbody>
              {batch.orders.map((o) => (
                <tr key={o.order_id} data-testid="csv-result-order" data-order={o.order_id}>
                  <td data-label={t("invBatch.report.col.guest")}>{o.guest}</td>
                  <td data-label={t("invBatch.report.col.name")}>{o.guest_name ?? "—"}</td>
                  <td data-label={t("invBatch.result.col.order")}>{o.system_id}</td>
                  <td data-label={t("invBatch.report.col.places")}>{n(o.places)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
