/**
 * Small presentational pieces shared by the Onboarding screens (APP-09):
 * the queue/settings page (onboarding.tsx) and the application card
 * (onboardingDetail.tsx). Router- and query-free, so the Node-only Vitest
 * environment can render them with renderToStaticMarkup.
 */
import {
  useRef,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
import { useEscapeClose, useFocusOnMount, useFocusRestore, useFocusTrap } from "@/lib/a11y";
import { statusLabel } from "@/lib/api/onboarding";

// ---------------------------------------------------------------------------
// Styles (inline, like the rest of admin-web)
// ---------------------------------------------------------------------------

export const pageStyle: CSSProperties = { padding: 24, maxWidth: 1280, color: "#0f172a" };

export const headerRowStyle: CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  alignItems: "flex-start",
  gap: 16,
  flexWrap: "wrap",
  marginBottom: 16,
};

export const headingStyle: CSSProperties = { margin: 0, fontSize: 22, fontWeight: 600, letterSpacing: -0.2 };

export const subheadingStyle: CSSProperties = { margin: "4px 0 0 0", fontSize: 13, color: "#475569", lineHeight: 1.45 };

export const sectionStyle: CSSProperties = {
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  padding: 16,
  marginBottom: 16,
};

export const sectionTitleStyle: CSSProperties = { margin: "0 0 4px 0", fontSize: 15, fontWeight: 600 };

export const hintStyle: CSSProperties = { margin: "0 0 12px 0", fontSize: 12, color: "#64748b", lineHeight: 1.45, maxWidth: 900 };

export const mutedStyle: CSSProperties = { margin: 0, fontSize: 13, color: "#64748b" };

export const tableStyle: CSSProperties = { width: "100%", borderCollapse: "collapse", fontSize: 13 };

export const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "8px 10px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
  whiteSpace: "nowrap",
};

export const tdStyle: CSSProperties = { padding: "8px 10px", borderBottom: "1px solid #f1f5f9", verticalAlign: "top" };

export const buttonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

export const primaryButtonStyle: CSSProperties = {
  ...buttonStyle,
  background: "#0369a1",
  borderColor: "#0369a1",
  color: "#ffffff",
};

export const dangerButtonStyle: CSSProperties = {
  ...buttonStyle,
  background: "#b91c1c",
  borderColor: "#b91c1c",
  color: "#ffffff",
};

export const disabledButtonStyle: CSSProperties = { opacity: 0.55, cursor: "not-allowed" };

export const linkButtonStyle: CSSProperties = { ...buttonStyle, textDecoration: "none", display: "inline-block" };

export const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
  width: "100%",
  boxSizing: "border-box",
};

export const labelStyle: CSSProperties = { display: "block", fontSize: 12, fontWeight: 600, color: "#334155", marginBottom: 4 };

export const fieldErrorStyle: CSSProperties = { margin: "4px 0 0 0", fontSize: 12, color: "#b91c1c" };

export const errorStyle: CSSProperties = {
  padding: "10px 12px",
  background: "#fef2f2",
  border: "1px solid #fecaca",
  borderRadius: 6,
  color: "#991b1b",
  fontSize: 13,
  marginBottom: 16,
};

export const noticeStyle: CSSProperties = {
  padding: "10px 12px",
  background: "#f0fdf4",
  border: "1px solid #bbf7d0",
  borderRadius: 6,
  color: "#166534",
  fontSize: 13,
  marginBottom: 16,
};

export const warningStyle: CSSProperties = {
  padding: "10px 12px",
  background: "#fffbeb",
  border: "1px solid #fde68a",
  borderRadius: 6,
  color: "#92400e",
  fontSize: 13,
  lineHeight: 1.45,
};

// ---------------------------------------------------------------------------
// Pills
// ---------------------------------------------------------------------------

type Tone = "good" | "warn" | "bad" | "info" | "muted";

const TONES: Readonly<Record<Tone, { bg: string; fg: string }>> = {
  good: { bg: "#dcfce7", fg: "#166534" },
  warn: { bg: "#fef3c7", fg: "#92400e" },
  bad: { bg: "#fee2e2", fg: "#991b1b" },
  info: { bg: "#e0f2fe", fg: "#075985" },
  muted: { bg: "#f1f5f9", fg: "#475569" },
};

export function Pill({ tone, children, testId }: { tone: Tone; children: ReactNode; testId?: string }): JSX.Element {
  const c = TONES[tone];
  return (
    <span
      data-testid={testId}
      style={{
        display: "inline-block",
        padding: "1px 8px",
        borderRadius: 10,
        background: c.bg,
        color: c.fg,
        fontSize: 11,
        fontWeight: 600,
        whiteSpace: "nowrap",
      }}
    >
      {children}
    </span>
  );
}

export function statusTone(status: string): Tone {
  switch (status) {
    case "approved":
      return "good";
    case "rejected":
      return "bad";
    case "pending_approval":
      return "info";
    case "info_requested":
      return "warn";
    default:
      return "muted";
  }
}

export function StatusPill({ status }: { status: string }): JSX.Element {
  return (
    <Pill tone={statusTone(status)} testId="onboarding-status">
      {statusLabel(status)}
    </Pill>
  );
}

export function checkTone(result: string): Tone {
  switch (result) {
    case "pass":
      return "good";
    case "warn":
      return "warn";
    case "fail":
      return "bad";
    default:
      return "muted";
  }
}

// ---------------------------------------------------------------------------
// Modal dialog
// ---------------------------------------------------------------------------

export interface DialogProps {
  readonly title: string;
  readonly testId: string;
  readonly onClose: () => void;
  readonly children: ReactNode;
  /** Present for a form dialog: pressing Enter in a field submits it. */
  readonly onSubmit?: () => void;
  readonly footer: ReactNode;
}

/**
 * Modal dialog with the SAUI-13 contract: role="dialog", aria-modal, Escape
 * closes, focus lands inside and returns to the opener, Tab stays inside.
 */
export function Dialog({ title, testId, onClose, children, onSubmit, footer }: DialogProps): JSX.Element {
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const firstRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, firstRef);
  useFocusRestore(true);
  useFocusTrap<HTMLDivElement>(true, dialogRef);

  const submit = (e: FormEvent<HTMLFormElement>): void => {
    e.preventDefault();
    onSubmit?.();
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={title}
      data-testid={testId}
      ref={dialogRef}
      tabIndex={-1}
      style={backdropStyle}
    >
      <form onSubmit={submit} style={modalStyle}>
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: 8 }}>
          <h2 style={{ margin: 0, fontSize: 17, fontWeight: 600 }}>{title}</h2>
          <button
            ref={firstRef}
            type="button"
            onClick={onClose}
            aria-label="Close"
            style={{ ...buttonStyle, border: "none", fontSize: 18, lineHeight: 1, padding: "2px 8px" }}
            data-testid={`${testId}-close`}
          >
            ×
          </button>
        </div>
        <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>{children}</div>
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8, flexWrap: "wrap" }}>{footer}</div>
      </form>
    </div>
  );
}

const backdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.55)",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  zIndex: 900,
  padding: 16,
};

const modalStyle: CSSProperties = {
  background: "#ffffff",
  padding: 20,
  borderRadius: 6,
  width: "min(560px, 100%)",
  maxHeight: "calc(100dvh - 32px)",
  overflowY: "auto",
  boxShadow: "0 10px 40px rgba(15, 23, 42, 0.25)",
  display: "flex",
  flexDirection: "column",
  gap: 14,
  border: "1px solid #cbd5e1",
};
