/**
 * Shared accessible drawer wrapper for SuperAdmin support consoles
 * (SAUI-13).
 *
 * The orders / tickets / refunds / organizations drawers are
 * structurally identical: an inline <aside role="dialog"> rendered
 * beneath the table once a row is opened. Before SAUI-13 they each
 * wired their own header + close button and shipped without keyboard
 * dismissal or focus restoration; this wrapper centralises:
 *
 *   - the role="dialog" / aria-modal / aria-labelledby contract;
 *   - the Escape-key close binding (useEscapeClose);
 *   - landing focus on the close button when the drawer opens
 *     (useFocusOnMount);
 *   - returning focus to the originating row button after close
 *     (useFocusRestore);
 *   - the visually consistent header chrome (eyebrow, title, ×).
 *
 * The drawer is intentionally NON-modal (aria-modal="false") and does
 * NOT trap focus -- the support consoles render it inline below the
 * table rather than as an overlay, and trapping focus inside an inline
 * region would conflict with the "Tab moves down the page" expectation.
 * Modal dialogs (e.g. <ReasonPromptModal />) call useFocusTrap directly.
 */
import { useRef, type ReactNode } from "react";
import * as S from "@/lib/admin/supportStyles";
import {
  useEscapeClose,
  useFocusOnMount,
  useFocusRestore,
} from "@/lib/a11y";

export interface SupportDrawerProps {
  /**
   * Stable id used to derive the dialog title element id
   * (`${id}-title`) so callers can also use it for tests / external
   * aria references.
   */
  readonly id: string;
  /** Short kind label rendered above the title (e.g. "Order"). */
  readonly eyebrow: string;
  /** Title content -- typically a monospaced UUID code block. */
  readonly title: ReactNode;
  /** Accessible name for the close button. */
  readonly closeLabel: string;
  /** data-testid prefix; the close button gets `${testIdPrefix}-close`. */
  readonly testIdPrefix: string;
  /** Called when the operator clicks × or presses Escape. */
  readonly onClose: () => void;
  readonly children: ReactNode;
}

export function SupportDrawer({
  id,
  eyebrow,
  title,
  closeLabel,
  testIdPrefix,
  onClose,
  children,
}: SupportDrawerProps) {
  const closeRef = useRef<HTMLButtonElement | null>(null);

  // SAUI-13 accessibility wiring. The drawer is non-modal but we still
  // owe operators keyboard parity: Esc closes, focus lands on close,
  // and focus returns to wherever it came from once we unmount.
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);

  const titleId = `${id}-title`;

  return (
    <aside
      style={S.drawerWrapStyle}
      role="dialog"
      aria-modal="false"
      aria-labelledby={titleId}
      data-testid={testIdPrefix}
    >
      <header style={S.drawerHeaderStyle}>
        <div>
          <div style={S.drawerEyebrowStyle}>{eyebrow}</div>
          <h2 id={titleId} style={S.drawerTitleStyle}>
            {title}
          </h2>
        </div>
        <button
          type="button"
          ref={closeRef}
          onClick={onClose}
          style={S.drawerCloseStyle}
          aria-label={closeLabel}
          data-testid={`${testIdPrefix}-close`}
          title="Close (Esc)"
        >
          ×
        </button>
      </header>
      {children}
    </aside>
  );
}
