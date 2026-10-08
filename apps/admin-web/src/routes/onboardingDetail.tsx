/**
 * Onboarding card — one organizer application (backlog APP-09, spec
 * 08_architecture/34_onboarding_applications_ru.md §6 and §8).
 *
 *   GET  /v1/admin/onboarding/applications/{id}
 *   POST /v1/admin/onboarding/applications/{id}/approve | reject |
 *        request-info | extend | resend | purge | recheck | notes
 *
 * The card shows the answers grouped into the form's blocks, the system's
 * checks, the timeline, private notes and the applicant's contacts, and offers
 * the decisions the status allows. Every decision except the note and the
 * recheck carries X-Admin-Reason (the API client prompts for it once per
 * session). Approving creates a workspace in one transaction on the server;
 * the confirmation dialog lists what will be created.
 *
 * Mock data: NONE. The card hits the live backend.
 */
import { createRoute, Link, useParams } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ApiError } from "@/lib/api/client";
import {
  FORM_BLOCKS,
  actorText,
  addOnboardingNote,
  approveOnboardingApplication,
  availableActions,
  checkLabel,
  eventDetailText,
  eventLabel,
  extendOnboardingApplication,
  fieldLabel,
  getOnboardingApplication,
  groupAnswers,
  purgeOnboardingApplication,
  recheckOnboardingApplication,
  rejectOnboardingApplication,
  relativeAge,
  requestOnboardingInfo,
  resendOnboardingLink,
  serverFieldErrors,
  sourceLabel,
  validateExtendDays,
  validateReject,
  validateRequestInfo,
  REQUESTABLE_FIELDS,
  type AnswerGroup,
  type OnboardingAction,
  type OnboardingApplication,
  type OnboardingApproveResult,
  type OnboardingCheck,
  type OnboardingDetail,
  type OnboardingEvent,
  type OnboardingNote,
} from "@/lib/api/onboarding";
import { formatDateTime } from "@/lib/admin/supportConsole";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import {
  Dialog,
  Pill,
  StatusPill,
  buttonStyle,
  checkTone,
  dangerButtonStyle,
  disabledButtonStyle,
  errorStyle,
  fieldErrorStyle,
  headerRowStyle,
  headingStyle,
  hintStyle,
  inputStyle,
  labelStyle,
  mutedStyle,
  noticeStyle,
  pageStyle,
  primaryButtonStyle,
  sectionStyle,
  sectionTitleStyle,
  subheadingStyle,
  tdStyle,
  tableStyle,
  warningStyle,
} from "./onboardingUi";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/onboarding/$applicationId",
  component: OnboardingDetailRoute,
});

const ONBOARDING_NAV_ENTRY = NAV_BY_PATH["/onboarding"];
if (ONBOARDING_NAV_ENTRY === undefined) {
  throw new Error("onboarding detail route: NAV_BY_PATH['/onboarding'] missing");
}

