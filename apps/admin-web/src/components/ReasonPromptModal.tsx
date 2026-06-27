/**
 * <ReasonPromptModal /> -- audit-reason capture UI (SAUI-04).
 *
 * Rendered once at the shell layer. When `useReason().pendingPrompt` is
 * non-null, the modal is shown over the current screen. The operator
 * must enter a non-empty reason to submit. Cancelling rejects the
 * pending API resolver(s) so the in-flight cross-tenant request fails
 * fast instead of going out without a header.
 *
 * Accessibility:
 *   - role="dialog" + aria-modal="true"
 *   - aria-labelledby points to the heading
 *   - first input is auto-focused on open
 *   - Esc cancels; Enter (inside form) submits
 */
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import { useReason } from "@/lib/auth/ReasonContext";
import {
  useEscapeClose,
  useFocusOnMount,
  useFocusRestore,
  useFocusTrap,
} from "@/lib/a11y";

export function ReasonPromptModal() {
  const { pendingPrompt, submitPrompt, cancelPrompt } = useReason();
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement | null>(null);
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const active = pendingPrompt !== null;

  // Reset the draft each time a new prompt opens. The actual focus
  // landing on the input is handled by useFocusOnMount below so we no
  // longer maintain a separate requestAnimationFrame block here.
  useEffect(() => {
    if (active) {
      setDraft("");
    }
  }, [active]);

  // SAUI-13 accessibility contract for the modal:
  //   - global Escape closes the prompt (useEscapeClose);
  //   - first interactive element (the input) receives focus on open;
  //   - focus restores to the element that opened the prompt when it
  //     closes -- this is critical because the prompt is triggered by
  //     authedFetch, often from a button deep in a support console;
  //   - Tab / Shift+Tab cycle stays inside the modal (useFocusTrap).
  useEscapeClose(active, cancelPrompt);
  useFocusOnMount<HTMLInputElement>(active, inputRef);
  useFocusRestore(active);
  useFocusTrap<HTMLDivElement>(active, dialogRef);

  const onSubmit = useCallback(
    (e: FormEvent<HTMLFormElement>) => {
      e.preventDefault();
      const trimmed = draft.trim();
      if (trimmed === "") {
        return;
      }
      submitPrompt(trimmed);
    },
    [draft, submitPrompt],
  );

  const onKeyDown = useCallback(
    (e: ReactKeyboardEvent<HTMLInputElement>) => {
      // Block the global Esc handler from racing the form when input is
      // focused -- consistent UX (esc cancels everywhere).
      if (e.key === "Escape") {
        e.preventDefault();
        cancelPrompt();
      }
    },
    [cancelPrompt],
  );

  if (pendingPrompt === null) {
    return null;
  }

  const trimmedLen = draft.trim().length;
  const canSubmit = trimmedLen > 0;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="reason-modal-heading"
      aria-describedby="reason-modal-body"
      data-testid="reason-prompt-modal"
      style={backdropStyle}
      ref={dialogRef}
      tabIndex={-1}
    >
      <form onSubmit={onSubmit} style={modalStyle}>
        <h2 id="reason-modal-heading" style={headingStyle}>
          Audit reason required
        </h2>
        <p id="reason-modal-body" style={bodyStyle}>
          You are about to read data across tenants. Enter a short, factual
          business reason; it will be attached to the audit trail for this
          and subsequent cross-tenant requests in this session.
        </p>
        <p style={metaStyle} data-testid="reason-modal-path">
          Triggered by:{" "}
          <code style={codeStyle}>{pendingPrompt.path}</code>
        </p>
        <label style={labelStyle} htmlFor="reason-modal-input">
          Business reason
        </label>
        <input
          id="reason-modal-input"
          ref={inputRef}
          type="text"
          required
          minLength={1}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="e.g. Investigating support ticket #4827"
          style={inputStyle}
          autoComplete="off"
          data-testid="reason-modal-input"
        />
        <p style={hintStyle}>
          Avoid generic phrases like &ldquo;browsing&rdquo; or
          &ldquo;checking&rdquo;. The reason is recorded verbatim.
        </p>
        <div style={actionsStyle}>
          <button
            type="button"
            onClick={cancelPrompt}
            style={cancelBtnStyle}
            data-testid="reason-modal-cancel"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={!canSubmit}
            style={canSubmit ? submitBtnStyle : submitBtnDisabledStyle}
            data-testid="reason-modal-submit"
            aria-describedby={canSubmit ? undefined : "reason-modal-submit-hint"}
            title={
              canSubmit
                ? undefined
                : "Enter a non-empty business reason to enable submission."
            }
          >
            Confirm reason
          </button>
          {canSubmit ? null : (
            <span
              id="reason-modal-submit-hint"
              style={visuallyHiddenHintStyle}
            >
              Enter a non-empty business reason to enable submission.
            </span>
          )}
        </div>
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
  zIndex: 1000,
};

const modalStyle: CSSProperties = {
  background: "#ffffff",
  padding: 24,
  borderRadius: 6,
  maxWidth: 480,
  width: "92%",
  boxShadow: "0 10px 40px rgba(15, 23, 42, 0.25)",
  display: "flex",
  flexDirection: "column",
  gap: 10,
  border: "1px solid #cbd5e1",
};

const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 18,
  fontWeight: 600,
  color: "#0f172a",
};

const bodyStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#334155",
  lineHeight: 1.5,
};

const metaStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#475569",
};

const labelStyle: CSSProperties = {
  marginTop: 4,
  fontSize: 11,
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
  color: "#475569",
};

const inputStyle: CSSProperties = {
  padding: "8px 10px",
  fontSize: 13,
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#f8fafc",
  color: "#0f172a",
  fontFamily: "inherit",
};

const hintStyle: CSSProperties = {
  margin: 0,
  fontSize: 11,
  color: "#64748b",
  fontStyle: "italic",
};

const actionsStyle: CSSProperties = {
  marginTop: 8,
  display: "flex",
  justifyContent: "flex-end",
  gap: 8,
};

const cancelBtnStyle: CSSProperties = {
  background: "#f1f5f9",
  color: "#0f172a",
  border: "1px solid #cbd5e1",
  padding: "8px 14px",
  fontSize: 13,
  borderRadius: 4,
  cursor: "pointer",
};

const submitBtnStyle: CSSProperties = {
  background: "#0f172a",
  color: "#f8fafc",
  border: 0,
  padding: "8px 14px",
  fontSize: 13,
  borderRadius: 4,
  cursor: "pointer",
};

const submitBtnDisabledStyle: CSSProperties = {
  ...submitBtnStyle,
  background: "#94a3b8",
  cursor: "not-allowed",
};

// Visually-hidden helper for aria-describedby live text. Mirrors the
// `visuallyHiddenStyle` helper in @/lib/a11y/tokens; inlined here to
// avoid a runtime dependency on the barrel import.
const visuallyHiddenHintStyle: CSSProperties = {
  position: "absolute",
  width: 1,
  height: 1,
  padding: 0,
  margin: -1,
  overflow: "hidden",
  clip: "rect(0, 0, 0, 0)",
  whiteSpace: "nowrap",
  border: 0,
};

const codeStyle: CSSProperties = {
  background: "#e2e8f0",
  padding: "1px 4px",
  borderRadius: 3,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};
