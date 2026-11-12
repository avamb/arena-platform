/**
 * One seating plan intake (spec 08_architecture/64, section 3.3): /venue-plans/$intakeId.
 * The administrator notifications link here.
 *
 *   GET  /v1/admin/plan-intakes/{id}
 *   POST /v1/admin/plan-intakes/{id}/claim | drafts | return | approve | reject
 *   GET  /v1/admin/plan-intakes/{id}/sources/{source_id}
 *   GET  /v1/admin/plan-intakes/{id}/drafts/{n}/svg
 *
 * Every write and every source download carries X-Admin-Reason (the API client asks
 * for it). A draft SVG is read through the API with the bearer token and shown as an
 * <img> from a blob URL. The markup never goes into the page.
 *
 * Mock data: NONE. The card hits the live backend.
 */
import { createRoute, Link, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import {
  approveIntake,
  claimIntake,
  downloadIntakeSource,
  fetchDraftSvg,
  formatBytes,
  gateFor,
  getIntake,
  holdsClaim,
  intakeErrorMessage,
  intakeStateLabel,
  rejectIntake,
  returnIntake,
  uploadIntakeDraft,
  waitingLabel,
  isFinalState,
  type ActionGate,
  type VenuePlanIntake,
  type VenuePlanIntakeSource,
} from "@/lib/api/venuePlans";
import { getUserId } from "@/lib/api/tokenStore";
import { formatDateTime } from "@/lib/admin/supportConsole";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { stateTone } from "./venuePlans";
import {
  Dialog,
  Pill,
  buttonStyle,
  dangerButtonStyle,
  disabledButtonStyle,
  errorStyle,
  fieldErrorStyle,
  hintStyle,
  inputStyle,
  labelStyle,
  mutedStyle,
  noticeStyle,
  pageStyle,
  primaryButtonStyle,
  sectionStyle,
  sectionTitleStyle,
  tableStyle,
  tdStyle,
  thStyle,
  warningStyle,
} from "./onboardingUi";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/venue-plans/$intakeId",
  component: VenuePlanDetailRoute,
});

const VENUE_PLANS_NAV_ENTRY = NAV_BY_PATH["/venue-plans"];
if (VENUE_PLANS_NAV_ENTRY === undefined) {
  throw new Error("venue plan detail route: NAV_BY_PATH['/venue-plans'] missing");
}