function OnboardingDetailRoute() {
  const { applicationId } = useParams({ from: "/onboarding/$applicationId" });
  return (
    <RequirePermission entry={ONBOARDING_NAV_ENTRY}>
      <OnboardingCard applicationId={applicationId} />
    </RequirePermission>
  );
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

/** Link to the organization an approval created (the organizations explorer opens its drawer from the hash). */
export function organizationHref(orgId: string): string {
  return `/organizations#org=${encodeURIComponent(orgId)}`;
}

/** Lines of the approval confirmation: exactly what the server will create. */
export function approvalSummary(app: Pick<OnboardingApplication, "org_name" | "legal_name" | "country" | "email" | "applicant_name" | "source" | "telegram_user_id">): readonly string[] {
  const lines = [
    `Organization “${app.org_name ?? "—"}”${app.legal_name ? ` (legal name ${app.legal_name})` : ""}${app.country ? `, ${app.country}` : ""}, with the legal and address fields from the application.`,
    "A direct-merchant sales channel with a public sales page. The channel is never merchant of record — only a superadmin can switch that on by hand.",
    `The owner account for ${app.applicant_name ? `${app.applicant_name} ` : ""}<${app.email}>, with a set-password e-mail sent to that address.`,
  ];
  if (app.source === "telegram" || (app.telegram_user_id !== null && app.telegram_user_id !== undefined)) {
    lines.push("A link between the owner and their Telegram account, so the event-center bot works at once without a second invitation.");
  }
  lines.push(
    "KYB status stays “unverified”.",
    "Any other open application from the same e-mail is closed as a duplicate.",
  );
  return lines;
}

/** Counts of checks that are not green, for the approval warning. */
export function checkProblems(checks: readonly OnboardingCheck[]): { warn: number; fail: number } {
  return {
    warn: checks.filter((c) => c.result === "warn").length,
    fail: checks.filter((c) => c.result === "fail").length,
  };
}

// ---------------------------------------------------------------------------
// Card (stateful)
// ---------------------------------------------------------------------------

type DialogKind = "approve" | "reject" | "request_info" | "extend" | "purge" | null;

export function OnboardingCard({ applicationId }: { readonly applicationId: string }): JSX.Element {
  const queryClient = useQueryClient();
  const detailKey = ["admin", "onboarding", "detail", applicationId] as const;
  const query = useQuery<OnboardingDetail, ApiError>({
    queryKey: detailKey,
    queryFn: () => getOnboardingApplication(applicationId),
    refetchOnWindowFocus: false,
    retry: false,
  });

  const [dialog, setDialog] = useState<DialogKind>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [actionError, setActionError] = useState<ApiError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [approved, setApproved] = useState<OnboardingApproveResult | null>(null);

  const detail = query.data;

  const afterChange = async (): Promise<void> => {
    await queryClient.invalidateQueries({ queryKey: ["admin", "onboarding", "list"] });
  };

  /** Runs one call; true when it succeeded. A failure is kept for the open dialog or the page banner. */
  const run = async <T,>(name: string, call: () => Promise<T>, onOk: (r: T) => void | Promise<void>): Promise<boolean> => {
    setBusy(name);
    setActionError(null);
    setNotice(null);
    try {
      const result = await call();
      await onOk(result);
      return true;
    } catch (err) {
      setActionError(err instanceof ApiError ? err : new ApiError(0, { code: "client.error", message: String(err) }));
      return false;
    } finally {
      setBusy(null);
    }
  };

  const keepDetail = async (d: OnboardingDetail, message: string): Promise<void> => {
    queryClient.setQueryData(detailKey, d);
    setNotice(message);
    setDialog(null);
    await afterChange();
  };

  const onApprove = () =>
    run("approve", () => approveOnboardingApplication(applicationId), async (res) => {
      setApproved(res);
      setDialog(null);
      await queryClient.invalidateQueries({ queryKey: detailKey });
      await afterChange();
    });

  const onReject = (reason: string, message: string) =>
    run("reject", () => rejectOnboardingApplication(applicationId, { reason: reason.trim(), message: message.trim() }), (d) =>
      keepDetail(d, "The application was rejected."),
    );

  const onRequestInfo = (fields: readonly string[], message: string) =>
    run("request_info", () => requestOnboardingInfo(applicationId, { fields: [...fields], message: message.trim() }), (d) =>
      keepDetail(d, "The applicant was asked for details and got a fresh link."),
    );

  const onExtend = (days: number | undefined) =>
    run("extend", () => extendOnboardingApplication(applicationId, days), (d) => keepDetail(d, "The draft was extended."));

  const onPurge = () =>
    run("purge", () => purgeOnboardingApplication(applicationId), (d) => keepDetail(d, "Personal data was erased."));

  const onResend = () =>
    run("resend", () => resendOnboardingLink(applicationId), (d) => keepDetail(d, "A new continue link was sent to the applicant."));

  const onRecheck = () =>
    run("recheck", () => recheckOnboardingApplication(applicationId), (d) => keepDetail(d, "The checks were recomputed."));

  const onAddNote = (body: string) =>
    run("note", () => addOnboardingNote(applicationId, body.trim()), async () => {
      await queryClient.invalidateQueries({ queryKey: detailKey });
    });

  const openDialog = (kind: Exclude<DialogKind, null>): void => {
    setActionError(null);
    setNotice(null);
    setDialog(kind);
  };

  const onAction = (action: OnboardingAction): void => {
    switch (action) {
      case "approve":
        return openDialog("approve");
      case "reject":
        return openDialog("reject");
      case "request_info":
        return openDialog("request_info");
      case "extend":
        return openDialog("extend");
      case "purge":
        return openDialog("purge");
      case "resend":
        void onResend();
        return;
      case "recheck":
        void onRecheck();
        return;
    }
  };

  return (
    <div style={pageStyle} data-testid="onboarding-card">
      <div style={{ marginBottom: 8, fontSize: 13 }}>
        <Link to="/onboarding" data-testid="onboarding-back">
          ← Onboarding
        </Link>
      </div>

      {query.isLoading ? <p style={mutedStyle}>Loading…</p> : null}
      {query.error ? (
        <div style={errorStyle} data-testid="onboarding-card-error">
          {query.error.status === 404 ? "This application does not exist." : `Could not load the application: ${query.error.message}`}
        </div>
      ) : null}

      {approved !== null ? <ApprovedBanner result={approved} /> : null}
      {notice !== null ? (
        <div style={noticeStyle} data-testid="onboarding-notice">
          {notice}
        </div>
      ) : null}
      {actionError !== null && dialog === null ? (
        <div style={errorStyle} data-testid="onboarding-action-error">
          {actionError.message}
          {actionError.code ? <span style={{ opacity: 0.7 }}> ({actionError.code})</span> : null}
        </div>
      ) : null}

      {detail ? (
        <>
          <OnboardingCardBody
            detail={detail}
            busy={busy}
            onAction={onAction}
            notes={<NotesPanel notes={detail.notes} busy={busy === "note"} onAdd={onAddNote} />}
          />

          {dialog === "approve" ? (
            <ApproveDialog
              detail={detail}
              busy={busy === "approve"}
              error={actionError}
              onClose={() => setDialog(null)}
              onConfirm={() => void onApprove()}
            />
          ) : null}
          {dialog === "reject" ? (
            <RejectDialog busy={busy === "reject"} error={actionError} onClose={() => setDialog(null)} onConfirm={(r, m) => void onReject(r, m)} />
          ) : null}
          {dialog === "request_info" ? (
            <RequestInfoDialog
              application={detail.application}
              busy={busy === "request_info"}
              error={actionError}
              onClose={() => setDialog(null)}
              onConfirm={(f, m) => void onRequestInfo(f, m)}
            />
          ) : null}
          {dialog === "extend" ? (
            <ExtendDialog busy={busy === "extend"} error={actionError} onClose={() => setDialog(null)} onConfirm={(d) => void onExtend(d)} />
          ) : null}
          {dialog === "purge" ? (
            <PurgeDialog busy={busy === "purge"} error={actionError} onClose={() => setDialog(null)} onConfirm={() => void onPurge()} />
          ) : null}
        </>
      ) : null}
    </div>
  );
}

function ApprovedBanner({ result }: { readonly result: OnboardingApproveResult }): JSX.Element {
  return (
    <div style={noticeStyle} data-testid="onboarding-approved-banner">
      <strong>Approved.</strong> The organization <code>{result.org_slug}</code> was created
      {result.owner_created ? " together with its owner account" : " (the owner account already existed)"}
      {result.closed_duplicates > 0 ? `; ${result.closed_duplicates} duplicate application(s) were closed` : ""}.{" "}
      <a href={organizationHref(result.org_id)} data-testid="onboarding-approved-org-link">
        Open the organization
      </a>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Card body (pure)
// ---------------------------------------------------------------------------

export interface OnboardingCardBodyProps {
  readonly detail: OnboardingDetail;
  readonly busy: string | null;
  readonly onAction: (a: OnboardingAction) => void;
  /** The notes section; a stateful panel in the page, a list in tests. */
  readonly notes: ReactNode;
  readonly now?: number;
}

export function OnboardingCardBody({ detail, busy, onAction, notes, now }: OnboardingCardBodyProps): JSX.Element {
  const app = detail.application;
  const groups = groupAnswers(app.answers, app.requested_fields);
  return (
    <>
      <CardHeader app={app} wouldApprove={detail.would_approve} now={now} />
      <ActionBar app={app} busy={busy} onAction={onAction} />
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(380px, 1fr))", gap: 16, alignItems: "start" }}>
        <AnswersSection groups={groups} purged={app.purged_at !== null && app.purged_at !== undefined} />
        <div>
          <ChecksSection
            checks={detail.checks}
            wouldApprove={detail.would_approve}
            busy={busy === "recheck"}
            canRecheck={availableActions(app).includes("recheck")}
            onRecheck={() => onAction("recheck")}
          />
          <TimelineSection events={detail.events} now={now} />
          {notes}
        </div>
      </div>
    </>
  );
}

export function CardHeader({
  app,
  wouldApprove,
  now,
}: {
  readonly app: OnboardingApplication;
  readonly wouldApprove: boolean;
  readonly now?: number;
}): JSX.Element {
  const purged = app.purged_at !== null && app.purged_at !== undefined;
  return (
    <section style={sectionStyle} data-testid="onboarding-header">
      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle} data-testid="onboarding-title">
            {app.org_name && app.org_name !== "" ? app.org_name : "Application without a name yet"}
          </h1>
          <p style={subheadingStyle}>
            {app.legal_name ? `${app.legal_name} · ` : ""}
            {app.country ?? "no country yet"}
          </p>
          <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 8 }}>
            <StatusPill status={app.status} />
            <Pill tone="muted">{sourceLabel(app.source)}</Pill>
            <Pill tone={wouldApprove ? "good" : "muted"} testId="onboarding-would-approve">
              {wouldApprove ? "System would approve" : "System would not approve yet"}
            </Pill>
          </div>
        </div>
        <dl style={contactGridStyle} data-testid="onboarding-contacts">
          <dt style={dtStyle}>Applicant</dt>
          <dd style={ddStyle}>{app.applicant_name ?? "—"}</dd>
          <dt style={dtStyle}>E-mail</dt>
          <dd style={ddStyle}>
            {app.email}{" "}
            {app.email_confirmed_at ? (
              <Pill tone="good">confirmed</Pill>
            ) : (
              <Pill tone="warn">not confirmed</Pill>
            )}
          </dd>
          <dt style={dtStyle}>Phone</dt>
          <dd style={ddStyle}>{app.phone ?? "—"}</dd>
          <dt style={dtStyle}>Telegram</dt>
          <dd style={ddStyle}>
            {typeof app.answers.telegram_username === "string" && app.answers.telegram_username !== ""
              ? `@${app.answers.telegram_username.replace(/^@/, "")}`
              : "—"}
            {app.telegram_user_id !== null && app.telegram_user_id !== undefined ? ` (id ${app.telegram_user_id})` : ""}
          </dd>
          <dt style={dtStyle}>Language</dt>
          <dd style={ddStyle}>{app.locale}</dd>
        </dl>
        <dl style={contactGridStyle}>
          <dt style={dtStyle}>Started</dt>
          <dd style={ddStyle}>{formatDateTime(app.created_at)}</dd>
          <dt style={dtStyle}>Submitted</dt>
          <dd style={ddStyle}>{formatDateTime(app.submitted_at)}</dd>
          <dt style={dtStyle}>Last activity</dt>
          <dd style={ddStyle}>{relativeAge(app.last_activity_at, now)}</dd>
          <dt style={dtStyle}>Draft expires</dt>
          <dd style={ddStyle}>{formatDateTime(app.expires_at)}</dd>
          <dt style={dtStyle}>Step</dt>
          <dd style={ddStyle}>
            {app.current_step} · {app.progress_pct}%
          </dd>
          <dt style={dtStyle}>Terms / privacy</dt>
          <dd style={ddStyle}>
            {app.terms_version ?? "—"} / {app.privacy_version ?? "—"}
          </dd>
        </dl>
      </div>

      {purged ? (
        <div style={warningStyle} data-testid="onboarding-purged">
          Personal data of this application was erased on {formatDateTime(app.purged_at)}. It cannot be decided or edited any more.
        </div>
      ) : null}

      {app.status === "info_requested" ? (
        <div style={{ ...warningStyle, marginTop: 8 }} data-testid="onboarding-info-request">
          <strong>Waiting for the applicant.</strong>{" "}
          {app.info_request_message ?? ""}
          {(app.requested_fields ?? []).length > 0 ? (
            <div style={{ marginTop: 4 }}>
              Asked about: {(app.requested_fields ?? []).map((f) => fieldLabel(f)).join(", ")}
            </div>
          ) : null}
        </div>
      ) : null}

      {app.reviewed_at ? (
        <div style={{ ...hintStyle, marginTop: 8, marginBottom: 0 }} data-testid="onboarding-decision">
          Decided {formatDateTime(app.reviewed_at)}
          {app.decision_reason ? ` — ${app.decision_reason}` : ""}.
        </div>
      ) : null}

      {app.status === "approved" && app.org_id ? (
        <div style={{ marginTop: 8 }}>
          <a href={organizationHref(app.org_id)} data-testid="onboarding-org-link">
            Open the organization
          </a>
        </div>
      ) : null}
    </section>
  );
}

