/**
 * The tab "Waiting for verification" of the Seating plans screen (spec 64, section 3.4).
 *
 *   GET  /v1/admin/seating-plans?verification_state=unverified|needs_fix|verified
 *   POST /v1/admin/seating-plans/{id}/verify | needs-fix
 *
 * The filter "No state (imported)" lists plans that the server never lists in one call
 * (see lib/api/venuePlans.ts, "No state"): the operator picks an organization, and the
 * page reads the plans of the organization's venues a few at a time.
 *
 * Mock data: NONE. Everything here comes from the live backend.
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { ResponsiveTable, type ResponsiveTableColumn } from "@/components/layout";
import { ApiError, authedFetch } from "@/lib/api/client";
import {
  NO_STATE_FILTER,
  VERIFICATION_STATES,
  fetchPlanPreviewUrl,
  listPlansByVerification,
  loadStatedPlanIds,
  planNeedsFix,
  rowFromAdminPlan,
  rowFromNoState,
  scanNoStatePlans,
  verificationLabel,
  verifyErrorMessage,
  verifyPlan,
  type VenueRef,
  type VerificationFilter,
  type VerificationRow,
} from "@/lib/api/venuePlans";
import type { BatchProgress } from "@/lib/admin/batch";
import { formatDateTime } from "@/lib/admin/supportConsole";
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
  primaryButtonStyle,
  sectionStyle,
  sectionTitleStyle,
  warningStyle,
} from "./onboardingUi";

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

export const NEEDS_FIX_NOTE_MAX = 1000;

/** The note of Needs fix: 1 to 1000 characters after trimming. */
export function validateNeedsFixNote(note: string): string | null {
  const t = note.trim();
  if (t === "") return "Write the correction that the owner must make.";
  if (t.length > NEEDS_FIX_NOTE_MAX) return `Write up to ${NEEDS_FIX_NOTE_MAX} characters.`;
  return null;
}

export function verificationTone(state: string | null): "good" | "warn" | "bad" | "muted" {
  switch (state) {
    case "verified":
      return "good";
    case "needs_fix":
      return "bad";
    case "unverified":
      return "warn";
    default:
      return "muted";
  }
}

export const FILTER_OPTIONS: readonly VerificationFilter[] = [...VERIFICATION_STATES, NO_STATE_FILTER];

// ---------------------------------------------------------------------------
// Table (presentational)
// ---------------------------------------------------------------------------

export interface VerificationTableProps {
  readonly rows: readonly VerificationRow[];
  readonly selectedId: string | null;
  readonly busyId: string | null;
  readonly onPreview: (row: VerificationRow) => void;
  readonly onVerify: (row: VerificationRow) => void;
  readonly onNeedsFix: (row: VerificationRow) => void;
  readonly forceLayout?: "desktop" | "mobile";
}