function VenuePlanDetailRoute() {
  const { intakeId } = useParams({ from: "/venue-plans/$intakeId" });
  return (
    <RequirePermission entry={VENUE_PLANS_NAV_ENTRY}>
      <IntakeCard intakeId={intakeId} />
    </RequirePermission>
  );
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

export const REJECT_REASON_MAX = 1000;

/** The reason the organizer reads: 1 to 1000 characters after trimming. */
export function validateRejectReason(reason: string): string | null {
  const t = reason.trim();
  if (t === "") return "Write the reason. The organizer reads it.";
  if (t.length > REJECT_REASON_MAX) return `Keep the reason under ${REJECT_REASON_MAX} characters.`;
  return null;
}

export const DRAFT_NOTE_MAX = 2000;

export function validateDraftFile(file: File | null): string | null {
  if (file === null) return "Choose an SVG file.";
  if (!/\.svg$/i.test(file.name) && file.type !== "image/svg+xml") return "The file must be an SVG. Choose an .svg file.";
  if (file.size > 3 * 1024 * 1024) return "The file is larger than 3 MB. Make it smaller.";
  return null;
}

export function validateNote(note: string): string | null {
  return note.trim().length > DRAFT_NOTE_MAX ? `Keep the note under ${DRAFT_NOTE_MAX} characters.` : null;
}

/** The people-facing name of who made a draft. */
export function authorLabel(author: string): string {
  switch (author) {
    case "source_svg":
      return "The SVG the organizer sent";
    case "operator_svg":
      return "An SVG from an administrator";
    case "operator":
      return "An administrator";
    case "generator":
      return "The generator";
    case "organizer_answers":
      return "The generator, after answers";
    case "organizer_text":
      return "The generator, after a text";
    default:
      return author;
  }
}

export function escalationLabel(reason: string | null | undefined): string {
  switch (reason) {
    case null:
    case undefined:
      return "—";
    case "no_generator":
      return "No generator yet";
    case "organizer_request":
      return "The organizer asked for an administrator";
    case "answers":
      return "The organizer sent answers";
    case "organizer_text":
      return "The organizer sent a text";
    default:
      return reason;
  }
}

export function downloadBlob(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = fileName;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

// ---------------------------------------------------------------------------
// Card (stateful)
// ---------------------------------------------------------------------------

type DialogKind = "approve" | "reject" | "takeover" | null;

export function IntakeCard({ intakeId }: { readonly intakeId: string }): JSX.Element {
  const queryClient = useQueryClient();
  const detailKey = ["admin", "venue-plans", "intake", intakeId] as const;
  const query = useQuery<VenuePlanIntake, ApiError>({
    queryKey: detailKey,
    queryFn: () => getIntake(intakeId),
    refetchOnWindowFocus: false,
    retry: false,
  });
  const intake = query.data;

  const [dialog, setDialog] = useState<DialogKind>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [shownDraft, setShownDraft] = useState<number | null>(null);
  const [imageUrl, setImageUrl] = useState<string | null>(null);
  const [imageState, setImageState] = useState<{ loading: boolean; error: string | null }>({ loading: false, error: null });

  const draftToShow = shownDraft ?? intake?.current_draft ?? null;

  // The draft image: SVG through the API, shown from a blob URL. No markup in the page.
  useEffect(() => {
    if (draftToShow === null) {
      setImageUrl(null);
      return undefined;
    }
    let cancelled = false;
    let created: string | null = null;
    setImageState({ loading: true, error: null });
    fetchDraftSvg(intakeId, draftToShow)
      .then((blob) => {
        if (cancelled) return;
        created = URL.createObjectURL(blob);
        setImageUrl(created);
        setImageState({ loading: false, error: null });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setImageUrl(null);
        setImageState({ loading: false, error: err instanceof ApiError ? err.message : String(err) });
      });
    return () => {
      cancelled = true;
      if (created !== null) URL.revokeObjectURL(created);
    };
  }, [intakeId, draftToShow, intake?.updated_at]);

  const run = async <T,>(name: string, call: () => Promise<T>, onOk: (r: T) => void, inDialog: boolean): Promise<void> => {
    setBusy(name);
    setError(null);
    setDialogError(null);
    setNotice(null);
    try {
      const result = await call();
      onOk(result);
    } catch (err) {
      const text = err instanceof ApiError ? intakeErrorMessage(err) : String(err);
      if (inDialog) setDialogError(text);
      else setError(text);
    } finally {
      setBusy(null);
    }
  };

  const keep = async (next: VenuePlanIntake, text: string): Promise<void> => {
    queryClient.setQueryData(detailKey, next);
    setShownDraft(null);
    setDialog(null);
    setNotice(text);
    await queryClient.invalidateQueries({ queryKey: ["admin", "venue-plans", "intakes"] });
  };

  const onClaim = (force: boolean): Promise<void> =>
    run("claim", () => claimIntake(intakeId, { force }), (n) => void keep(n, force ? "You took over the intake." : "You hold the intake now."), force);
  const onUpload = (file: File, note: string): Promise<void> =>
    run("upload", () => uploadIntakeDraft(intakeId, file, note), (n) => void keep(n, "The draft is saved. It is the current draft now."), false);
  const onReturn = (note: string): Promise<void> =>
    run("return", () => returnIntake(intakeId, note), (n) => void keep(n, "The intake went back to the organizer."), false);
  const onApprove = (): Promise<void> =>
    run(
      "approve",
      () => approveIntake(intakeId, intake?.current_draft ?? 0),
      (n) => void keep(n, "The seating plan is saved. The organizer gets a message."),
      true,
    );
  const onReject = (reason: string): Promise<void> =>
    run("reject", () => rejectIntake(intakeId, reason), (n) => void keep(n, "The intake is closed. The organizer reads your reason."), true);
  const onDownload = (source: VenuePlanIntakeSource, png: boolean): Promise<void> =>
    run(
      "download",
      () => downloadIntakeSource(intakeId, source, png ? "png" : undefined),
      (f) => downloadBlob(f.blob, f.fileName),
      false,
    );

  return (
    <div style={pageStyle} data-testid="venue-plan-card">
      <div style={{ marginBottom: 8, fontSize: 13 }}>
        <Link to="/venue-plans" data-testid="venue-plan-back">
          ← Seating plans
        </Link>
      </div>
      {query.isLoading ? <p style={mutedStyle}>Loading…</p> : null}
      {query.error ? (
        <div style={errorStyle} data-testid="venue-plan-load-error">
          {query.error.status === 404
            ? "This intake does not exist. Go back to the list."
            : `We did not load the intake. Reload the page. ${query.error.message}`}
        </div>
      ) : null}
      {notice !== null ? (
        <div style={noticeStyle} data-testid="venue-plan-notice">
          {notice}
        </div>
      ) : null}
      {error !== null ? (
        <div style={errorStyle} data-testid="venue-plan-action-error">
          {error}
        </div>
      ) : null}

      {intake ? (
        <>
          <IntakeCardBody
            intake={intake}
            callerId={getUserId()}
            draftImageUrl={imageUrl}
            draftImageLoading={imageState.loading}
            draftImageError={imageState.error}
            shownDraft={draftToShow}
            busy={busy}
            onShowDraft={setShownDraft}
            onClaim={() => void onClaim(false)}
            onTakeOver={() => {
              setDialogError(null);
              setDialog("takeover");
            }}
            onDownload={(s, png) => void onDownload(s, png)}
            onUpload={(f, n) => void onUpload(f, n)}
            onReturn={(n) => void onReturn(n)}
            onAskApprove={() => {
              setDialogError(null);
              setDialog("approve");
            }}
            onAskReject={() => {
              setDialogError(null);
              setDialog("reject");
            }}
          />
          {dialog === "takeover" ? (
            <Dialog
              title="Take over the intake"
              testId="venue-plan-takeover-dialog"
              onClose={() => setDialog(null)}
              onSubmit={() => void onClaim(true)}
              footer={
                <>
                  <button type="button" style={buttonStyle} onClick={() => setDialog(null)}>
                    Cancel
                  </button>
                  <button type="submit" style={primaryButtonStyle} disabled={busy === "claim"} data-testid="venue-plan-takeover-confirm">
                    Take over
                  </button>
                </>
              }
            >
              <p style={hintStyle}>Another administrator holds this intake. If you take it over, that administrator cannot change it any more.</p>
              {dialogError !== null ? <div style={errorStyle}>{dialogError}</div> : null}
            </Dialog>
          ) : null}
          {dialog === "approve" ? (
            <Dialog
              title="Approve the current draft"
              testId="venue-plan-approve-dialog"
              onClose={() => setDialog(null)}
              onSubmit={() => void onApprove()}
              footer={
                <>
                  <button type="button" style={buttonStyle} onClick={() => setDialog(null)}>
                    Cancel
                  </button>
                  <button type="submit" style={primaryButtonStyle} disabled={busy === "approve"} data-testid="venue-plan-approve-confirm">
                    {busy === "approve" ? "Saving…" : "Approve"}
                  </button>
                </>
              }
            >
              <p style={hintStyle}>
                Draft {intake.current_draft} becomes a seating plan of {intake.org_name ?? "the organization"} for {intake.venue_name}.
                The intake closes. The organizer gets a message.
              </p>
              {dialogError !== null ? <div style={errorStyle}>{dialogError}</div> : null}
            </Dialog>
          ) : null}
          {dialog === "reject" ? (
            <RejectDialog
              busy={busy === "reject"}
              error={dialogError}
              onClose={() => setDialog(null)}
              onConfirm={(r) => void onReject(r)}
            />
          ) : null}
        </>
      ) : null}
    </div>
  );
}

function RejectDialog({
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly busy: boolean;
  readonly error: string | null;
  readonly onClose: () => void;
  readonly onConfirm: (reason: string) => void;
}): JSX.Element {
  const [reason, setReason] = useState("");
  const [touched, setTouched] = useState(false);
  const problem = validateRejectReason(reason);
  return (
    <Dialog
      title="Reject the intake"
      testId="venue-plan-reject-dialog"
      onClose={onClose}
      onSubmit={() => {
        setTouched(true);
        if (problem === null) onConfirm(reason);
      }}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" style={busy ? { ...dangerButtonStyle, ...disabledButtonStyle } : dangerButtonStyle} disabled={busy} data-testid="venue-plan-reject-confirm">
            {busy ? "Saving…" : "Reject"}
          </button>
        </>
      }
    >
      <p style={hintStyle}>The intake closes. The organizer reads the reason and can send another file.</p>
      <label style={labelStyle} htmlFor="venue-plan-reject-reason">
        Reason for the organizer
      </label>
      <textarea
        id="venue-plan-reject-reason"
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        rows={4}
        style={inputStyle}
        data-testid="venue-plan-reject-reason"
      />
      {touched && problem !== null ? <p style={fieldErrorStyle}>{problem}</p> : null}
      {error !== null ? <div style={errorStyle}>{error}</div> : null}
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Card body (presentational: no router, no queries)
// ---------------------------------------------------------------------------

export interface IntakeCardBodyProps {
  readonly intake: VenuePlanIntake;
  readonly callerId: string | null;
  readonly now?: number;
  readonly draftImageUrl: string | null;
  readonly draftImageLoading: boolean;
  readonly draftImageError: string | null;
  readonly shownDraft: number | null;
  readonly busy: string | null;
  readonly onShowDraft: (n: number) => void;
  readonly onClaim: () => void;
  readonly onTakeOver: () => void;
  readonly onDownload: (source: VenuePlanIntakeSource, png: boolean) => void;
  readonly onUpload: (file: File, note: string) => void;
  readonly onReturn: (note: string) => void;
  readonly onAskApprove: () => void;
  readonly onAskReject: () => void;
}

export function IntakeCardBody(p: IntakeCardBodyProps): JSX.Element {
  const { intake } = p;
  const mine = holdsClaim(intake, p.callerId);
  const final = isFinalState(intake.state);
  return (
    <>
      <IntakeHeader intake={intake} mine={mine} now={p.now} />

      <ClaimBar intake={intake} callerId={p.callerId} busy={p.busy === "claim"} onClaim={p.onClaim} onTakeOver={p.onTakeOver} />

      <SourcesSection sources={intake.sources} onDownload={p.onDownload} busy={p.busy === "download"} />

      <OrganizerTexts intake={intake} />

      <DraftSection
        intake={intake}
        imageUrl={p.draftImageUrl}
        loading={p.draftImageLoading}
        error={p.draftImageError}
        shownDraft={p.shownDraft}
        onShowDraft={p.onShowDraft}
      />

      {final ? (
        <div style={warningStyle} data-testid="venue-plan-final">
          The intake is {intakeStateLabel(intake.state).toLowerCase()}. Nothing changes after that.
          {intake.plan_id ? ` The seating plan is ${intake.plan_id}, version ${intake.plan_version ?? 1}.` : ""}
        </div>
      ) : null}

      <ActionsSection
        intake={intake}
        callerId={p.callerId}
        busy={p.busy}
        onUpload={p.onUpload}
        onReturn={p.onReturn}
        onAskApprove={p.onAskApprove}
        onAskReject={p.onAskReject}
      />
    </>
  );
}

function Fact({ label, children, testId }: { label: string; children: ReactNode; testId?: string }): JSX.Element {
  return (
    <div>
      <div style={{ fontSize: 11, textTransform: "uppercase", letterSpacing: 0.4, color: "#64748b" }}>{label}</div>
      <div style={{ fontSize: 14 }} data-testid={testId}>
        {children}
      </div>
    </div>
  );
}

export function IntakeHeader({
  intake,
  mine,
  now,
}: {
  readonly intake: VenuePlanIntake;
  readonly mine: boolean;
  readonly now?: number;
}): JSX.Element {
  return (
    <div style={sectionStyle} data-testid="venue-plan-header">
      <h1 style={{ margin: "0 0 12px 0", fontSize: 22, fontWeight: 600 }}>
        {intake.org_name ?? intake.org_id.slice(0, 8)} · {intake.venue_name}
      </h1>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))", gap: 12 }}>
        <Fact label="State">
          <Pill tone={stateTone(intake.state)} testId="venue-plan-state">
            {intakeStateLabel(intake.state)}
          </Pill>
        </Fact>
        <Fact label="Channel">{intake.channel}</Fact>
        <Fact label="Why an administrator">{escalationLabel(intake.escalation_reason)}</Fact>
        <Fact label="Received">{formatDateTime(intake.created_at)}</Fact>
        <Fact label="Waiting" testId="venue-plan-waiting">
          {isFinalState(intake.state) ? "—" : waitingLabel(intake.created_at, now)}
        </Fact>
        <Fact label="Administrator" testId="venue-plan-operator">
          {!intake.operator_claimed || !intake.operator_id
            ? "Nobody"
            : mine
              ? `You, since ${formatDateTime(intake.operator_claimed_at)}`
              : `${intake.operator_id.slice(0, 8)}, since ${formatDateTime(intake.operator_claimed_at)}`}
        </Fact>
        <Fact label="Drafts">{intake.review_rounds}</Fact>
        <Fact label="Last change">{formatDateTime(intake.updated_at)}</Fact>
      </div>
    </div>
  );
}

export function ClaimBar({
  intake,
  callerId,
  busy,
  onClaim,
  onTakeOver,
}: {
  readonly intake: VenuePlanIntake;
  readonly callerId: string | null;
  readonly busy: boolean;
  readonly onClaim: () => void;
  readonly onTakeOver: () => void;
}): JSX.Element {
  const gate = gateFor("claim", intake, callerId);
  const heldByOther = intake.operator_claimed && !holdsClaim(intake, callerId);
  return (
    <div style={{ ...sectionStyle, display: "flex", gap: 12, alignItems: "center", flexWrap: "wrap" }} data-testid="venue-plan-claim-bar">
      {heldByOther ? (
        <button
          type="button"
          style={!gate.enabled || busy ? { ...buttonStyle, ...disabledButtonStyle } : buttonStyle}
          disabled={!gate.enabled || busy}
          onClick={onTakeOver}
          data-testid="venue-plan-takeover"
        >
          Take over
        </button>
      ) : (
        <button
          type="button"
          style={!gate.enabled || busy ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
          disabled={!gate.enabled || busy}
          onClick={onClaim}
          data-testid="venue-plan-claim"
        >
          {busy ? "Saving…" : "Claim"}
        </button>
      )}
      <span style={mutedStyle} data-testid="venue-plan-claim-hint">
        {gate.enabled
          ? heldByOther
            ? "Another administrator holds this intake. Take over to continue."
            : "Claim the intake before you change it. Nobody else can change it then."
          : gate.why}
      </span>
    </div>
  );
}

export function SourcesSection({
  sources,
  onDownload,
  busy,
}: {
  readonly sources: readonly VenuePlanIntakeSource[];
  readonly onDownload: (s: VenuePlanIntakeSource, png: boolean) => void;
  readonly busy: boolean;
}): JSX.Element {
  return (
    <div style={sectionStyle} data-testid="venue-plan-sources">
      <div style={sectionTitleStyle}>Files from the organizer</div>
      <p style={hintStyle}>A download asks for a reason. The platform keeps the reason in the audit log.</p>
      {sources.length === 0 ? (
        <p style={mutedStyle}>The organizer sent no files.</p>
      ) : (
        <table style={tableStyle}>
          <thead>
            <tr>
              <th style={thStyle}>Name</th>
              <th style={thStyle}>Kind</th>
              <th style={thStyle}>Size</th>
              <th style={thStyle}>Details</th>
              <th style={thStyle}></th>
            </tr>
          </thead>
          <tbody>
            {sources.map((s) => (
              <tr key={s.id} data-testid={`venue-plan-source-${s.id}`}>
                <td style={tdStyle}>{s.file_name}</td>
                <td style={tdStyle}>{s.kind}</td>
                <td style={tdStyle}>{formatBytes(s.byte_size)}</td>
                <td style={tdStyle}>
                  {s.pages !== null && s.pages !== undefined ? `${s.pages} pages` : ""}
                  {s.width !== null && s.width !== undefined && s.height !== null && s.height !== undefined ? `${s.width} × ${s.height} px` : ""}
                  {s.needs_conversion ? " Converted later." : ""}
                </td>
                <td style={tdStyle}>
                  <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                    <button
                      type="button"
                      style={busy ? { ...buttonStyle, ...disabledButtonStyle } : buttonStyle}
                      disabled={busy}
                      onClick={() => onDownload(s, false)}
                      data-testid={`venue-plan-download-${s.id}`}
                    >
                      Download
                    </button>
                    {s.png_copy_path ? (
                      <button
                        type="button"
                        style={busy ? { ...buttonStyle, ...disabledButtonStyle } : buttonStyle}
                        disabled={busy}
                        onClick={() => onDownload(s, true)}
                        data-testid={`venue-plan-download-png-${s.id}`}
                      >
                        Download PNG
                      </button>
                    ) : null}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

export function OrganizerTexts({ intake }: { readonly intake: VenuePlanIntake }): JSX.Element | null {
  const corrections = intake.corrections ?? [];
  const answers = (intake.draft?.questions ?? []).filter((q) => q.answer !== null && q.answer !== undefined);
  const hasAny =
    Boolean(intake.escalation_text) ||
    Boolean(intake.return_note) ||
    Boolean(intake.reject_reason) ||
    corrections.length > 0 ||
    answers.length > 0;
  if (!hasAny) return null;
  return (
    <div style={sectionStyle} data-testid="venue-plan-texts">
      <div style={sectionTitleStyle}>Texts and answers</div>
      {intake.escalation_text ? (
        <p data-testid="venue-plan-escalation-text" style={{ whiteSpace: "pre-wrap" }}>
          <strong>The organizer wrote:</strong> {intake.escalation_text}
        </p>
      ) : null}
      {corrections.map((c) => (
        <p key={c.id} style={{ whiteSpace: "pre-wrap" }} data-testid={`venue-plan-correction-${c.id}`}>
          <strong>
            {c.kind === "answer" ? "Answers" : c.kind === "organizer_text" ? "Text correction" : "Edit"}
            {c.draft !== null && c.draft !== undefined ? ` on draft ${c.draft}` : ""} ({c.status}, {formatDateTime(c.created_at)}):
          </strong>{" "}
          {c.text ?? "Answers to the questions of the draft."}
        </p>
      ))}
      {answers.map((q) => (
        <p key={q.id} data-testid={`venue-plan-answer-${q.id}`}>
          <strong>Question “{q.kind}”:</strong> {JSON.stringify(q.answer)}
        </p>
      ))}
      {intake.return_note ? (
        <p style={{ whiteSpace: "pre-wrap" }}>
          <strong>Note sent to the organizer:</strong> {intake.return_note}
        </p>
      ) : null}
      {intake.reject_reason ? (
        <p style={{ whiteSpace: "pre-wrap" }}>
          <strong>Reason of the rejection:</strong> {intake.reject_reason}
        </p>
      ) : null}
    </div>
  );
}

export function DraftSection({
  intake,
  imageUrl,
  loading,
  error,
  shownDraft,
  onShowDraft,
}: {
  readonly intake: VenuePlanIntake;
  readonly imageUrl: string | null;
  readonly loading: boolean;
  readonly error: string | null;
  readonly shownDraft: number | null;
  readonly onShowDraft: (n: number) => void;
}): JSX.Element {
  const draft = intake.draft;
  const summary = draft?.summary;
  return (
    <div style={sectionStyle} data-testid="venue-plan-drafts">
      <div style={sectionTitleStyle}>Drafts</div>
      {intake.drafts.length === 0 ? (
        <p style={mutedStyle}>There is no draft yet. Upload an SVG draft below.</p>
      ) : (
        <>
          <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginBottom: 10 }} role="group" aria-label="Drafts">
            {intake.drafts.map((d) => (
              <button
                key={d.n}
                type="button"
                style={d.n === shownDraft ? primaryButtonStyle : buttonStyle}
                onClick={() => onShowDraft(d.n)}
                title={`${authorLabel(d.author)}, ${formatDateTime(d.created_at)}`}
                data-testid={`venue-plan-draft-${d.n}`}
              >
                Draft {d.n}
                {d.n === intake.current_draft ? " (current)" : ""}
              </button>
            ))}
          </div>
          {summary ? (
            <div style={{ display: "flex", gap: 16, flexWrap: "wrap", marginBottom: 10 }} data-testid="venue-plan-summary">
              <span>
                <strong>{summary.seats}</strong> seats
              </span>
              <span>
                <strong>{summary.rows}</strong> rows
              </span>
              <span>
                <strong>{summary.sections}</strong> sections
              </span>
              <span>
                <strong>{summary.warnings.length}</strong> warnings
              </span>
              <span>Type: {summary.plan_type}</span>
            </div>
          ) : null}
          {summary && summary.warnings.length > 0 ? (
            <p style={hintStyle} data-testid="venue-plan-warnings">
              Warnings: {summary.warnings.join(", ")}
            </p>
          ) : null}
          {draft?.note ? <p style={hintStyle}>Note on the current draft: {draft.note}</p> : null}
          {loading ? <p style={mutedStyle}>Loading the image…</p> : null}
          {error !== null ? <div style={errorStyle}>We did not load the image. Reload the page. {error}</div> : null}
          {imageUrl !== null ? (
            <img
              src={imageUrl}
              alt={`Draft ${shownDraft ?? ""} of the seating plan`}
              style={{ maxWidth: "100%", maxHeight: 640, display: "block", border: "1px solid #e2e8f0", background: "#ffffff" }}
              data-testid="venue-plan-draft-image"
            />
          ) : null}
        </>
      )}
    </div>
  );
}

function GateHint({ gate }: { readonly gate: ActionGate }): JSX.Element | null {
  return gate.enabled ? null : (
    <span style={{ ...mutedStyle, fontSize: 12 }} data-testid="venue-plan-gate-hint">
      {gate.why}
    </span>
  );
}

export function ActionsSection({
  intake,
  callerId,
  busy,
  onUpload,
  onReturn,
  onAskApprove,
  onAskReject,
}: {
  readonly intake: VenuePlanIntake;
  readonly callerId: string | null;
  readonly busy: string | null;
  readonly onUpload: (file: File, note: string) => void;
  readonly onReturn: (note: string) => void;
  readonly onAskApprove: () => void;
  readonly onAskReject: () => void;
}): JSX.Element {
  const upload = gateFor("upload_draft", intake, callerId);
  const ret = gateFor("return", intake, callerId);
  const approve = gateFor("approve", intake, callerId);
  const reject = gateFor("reject", intake, callerId);
  return (
    <div data-testid="venue-plan-actions">
      <UploadDraftForm gate={upload} busy={busy === "upload"} onUpload={onUpload} />
      <ReturnForm gate={ret} busy={busy === "return"} onReturn={onReturn} />
      <div style={{ ...sectionStyle, display: "flex", gap: 12, alignItems: "center", flexWrap: "wrap" }}>
        <button
          type="button"
          style={!approve.enabled ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
          disabled={!approve.enabled}
          title={approve.why || undefined}
          onClick={onAskApprove}
          data-testid="venue-plan-approve"
        >
          Approve
        </button>
        <GateHint gate={approve} />
        <button
          type="button"
          style={!reject.enabled ? { ...dangerButtonStyle, ...disabledButtonStyle } : dangerButtonStyle}
          disabled={!reject.enabled}
          title={reject.why || undefined}
          onClick={onAskReject}
          data-testid="venue-plan-reject"
        >
          Reject
        </button>
        <GateHint gate={reject} />
      </div>
    </div>
  );
}

function UploadDraftForm({
  gate,
  busy,
  onUpload,
}: {
  readonly gate: ActionGate;
  readonly busy: boolean;
  readonly onUpload: (file: File, note: string) => void;
}): JSX.Element {
  const [file, setFile] = useState<File | null>(null);
  const [note, setNote] = useState("");
  const [touched, setTouched] = useState(false);
  const problem = validateDraftFile(file) ?? validateNote(note);
  return (
    <form
      style={sectionStyle}
      data-testid="venue-plan-upload-form"
      onSubmit={(e) => {
        e.preventDefault();
        setTouched(true);
        if (problem === null && file !== null) onUpload(file, note);
      }}
    >
      <div style={sectionTitleStyle}>Upload an SVG draft</div>
      <p style={hintStyle}>Draw the plan by hand, for example in Inkscape. The server checks the geometry before it saves the file.</p>
      <label style={labelStyle} htmlFor="venue-plan-upload-file">
        SVG file
      </label>
      <input
        id="venue-plan-upload-file"
        type="file"
        accept=".svg,image/svg+xml"
        disabled={!gate.enabled}
        onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        data-testid="venue-plan-upload-file"
      />
      <label style={{ ...labelStyle, marginTop: 10 }} htmlFor="venue-plan-upload-note">
        Note (optional)
      </label>
      <textarea
        id="venue-plan-upload-note"
        rows={2}
        value={note}
        disabled={!gate.enabled}
        onChange={(e) => setNote(e.target.value)}
        style={inputStyle}
        data-testid="venue-plan-upload-note"
      />
      {touched && problem !== null ? <p style={fieldErrorStyle}>{problem}</p> : null}
      <div style={{ display: "flex", gap: 12, alignItems: "center", marginTop: 10 }}>
        <button
          type="submit"
          style={!gate.enabled || busy ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
          disabled={!gate.enabled || busy}
          data-testid="venue-plan-upload-submit"
        >
          {busy ? "Uploading…" : "Upload draft"}
        </button>
        <GateHint gate={gate} />
      </div>
    </form>
  );
}

function ReturnForm({
  gate,
  busy,
  onReturn,
}: {
  readonly gate: ActionGate;
  readonly busy: boolean;
  readonly onReturn: (note: string) => void;
}): JSX.Element {
  const [note, setNote] = useState("");
  const problem = validateNote(note);
  return (
    <form
      style={sectionStyle}
      data-testid="venue-plan-return-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (problem === null) onReturn(note);
      }}
    >
      <div style={sectionTitleStyle}>Return to the organizer</div>
      <p style={hintStyle}>The organizer checks the current draft and approves it. The organizer gets your note in a message.</p>
      <label style={labelStyle} htmlFor="venue-plan-return-note">
        Note (optional)
      </label>
      <textarea
        id="venue-plan-return-note"
        rows={2}
        value={note}
        disabled={!gate.enabled}
        onChange={(e) => setNote(e.target.value)}
        style={inputStyle}
        data-testid="venue-plan-return-note"
      />
      {problem !== null ? <p style={fieldErrorStyle}>{problem}</p> : null}
      <div style={{ display: "flex", gap: 12, alignItems: "center", marginTop: 10 }}>
        <button
          type="submit"
          style={!gate.enabled || busy ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
          disabled={!gate.enabled || busy}
          data-testid="venue-plan-return-submit"
        >
          {busy ? "Saving…" : "Return to the organizer"}
        </button>
        <GateHint gate={gate} />
      </div>
    </form>
  );
}