const ACTION_LABELS: Readonly<Record<OnboardingAction, string>> = {
  approve: "Approve",
  request_info: "Request details",
  reject: "Reject",
  extend: "Extend",
  resend: "Resend link",
  purge: "Delete personal data",
  recheck: "Recheck",
};

/** Buttons in the order an operator reaches for them; Recheck lives in the checks section. */
const ACTION_ORDER: readonly OnboardingAction[] = ["approve", "request_info", "reject", "extend", "resend", "purge"];

export function ActionBar({
  app,
  busy,
  onAction,
}: {
  readonly app: Pick<OnboardingApplication, "status" | "purged_at">;
  readonly busy: string | null;
  readonly onAction: (a: OnboardingAction) => void;
}): JSX.Element | null {
  const allowed = availableActions(app);
  const shown = ACTION_ORDER.filter((a) => allowed.includes(a));
  if (shown.length === 0) return null;
  return (
    <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 16 }} data-testid="onboarding-actions">
      {shown.map((a) => {
        const base = a === "approve" ? primaryButtonStyle : a === "reject" || a === "purge" ? { ...buttonStyle, color: "#b91c1c", borderColor: "#fca5a5" } : buttonStyle;
        const isBusy = busy === a;
        return (
          <button
            key={a}
            type="button"
            style={busy !== null ? { ...base, ...disabledButtonStyle } : base}
            disabled={busy !== null}
            onClick={() => onAction(a)}
            data-testid={`onboarding-action-${a}`}
          >
            {isBusy ? `${ACTION_LABELS[a]}…` : ACTION_LABELS[a]}
          </button>
        );
      })}
    </div>
  );
}

