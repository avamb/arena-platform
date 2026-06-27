/**
 * <ActiveReasonBadge /> -- shell-header indicator for SAUI-04.
 *
 * Renders the currently active audit reason for cross-tenant reads,
 * with a "Change" action that opens the prompt modal so the operator
 * can replace the value before the next request. When no reason is
 * active, the badge displays "not set" and the action label flips to
 * "Set reason" so the affordance is obvious.
 *
 * The badge intentionally truncates long reasons to one line; the full
 * value is in the title attribute for tooltip-on-hover.
 */
import type { CSSProperties } from "react";
import { useReason } from "@/lib/auth/ReasonContext";

const MAX_INLINE_LEN = 48;

function truncate(s: string, max: number): string {
  if (s.length <= max) {
    return s;
  }
  return `${s.slice(0, max - 1).trimEnd()}…`;
}

export function ActiveReasonBadge() {
  const { activeReason, openPrompt, clearReason } = useReason();
  const hasReason = activeReason !== null;
  return (
    <div
      style={containerStyle}
      data-testid="active-reason-badge"
      data-has-reason={hasReason ? "true" : "false"}
    >
      <span style={labelStyle}>Audit reason</span>
      <span
        style={hasReason ? valueStyle : valueEmptyStyle}
        title={activeReason ?? "No audit reason captured this session."}
        data-testid="active-reason-value"
      >
        {hasReason ? truncate(activeReason ?? "", MAX_INLINE_LEN) : "not set"}
      </span>
      <button
        type="button"
        onClick={openPrompt}
        style={changeBtnStyle}
        data-testid="active-reason-change"
      >
        {hasReason ? "Change" : "Set reason"}
      </button>
      {hasReason ? (
        <button
          type="button"
          onClick={clearReason}
          style={clearBtnStyle}
          title="Discard the active reason; the next cross-tenant request will re-prompt."
          data-testid="active-reason-clear"
        >
          Clear
        </button>
      ) : null}
    </div>
  );
}

const containerStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 8,
  padding: "4px 8px",
  background: "#fef3c7",
  border: "1px solid #fcd34d",
  borderRadius: 4,
  fontSize: 12,
  color: "#78350f",
  maxWidth: "60%",
};
const labelStyle: CSSProperties = {
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
  fontSize: 10,
  color: "#92400e",
  flexShrink: 0,
};
const valueStyle: CSSProperties = {
  color: "#0f172a",
  whiteSpace: "nowrap",
  overflow: "hidden",
  textOverflow: "ellipsis",
  maxWidth: 360,
};
const valueEmptyStyle: CSSProperties = {
  ...valueStyle,
  color: "#9a3412",
  fontStyle: "italic",
};
const changeBtnStyle: CSSProperties = {
  background: "#0f172a",
  color: "#fff",
  border: 0,
  padding: "3px 8px",
  fontSize: 11,
  borderRadius: 3,
  cursor: "pointer",
};
const clearBtnStyle: CSSProperties = {
  background: "transparent",
  color: "#78350f",
  border: "1px solid #fcd34d",
  padding: "2px 6px",
  fontSize: 11,
  borderRadius: 3,
  cursor: "pointer",
};