export function VerificationTable({
  rows,
  selectedId,
  busyId,
  onPreview,
  onVerify,
  onNeedsFix,
  forceLayout,
}: VerificationTableProps): JSX.Element {
  const columns: ResponsiveTableColumn<VerificationRow>[] = [
    {
      id: "plan",
      header: "Plan",
      primary: true,
      renderCell: (r) => (
        <div data-testid={`venue-plans-verify-row-${r.planId}`}>
          <div style={{ fontWeight: 600 }}>{r.name}</div>
          <Pill tone={verificationTone(r.state)}>{verificationLabel(r.state)}</Pill>
          {r.note ? <div style={{ fontSize: 12, color: "#991b1b", marginTop: 2 }}>Note: {r.note}</div> : null}
        </div>
      ),
    },
    { id: "organization", header: "Organization", renderCell: (r) => r.orgName },
    { id: "venue", header: "Venue", renderCell: (r) => r.venueName },
    {
      id: "seats",
      header: "Seats",
      align: "right",
      renderCell: (r) => (r.seats === null ? "—" : r.seats.toLocaleString("en-US")),
    },
    { id: "created", header: "Created", renderCell: (r) => formatDateTime(r.createdAt) },
    {
      id: "actions",
      header: "",
      renderCell: (r) => {
        const busy = busyId === r.planId;
        const verified = r.state === "verified";
        return (
          <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
            <button
              type="button"
              style={r.planId === selectedId ? primaryButtonStyle : buttonStyle}
              onClick={() => onPreview(r)}
              data-testid={`venue-plans-preview-${r.planId}`}
            >
              Preview
            </button>
            <button
              type="button"
              style={busy || verified ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
              disabled={busy || verified}
              title={verified ? "The plan is verified." : undefined}
              onClick={() => onVerify(r)}
              data-testid={`venue-plans-verify-${r.planId}`}
            >
              Verify
            </button>
            <button
              type="button"
              style={busy ? { ...buttonStyle, ...disabledButtonStyle } : buttonStyle}
              disabled={busy}
              onClick={() => onNeedsFix(r)}
              data-testid={`venue-plans-needsfix-${r.planId}`}
            >
              Correction necessary
            </button>
          </div>
        );
      },
    },
  ];
  return (
    <ResponsiveTable<VerificationRow>
      id="venue-plans-verify-table"
      caption="Seating plans"
      columns={columns}
      rows={rows}
      rowKey={(r) => r.planId}
      forceLayout={forceLayout}
      empty="No plans with this status."
    />
  );
}

// ---------------------------------------------------------------------------
// Preview: the image of the current version, next to the list
// ---------------------------------------------------------------------------

export function PlanPreviewBox({
  row,
  url,
  loading,
  error,
}: {
  readonly row: VerificationRow;
  readonly url: string | null | undefined;
  readonly loading: boolean;
  readonly error: ApiError | null;
}): JSX.Element {
  return (
    <div style={sectionStyle} data-testid="venue-plans-preview-box">
      <div style={sectionTitleStyle}>
        {row.name} · {row.venueName}
      </div>
      {loading ? <p style={mutedStyle}>Loading the image…</p> : null}
      {error !== null ? <div style={errorStyle}>We did not load the image. Reload the page. {error.message}</div> : null}
      {!loading && error === null && url ? (
        <img
          src={url}
          alt={`Seating plan ${row.name}`}
          style={{ maxWidth: "100%", maxHeight: 520, display: "block", border: "1px solid #e2e8f0", background: "#ffffff" }}
          data-testid="venue-plans-preview-img"
        />
      ) : null}
      {!loading && error === null && !url ? (
        <p style={hintStyle}>The version has no stored SVG. Open the plan on the Venues screen to see its geometry.</p>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Dialog: Needs fix
// ---------------------------------------------------------------------------

function NeedsFixDialog({
  row,
  busy,
  error,
  onClose,
  onConfirm,
}: {
  readonly row: VerificationRow;
  readonly busy: boolean;
  readonly error: string | null;
  readonly onClose: () => void;
  readonly onConfirm: (note: string) => void;
}): JSX.Element {
  const [note, setNote] = useState("");
  const [touched, setTouched] = useState(false);
  const problem = validateNeedsFixNote(note);
  return (
    <Dialog
      title={`Correction necessary: ${row.name}`}
      testId="venue-plans-needsfix-dialog"
      onClose={onClose}
      onSubmit={() => {
        setTouched(true);
        if (problem === null) onConfirm(note);
      }}
      footer={
        <>
          <button type="button" style={buttonStyle} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" style={busy ? { ...dangerButtonStyle, ...disabledButtonStyle } : dangerButtonStyle} disabled={busy} data-testid="venue-plans-needsfix-confirm">
            {busy ? "Saving…" : "Send back for correction"}
          </button>
        </>
      }
    >
      <p style={hintStyle}>
        The plan goes back to private. No other organization can copy it. Copies stay in use. The owner sees the
        note.
      </p>
      <label style={labelStyle} htmlFor="venue-plans-needsfix-note">
        Correction necessary
      </label>
      <textarea
        id="venue-plans-needsfix-note"
        value={note}
        onChange={(e) => setNote(e.target.value)}
        rows={4}
        style={inputStyle}
        data-testid="venue-plans-needsfix-note"
      />
      {touched && problem !== null ? <p style={fieldErrorStyle}>{problem}</p> : null}
      {error !== null ? (
        <div style={errorStyle} data-testid="venue-plans-needsfix-error">
          {error}
        </div>
      ) : null}
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Panel (stateful)
// ---------------------------------------------------------------------------

interface OrganizationItem {
  readonly id: string;
  readonly name: string;
}

export function VerificationPanel(): JSX.Element {
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState<VerificationFilter>("unverified");
  const [selected, setSelected] = useState<VerificationRow | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [needsFixRow, setNeedsFixRow] = useState<VerificationRow | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);

  // "No state (imported)"
  const [orgId, setOrgId] = useState("");
  const [noStateRows, setNoStateRows] = useState<readonly VerificationRow[]>([]);
  const [scanProgress, setScanProgress] = useState<BatchProgress | null>(null);
  const [scanning, setScanning] = useState(false);
  const [scanNotes, setScanNotes] = useState<readonly string[]>([]);
  const [scanError, setScanError] = useState<string | null>(null);
  const stopRef = useRef(false);

  const isNone = filter === NO_STATE_FILTER;

  const listQuery = useQuery<readonly VerificationRow[], ApiError>({
    queryKey: ["admin", "venue-plans", "verification", filter],
    queryFn: async () => {
      if (filter === NO_STATE_FILTER) return [];
      const rows = await listPlansByVerification(filter);
      return rows.map(rowFromAdminPlan);
    },
    enabled: !isNone,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const orgsQuery = useQuery<readonly OrganizationItem[], ApiError>({
    queryKey: ["admin", "venue-plans", "organizations"],
    queryFn: async () => {
      const res = await authedFetch<{ organizations: OrganizationItem[] }>({ method: "GET", path: "/v1/organizations" });
      return [...res.organizations].sort((a, b) => a.name.localeCompare(b.name));
    },
    enabled: isNone,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const previewQuery = useQuery<string | null, ApiError>({
    queryKey: ["admin", "venue-plans", "preview", selected?.planId ?? "", selected?.currentVersionId ?? ""],
    queryFn: () => fetchPlanPreviewUrl(selected?.planId ?? "", selected?.currentVersionId ?? null),
    enabled: selected !== null,
    refetchOnWindowFocus: false,
    retry: false,
    staleTime: 5 * 60 * 1000,
  });

  const rows: readonly VerificationRow[] = isNone ? noStateRows : (listQuery.data ?? []);

  const afterChange = async (row: VerificationRow): Promise<void> => {
    if (isNone) {
      setNoStateRows((prev) => prev.filter((r) => r.planId !== row.planId));
    }
    if (selected?.planId === row.planId) setSelected(null);
    await queryClient.invalidateQueries({ queryKey: ["admin", "venue-plans", "verification"] });
  };

  const onVerify = async (row: VerificationRow): Promise<void> => {
    setBusyId(row.planId);
    setActionError(null);
    setMessage(null);
    try {
      const res = await verifyPlan(row.planId);
      setMessage(
        res.changed
          ? `Verified “${row.name}”. It is the template of ${row.venueName}.`
          : `“${row.name}” is verified. No change.`,
      );
      await afterChange(row);
    } catch (err) {
      setActionError(err instanceof ApiError ? verifyErrorMessage(err) : String(err));
    } finally {
      setBusyId(null);
    }
  };

  const onNeedsFix = async (row: VerificationRow, note: string): Promise<void> => {
    setBusyId(row.planId);
    setDialogError(null);
    try {
      await planNeedsFix(row.planId, note);
      setMessage(`Sent “${row.name}” back for correction.`);
      setNeedsFixRow(null);
      await afterChange(row);
    } catch (err) {
      setDialogError(err instanceof ApiError ? verifyErrorMessage(err) : String(err));
    } finally {
      setBusyId(null);
    }
  };

  const onScan = async (): Promise<void> => {
    if (orgId === "") return;
    stopRef.current = false;
    setScanning(true);
    setScanError(null);
    setScanNotes([]);
    setNoStateRows([]);
    setScanProgress(null);
    try {
      const [venuesRes, stated] = await Promise.all([
        authedFetch<{ venues: VenueRef[] }>({ method: "GET", path: "/v1/venues" }),
        loadStatedPlanIds(),
      ]);
      const orgName = orgsQuery.data?.find((o) => o.id === orgId)?.name ?? orgId.slice(0, 8);
      const result = await scanNoStatePlans({
        orgId,
        venues: venuesRes.venues,
        stated: stated.ids,
        concurrency: 3,
        onProgress: setScanProgress,
        shouldStop: () => stopRef.current,
      });
      setNoStateRows(result.plans.map((p) => rowFromNoState(p, orgName)));
      const notes: string[] = [];
      if (stated.truncated) {
        notes.push("One of the lists of plans with a status has more than 200 plans. The list shows 200 plans. A plan with a status can show here too. Verify works on it too.");
      }
      if (result.failedVenues.length > 0) {
        notes.push(`We cannot read the plans of:${result.failedVenues.map((v) => v.name).join(", ")}.`);
      }
      if (result.stopped) notes.push("You stopped the scan. The list is not complete.");
      setScanNotes(notes);
    } catch (err) {
      setScanError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setScanning(false);
    }
  };

  return (
    <div data-testid="venue-plans-verification">
      <div style={{ display: "flex", gap: 12, alignItems: "flex-end", flexWrap: "wrap", marginBottom: 12 }}>
        <label style={{ fontSize: 12, fontWeight: 600, color: "#334155" }}>
          <span style={{ display: "block", marginBottom: 4 }}>Status of the plan</span>
          <select
            value={filter}
            onChange={(e) => {
              setFilter(e.target.value as VerificationFilter);
              setSelected(null);
              setMessage(null);
              setActionError(null);
            }}
            style={{ ...inputStyle, width: 240 }}
            data-testid="venue-plans-verify-filter"
          >
            {FILTER_OPTIONS.map((f) => (
              <option key={f} value={f}>
                {verificationLabel(f === NO_STATE_FILTER ? null : f)}
              </option>
            ))}
          </select>
        </label>
        {isNone ? (
          <>
            <label style={{ fontSize: 12, fontWeight: 600, color: "#334155" }}>
              <span style={{ display: "block", marginBottom: 4 }}>Organization</span>
              <select
                value={orgId}
                onChange={(e) => setOrgId(e.target.value)}
                style={{ ...inputStyle, width: 280 }}
                data-testid="venue-plans-verify-org"
              >
                <option value="">Select an organization</option>
                {(orgsQuery.data ?? []).map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.name}
                  </option>
                ))}
              </select>
              {orgsQuery.isError ? (
                <span role="alert" style={{ display: "block", marginTop: 4 }} data-testid="venue-plans-verify-org-error">
                  The list of organizations did not load. Reload the page.
                </span>
              ) : null}
            </label>
            <button
              type="button"
              style={orgId === "" || scanning ? { ...primaryButtonStyle, ...disabledButtonStyle } : primaryButtonStyle}
              disabled={orgId === "" || scanning}
              onClick={() => void onScan()}
              data-testid="venue-plans-scan"
            >
              {scanning ? "Reading…" : "Find plans"}
            </button>
            {scanning ? (
              <button type="button" style={buttonStyle} onClick={() => { stopRef.current = true; }} data-testid="venue-plans-scan-stop">
                Stop
              </button>
            ) : null}
          </>
        ) : (
          <button type="button" style={buttonStyle} onClick={() => void listQuery.refetch()} data-testid="venue-plans-verify-reload">
            Reload
          </button>
        )}
      </div>

      {isNone ? (
        <p style={hintStyle}>
          The server list does not show plans with no status (imported plans). Select
          an organization. Then the page reads the plans of its venues. Verify works on these plans too.
        </p>
      ) : null}
      {isNone && scanProgress !== null ? (
        <p style={mutedStyle} data-testid="venue-plans-scan-progress">
          Read {scanProgress.completed} of {scanProgress.total} venues
          {scanProgress.failed > 0 ? `, ${scanProgress.failed} failed` : ""}.
        </p>
      ) : null}
      {scanError !== null ? <div style={errorStyle}>We did not read the plans: {scanError}</div> : null}
      {scanNotes.map((n) => (
        <div key={n} style={{ ...warningStyle, marginBottom: 8 }}>
          {n}
        </div>
      ))}

      {message !== null ? (
        <div style={noticeStyle} data-testid="venue-plans-verify-notice">
          {message}
        </div>
      ) : null}
      {actionError !== null ? (
        <div style={errorStyle} data-testid="venue-plans-verify-error">
          {actionError}
        </div>
      ) : null}
      {!isNone && listQuery.isLoading ? <p style={mutedStyle}>Loading…</p> : null}
      {!isNone && listQuery.error ? (
        <div style={errorStyle} data-testid="venue-plans-verify-load-error">
          We did not load the plans: {listQuery.error.message}
        </div>
      ) : null}

      {isNone && noStateRows.length === 0 && scanProgress === null ? null : (
        <div style={{ display: "grid", gridTemplateColumns: selected !== null ? "minmax(0, 3fr) minmax(0, 2fr)" : "minmax(0, 1fr)", gap: 16, alignItems: "start" }}>
          <VerificationTable
            rows={rows}
            selectedId={selected?.planId ?? null}
            busyId={busyId}
            onPreview={(r) => setSelected(r)}
            onVerify={(r) => void onVerify(r)}
            onNeedsFix={(r) => {
              setDialogError(null);
              setNeedsFixRow(r);
            }}
          />
          {selected !== null ? (
            <PlanPreviewBox
              row={selected}
              url={previewQuery.data}
              loading={previewQuery.isLoading}
              error={previewQuery.error ?? null}
            />
          ) : null}
        </div>
      )}

      {needsFixRow !== null ? (
        <NeedsFixDialog
          row={needsFixRow}
          busy={busyId === needsFixRow.planId}
          error={dialogError}
          onClose={() => setNeedsFixRow(null)}
          onConfirm={(note) => void onNeedsFix(needsFixRow, note)}
        />
      ) : null}
    </div>
  );
}