export function AnswersSection({ groups, purged }: { readonly groups: readonly AnswerGroup[]; readonly purged: boolean }): JSX.Element {
  return (
    <section style={sectionStyle} data-testid="onboarding-answers">
      <h2 style={sectionTitleStyle}>Answers</h2>
      {purged ? <p style={hintStyle}>The personal fields were erased.</p> : null}
      {groups.map((g) => (
        <div key={g.id} style={{ marginBottom: 14 }} data-testid={`onboarding-block-${g.id}`}>
          <h3 style={{ margin: "0 0 6px 0", fontSize: 13, fontWeight: 600, color: "#334155" }}>{g.title}</h3>
          <dl style={{ margin: 0, display: "grid", gridTemplateColumns: "minmax(140px, 40%) 1fr", columnGap: 12, rowGap: 4, fontSize: 13 }}>
            {g.rows.map((r) => (
              <FragmentRow key={r.key} label={r.label} text={r.text} filled={r.filled} requested={r.requested} fieldKey={r.key} />
            ))}
          </dl>
        </div>
      ))}
    </section>
  );
}

function FragmentRow({
  label,
  text,
  filled,
  requested,
  fieldKey,
}: {
  readonly label: string;
  readonly text: string;
  readonly filled: boolean;
  readonly requested: boolean;
  readonly fieldKey: string;
}): JSX.Element {
  return (
    <>
      <dt style={{ color: "#64748b" }}>{label}</dt>
      <dd style={{ margin: 0, overflowWrap: "anywhere", color: filled ? "#0f172a" : "#94a3b8" }} data-testid={`onboarding-answer-${fieldKey}`}>
        {text}
        {requested ? (
          <>
            {" "}
            <Pill tone="warn">asked to fix</Pill>
          </>
        ) : null}
      </dd>
    </>
  );
}

