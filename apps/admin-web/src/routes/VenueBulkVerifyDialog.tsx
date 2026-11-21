/**
 * Confirmation of the bulk "mark as verified" action on the Venues screen. It says how many
 * venues change, has its own audit reason (empty every time the dialog opens, Latin letters
 * only) and sends nothing until the administrator confirms.
 */
import { useState, type CSSProperties } from "react";
import { isLatinReason } from "@/lib/admin/venueBulk";

export function VenueBulkVerifyDialog({
  count,
  onCancel,
  onConfirm,
}: {
  readonly count: number;
  readonly onCancel: () => void;
  readonly onConfirm: (reason: string) => void;
}) {
  // The dialog is mounted only while open, so the reason starts empty every time.
  const [reason, setReason] = useState("");
  return (
    <VenueBulkVerifyDialogView
      count={count}
      reason={reason}
      onReasonChange={setReason}
      onCancel={onCancel}
      onConfirm={onConfirm}
    />
  );
}

/** The stateless half of the dialog. */
export function VenueBulkVerifyDialogView({
  count,
  reason,
  onReasonChange,
  onCancel,
  onConfirm,
}: {
  readonly count: number;
  readonly reason: string;
  readonly onReasonChange: (reason: string) => void;
  readonly onCancel: () => void;
  readonly onConfirm: (reason: string) => void;
}) {
  const valid = isLatinReason(reason);
  const showProblem = reason.trim() !== "" && !valid;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="venues-bulk-dialog-title"
      style={backdropStyle}
      data-testid="venues-bulk-dialog"
    >
      <div style={dialogStyle}>
        <header style={headerStyle}>
          <h2 id="venues-bulk-dialog-title" style={titleStyle}>
            Mark venues as verified
          </h2>
        </header>
        <div style={bodyStyle}>
          <p style={paraStyle} data-testid="venues-bulk-dialog-summary">
            {count === 1
              ? "You mark 1 venue as verified."
              : `You mark ${count} venues as verified.`}{" "}
            The new status is <strong>verified</strong>. Each organization can then add sessions at
            these venues.
          </p>
          <label style={labelStyle} htmlFor="venues-bulk-dialog-reason">
            Audit reason
          </label>
          <input
            id="venues-bulk-dialog-reason"
            type="text"
            value={reason}
            onChange={(e) => onReasonChange(e.target.value)}
            autoComplete="off"
            autoFocus
            style={inputStyle}
            data-testid="venues-bulk-dialog-reason"
          />
          <span style={hintStyle}>
            Write the reason in Latin letters. The audit log keeps it for all {count} venues.
          </span>
          {showProblem ? (
            <span style={problemStyle} role="alert" data-testid="venues-bulk-dialog-problem">
              Use Latin letters only. Remove other characters.
            </span>
          ) : null}
          <div style={actionsStyle}>
            <button
              type="button"
              onClick={onCancel}
              style={secondaryStyle}
              data-testid="venues-bulk-dialog-cancel"
            >
              Cancel
            </button>
            <button
              type="button"
              disabled={!valid}
              onClick={() => onConfirm(reason.trim())}
              style={valid ? primaryStyle : { ...primaryStyle, opacity: 0.5, cursor: "not-allowed" }}
              data-testid="venues-bulk-dialog-confirm"
            >
              Mark as verified
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

const backdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  padding: 16,
  zIndex: 100,
};

const dialogStyle: CSSProperties = {
  background: "#ffffff",
  borderRadius: 8,
  border: "1px solid #e2e8f0",
  boxShadow: "0 10px 25px rgba(15, 23, 42, 0.2)",
  width: "min(520px, 100%)",
  maxHeight: "90vh",
  overflowY: "auto",
};

const headerStyle: CSSProperties = {
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
};

const titleStyle: CSSProperties = { margin: 0, fontSize: 16, fontWeight: 600, color: "#0f172a" };

const bodyStyle: CSSProperties = { display: "flex", flexDirection: "column", gap: 12, padding: 16 };

const paraStyle: CSSProperties = { margin: 0, fontSize: 13, color: "#334155", lineHeight: 1.5 };

const labelStyle: CSSProperties = { fontSize: 12, fontWeight: 600, color: "#334155" };

const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const hintStyle: CSSProperties = { fontSize: 11, color: "#64748b", lineHeight: 1.4 };

const problemStyle: CSSProperties = { fontSize: 11, color: "#b91c1c", fontWeight: 500 };

const actionsStyle: CSSProperties = { display: "flex", justifyContent: "flex-end", gap: 8 };

const primaryStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const secondaryStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};