export function ChecksSection({
  checks,
  wouldApprove,
  busy,
  canRecheck,
  onRecheck,
}: {
  readonly checks: readonly OnboardingCheck[];
  readonly wouldApprove: boolean;
  readonly busy: boolean;
  readonly canRecheck: boolean;
  readonly onRecheck: () => void;
}): JSX.Element {
  return (
    <section style={sectionStyle} data-testid="onboarding-checks">
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 8 }}>
        <h2 style={sectionTitleStyle}>Checks</h2>
        {canRecheck ? (
          <button
            type="button"
            style={busy ? { ...buttonStyle, ...disabledButtonStyle } : buttonStyle}
            disabled={busy}
            onClick={onRecheck}
            data-testid="onboarding-recheck"
          >
            {busy ? "Rechecking…" : "Recheck"}
          </button>
        ) : null}
      </div>
      <p style={hintStyle}>
        {wouldApprove ? "Every check passed: in automatic mode the system would approve this application." : "At least one check is not green."}
      </p>
      {checks.length === 0 ? <p style={mutedStyle}>No checks have run yet.</p> : null}
      <table style={tableStyle}>
        <tbody>
          {checks.map((c) => (
            <tr key={c.key} data-testid={`onboarding-check-${c.key}`}>
              <td style={{ ...tdStyle, width: 70 }}>
                <Pill tone={checkTone(c.result)}>{c.result}</Pill>
              </td>
              <td style={tdStyle}>
                <div style={{ fontWeight: 600 }}>{checkLabel(c.key)}</div>
                {c.detail ? <div style={{ fontSize: 12, color: "#64748b" }}>{c.detail}</div> : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

export function TimelineSection({ events, now }: { readonly events: readonly OnboardingEvent[]; readonly now?: number }): JSX.Element {
  return (
    <section style={sectionStyle} data-testid="onboarding-timeline">
      <h2 style={sectionTitleStyle}>Timeline</h2>
      {events.length === 0 ? <p style={mutedStyle}>Nothing has happened yet.</p> : null}
      <ol style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 }}>
        {events.map((e) => {
          const detail = eventDetailText(e);
          return (
            <li key={e.id} style={{ fontSize: 13 }} data-testid={`onboarding-event-${e.kind}`}>
              <div>
                <strong>{eventLabel(e.kind)}</strong> <span style={{ color: "#64748b" }}>· {actorText(e)}</span>
              </div>
              {detail ? <div style={{ fontSize: 12, color: "#475569" }}>{detail}</div> : null}
              <div style={{ fontSize: 11, color: "#94a3b8" }} title={formatDateTime(e.created_at)}>
                {relativeAge(e.created_at, now)} · {formatDateTime(e.created_at)}
              </div>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Notes
// ---------------------------------------------------------------------------

export function NotesList({ notes }: { readonly notes: readonly OnboardingNote[] }): JSX.Element {
  if (notes.length === 0) return <p style={mutedStyle} data-testid="onboarding-notes-empty">No notes yet.</p>;
  return (
    <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 }}>
      {notes.map((n) => (
        <li key={n.id} style={{ fontSize: 13, background: "#f8fafc", border: "1px solid #e2e8f0", borderRadius: 4, padding: 8 }} data-testid="onboarding-note">
          <div style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{n.body}</div>
          <div style={{ fontSize: 11, color: "#94a3b8", marginTop: 4 }}>{formatDateTime(n.created_at)}</div>
        </li>
      ))}
    </ul>
  );
}

function NotesPanel({
  notes,
  busy,
  onAdd,
}: {
  readonly notes: readonly OnboardingNote[];
  readonly busy: boolean;
  readonly onAdd: (body: string) => Promise<boolean>;
}): JSX.Element {
  const [body, setBody] = useState("");
  const submit = async (): Promise<void> => {
    if (body.trim() === "") return;
    if (await onAdd(body)) setBody("");
  };
  return (
    <section style={sectionStyle} data-testid="onboarding-notes">
      <h2 style={sectionTitleStyle}>Private notes</h2>
      <p style={hintStyle}>Visible to operators only; the applicant never sees them.</p>
      <NotesList notes={notes} />
      <form
        style={{ marginTop: 10 }}
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <label style={labelStyle} htmlFor="onboarding-note-body">
          Add a note
        </label>
        <textarea
          id="onboarding-note-body"
          style={{ ...inputStyle, minHeight: 60, resize: "vertical" }}
          value={body}
          maxLength={2000}
          onChange={(e) => setBody(e.target.value)}
          data-testid="onboarding-note-input"
        />
        <button
          type="submit"
          style={busy || body.trim() === "" ? { ...buttonStyle, marginTop: 6, ...disabledButtonStyle } : { ...buttonStyle, marginTop: 6 }}
          disabled={busy || body.trim() === ""}
          data-testid="onboarding-note-add"
        >
          {busy ? "Adding…" : "Add note"}
        </button>
      </form>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Dialogs
// ---------------------------------------------------------------------------

function DialogError({ error }: { readonly error: ApiError | null }): JSX.Element | null {
  if (error === null) return null;
  return (
    <div style={{ ...errorStyle, marginBottom: 0 }} role="alert" data-testid="onboarding-dialog-error">
      {error.message}
      {error.code ? <span style={{ opacity: 0.7 }}> ({error.code})</span> : null}
    </div>
  );
}

export function ApproveDialogBody({ detail }: { readonly detail: OnboardingDetail }): JSX.Element {
  const problems = checkProblems(detail.checks);
  return (
    <>
      <p style={{ margin: 0, fontSize: 13 }}>Approving this application will create, in one step:</p>
      <ul style={{ margin: 0, paddingLeft: 20, fontSize: 13, lineHeight: 1.5 }} data-testid="onboarding-approve-list">
        {approvalSummary(detail.application).map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
      {!detail.would_approve ? (
        <div style={warningStyle} data-testid="onboarding-approve-warning">
          The system would <strong>not</strong> approve this application by itself
          {problems.fail + problems.warn > 0 ? ` (${problems.fail} failed, ${problems.warn} with a warning)` : ""}. Make sure you have looked at the checks.
        </div>
      ) : null}
      <p style={{ ...hintStyle, margin: 0 }}>Approval asks for an audit reason and cannot be undone from here.</p>
    </>
  );
}

function ApproveDialog({
  detail,
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly detail: OnboardingDetail;
  readonly busy: boolean;
  readonly error: ApiError | null;
  readonly onClose: () => void;
  readonly onConfirm: () => void;
}): JSX.Element {
  return (
    <Dialog
      title="Approve application"
      testId="onboarding-dialog-approve"
      onClose={onClose}
      onSubmit={onConfirm}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose} data-testid="onboarding-dialog-cancel">
            Cancel
          </button>
          <button
            type="submit"
            style={busy ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
            disabled={busy}
            data-testid="onboarding-dialog-confirm"
          >
            {busy ? "Approving…" : "Approve and create"}
          </button>
        </>
      }
    >
      <ApproveDialogBody detail={detail} />
      <DialogError error={error} />
    </Dialog>
  );
}

function RejectDialog({
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly busy: boolean;
  readonly error: ApiError | null;
  readonly onClose: () => void;
  readonly onConfirm: (reason: string, message: string) => void;
}): JSX.Element {
  const [reason, setReason] = useState("");
  const [message, setMessage] = useState("");
  const [localErrors, setLocalErrors] = useState<Record<string, string>>({});
  const errors = { ...serverFieldErrors(error?.details), ...localErrors };
  return (
    <Dialog
      title="Reject application"
      testId="onboarding-dialog-reject"
      onClose={onClose}
      onSubmit={() => {
        const found = validateReject({ reason, message });
        setLocalErrors(found);
        if (Object.keys(found).length === 0) onConfirm(reason, message);
      }}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose} data-testid="onboarding-dialog-cancel">
            Cancel
          </button>
          <button type="submit" style={busy ? { ...dangerButtonStyle, ...disabledButtonStyle } : dangerButtonStyle} disabled={busy} data-testid="onboarding-dialog-confirm">
            {busy ? "Rejecting…" : "Reject"}
          </button>
        </>
      }
    >
      <div>
        <label style={labelStyle} htmlFor="onboarding-reject-reason">
          Reason (required, kept in the record)
        </label>
        <textarea
          id="onboarding-reject-reason"
          style={{ ...inputStyle, minHeight: 60 }}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          aria-invalid={errors.reason !== undefined}
          data-testid="onboarding-reject-reason"
        />
        {errors.reason ? <p style={fieldErrorStyle} data-testid="onboarding-reject-reason-error">{errors.reason === "required" ? "Give the reason for the rejection." : errors.reason}</p> : null}
      </div>
      <div>
        <label style={labelStyle} htmlFor="onboarding-reject-message">
          Message to the applicant (optional)
        </label>
        <textarea
          id="onboarding-reject-message"
          style={{ ...inputStyle, minHeight: 60 }}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          data-testid="onboarding-reject-message"
        />
        <p style={{ ...hintStyle, margin: "4px 0 0 0" }}>
          Sent in the rejection e-mail when the applicant confirmed their address. The reason above is not sent.
        </p>
      </div>
      <DialogError error={error} />
    </Dialog>
  );
}

function RequestInfoDialog({
  application,
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly application: OnboardingApplication;
  readonly busy: boolean;
  readonly error: ApiError | null;
  readonly onClose: () => void;
  readonly onConfirm: (fields: readonly string[], message: string) => void;
}): JSX.Element {
  const [fields, setFields] = useState<readonly string[]>([]);
  const [message, setMessage] = useState("");
  const [localErrors, setLocalErrors] = useState<Record<string, string>>({});
  const errors = { ...serverFieldErrors(error?.details), ...localErrors };
  const toggle = (key: string): void =>
    setFields((cur) => (cur.includes(key) ? cur.filter((k) => k !== key) : [...cur, key]));
  return (
    <Dialog
      title="Request details"
      testId="onboarding-dialog-request-info"
      onClose={onClose}
      onSubmit={() => {
        const found = validateRequestInfo({ fields, message });
        setLocalErrors(found);
        if (Object.keys(found).length === 0) onConfirm(fields, message);
      }}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose} data-testid="onboarding-dialog-cancel">
            Cancel
          </button>
          <button type="submit" style={busy ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle} disabled={busy} data-testid="onboarding-dialog-confirm">
            {busy ? "Sending…" : "Send request"}
          </button>
        </>
      }
    >
      <fieldset style={{ border: "1px solid #e2e8f0", borderRadius: 4, padding: 10, margin: 0 }}>
        <legend style={{ ...labelStyle, padding: "0 4px", marginBottom: 0 }}>Fields to ask about</legend>
        <div style={{ maxHeight: 220, overflowY: "auto", display: "flex", flexDirection: "column", gap: 8 }}>
          {FORM_BLOCKS.map((b) => (
            <div key={b.id}>
              <div style={{ fontSize: 11, fontWeight: 600, color: "#64748b", textTransform: "uppercase", letterSpacing: 0.4 }}>{b.title}</div>
              {b.fields
                .filter((k) => REQUESTABLE_FIELDS.includes(k))
                .map((k) => {
                  const filled = application.answers[k] !== undefined && application.answers[k] !== null && application.answers[k] !== "";
                  return (
                    <label key={k} style={{ display: "flex", gap: 6, fontSize: 13, alignItems: "center" }}>
                      <input
                        type="checkbox"
                        checked={fields.includes(k)}
                        onChange={() => toggle(k)}
                        data-testid={`onboarding-info-field-${k}`}
                      />
                      <span>{fieldLabel(k)}</span>
                      {filled ? null : <span style={{ color: "#94a3b8", fontSize: 11 }}>(empty)</span>}
                    </label>
                  );
                })}
            </div>
          ))}
        </div>
        {errors.fields ? (
          <p style={fieldErrorStyle} data-testid="onboarding-info-fields-error">
            {errors.fields === "required" || errors.fields === "invalid" ? "Pick at least one field to ask about." : errors.fields}
          </p>
        ) : null}
      </fieldset>
      <div>
        <label style={labelStyle} htmlFor="onboarding-info-message">
          Message to the applicant (required)
        </label>
        <textarea
          id="onboarding-info-message"
          style={{ ...inputStyle, minHeight: 70 }}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          aria-invalid={errors.message !== undefined}
          data-testid="onboarding-info-message"
        />
        {errors.message ? (
          <p style={fieldErrorStyle} data-testid="onboarding-info-message-error">
            {errors.message === "required" ? "Write what the applicant should add or correct." : errors.message}
          </p>
        ) : null}
        <p style={{ ...hintStyle, margin: "4px 0 0 0" }}>The applicant gets this text with a fresh link to the form and can change only the fields you picked.</p>
      </div>
      <DialogError error={error} />
    </Dialog>
  );
}

function ExtendDialog({
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly busy: boolean;
  readonly error: ApiError | null;
  readonly onClose: () => void;
  readonly onConfirm: (days: number | undefined) => void;
}): JSX.Element {
  const [raw, setRaw] = useState("");
  const [localError, setLocalError] = useState<string | null>(null);
  return (
    <Dialog
      title="Extend the draft"
      testId="onboarding-dialog-extend"
      onClose={onClose}
      onSubmit={() => {
        const parsed = validateExtendDays(raw);
        setLocalError(parsed.error ?? null);
        if (parsed.error === undefined) onConfirm(parsed.days);
      }}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose} data-testid="onboarding-dialog-cancel">
            Cancel
          </button>
          <button type="submit" style={busy ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle} disabled={busy} data-testid="onboarding-dialog-confirm">
            {busy ? "Extending…" : "Extend"}
          </button>
        </>
      }
    >
      <div>
        <label style={labelStyle} htmlFor="onboarding-extend-days">
          Days from now
        </label>
        <input
          id="onboarding-extend-days"
          style={{ ...inputStyle, maxWidth: 140 }}
          inputMode="numeric"
          value={raw}
          onChange={(e) => setRaw(e.target.value)}
          placeholder="default"
          data-testid="onboarding-extend-days"
        />
        <p style={{ ...hintStyle, margin: "4px 0 0 0" }}>
          Leave empty for the standard draft lifetime from the settings. An expired draft becomes a draft again.
        </p>
        {localError ? <p style={fieldErrorStyle} data-testid="onboarding-extend-error">{localError}</p> : null}
      </div>
      <DialogError error={error} />
    </Dialog>
  );
}

function PurgeDialog({
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly busy: boolean;
  readonly error: ApiError | null;
  readonly onClose: () => void;
  readonly onConfirm: () => void;
}): JSX.Element {
  return (
    <Dialog
      title="Delete personal data"
      testId="onboarding-dialog-purge"
      onClose={onClose}
      onSubmit={onConfirm}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose} data-testid="onboarding-dialog-cancel">
            Cancel
          </button>
          <button type="submit" style={busy ? { ...dangerButtonStyle, ...disabledButtonStyle } : dangerButtonStyle} disabled={busy} data-testid="onboarding-dialog-confirm">
            {busy ? "Deleting…" : "Delete personal data"}
          </button>
        </>
      }
    >
      <p style={{ margin: 0, fontSize: 13 }}>
        This erases the applicant's name, e-mail, phone and every answer. The row stays with its status and decision
        reason for statistics, and the timeline records the deletion. It cannot be undone.
      </p>
      <DialogError error={error} />
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Styles local to the header
// ---------------------------------------------------------------------------

const contactGridStyle = {
  margin: 0,
  display: "grid",
  gridTemplateColumns: "max-content 1fr",
  columnGap: 12,
  rowGap: 4,
  fontSize: 13,
} as const;

const dtStyle = { color: "#64748b" } as const;

const ddStyle = { margin: 0, overflowWrap: "anywhere" } as const;
