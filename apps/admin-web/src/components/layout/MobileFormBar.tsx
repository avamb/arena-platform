import type { CSSProperties } from "react";
import { useEffect, useRef, useState } from "react";

/**
 * Wave M-5 — sticky bottom action bar contract for organizer/agent forms.
 *
 * The M-5 specification requires every organizer/agent form (O-4 legal &
 * billing, V-3 venue address, E-3..E-5 event/session/tier) to render its
 * Save / Cancel action row as a sticky bar at the bottom of the form that:
 *
 *  - is at least 56 px tall (comfortable one-thumb touch target),
 *  - respects `env(safe-area-inset-bottom)` so the iOS home-indicator does
 *    not eat the action row,
 *  - keeps the primary action visually distinct from the secondary,
 *  - stays inside the single-column < md layout (no horizontal scroll).
 *
 * This module exports a small set of inert CSS-in-JS style objects plus an
 * `OverflowMenu` primitive for grouping destructive actions away from the
 * primary Save / Cancel buttons. The styles are intentionally plain
 * `CSSProperties` so they slot into the existing inline-style call sites
 * without requiring a CSS framework migration.
 */

// ---------------------------------------------------------------------------
// Sticky action bar styles
// ---------------------------------------------------------------------------

/**
 * Minimum height for the M-5 action bar, in pixels. Exported as a constant
 * so tests can assert the contract numerically without parsing CSS strings.
 */
export const M5_ACTION_BAR_MIN_HEIGHT_PX = 56 as const;

/**
 * Sticky bottom action bar style. Drop-in replacement for the legacy
 * `formActionsStyle` / `rowActionsStyle` containers used by O-4, V-3,
 * E-3..E-5 form footers.
 *
 * Layout:
 *  - position: sticky / bottom: 0 so the bar follows the operator as they
 *    scroll a long form on a small viewport,
 *  - flex row with `gap: 8` and `justify-content: flex-end` so the primary
 *    Save sits on the right with Cancel immediately to its left,
 *  - `padding-bottom` adds `env(safe-area-inset-bottom)` so iOS / Android
 *    home-indicators do not overlap the touch target,
 *  - `min-height: 56px` enforces the touch-target contract,
 *  - white background + 1 px top border so it visually separates from the
 *    scrollable form body.
 */
export const mobileFormBarStyle: CSSProperties = {
  position: "sticky",
  bottom: 0,
  left: 0,
  right: 0,
  zIndex: 5,
  display: "flex",
  flexDirection: "row",
  alignItems: "center",
  justifyContent: "flex-end",
  gap: 8,
  minHeight: M5_ACTION_BAR_MIN_HEIGHT_PX,
  padding: "8px 16px",
  paddingBottom: "calc(8px + env(safe-area-inset-bottom))",
  background: "#ffffff",
  borderTop: "1px solid #e2e8f0",
  boxShadow: "0 -2px 8px rgba(15, 23, 42, 0.04)",
  flexWrap: "wrap",
};

/**
 * Single-column form layout. Forms authored against the M-5 contract should
 * wrap their field stack with this style so that under the `md` breakpoint
 * the layout collapses to one column with comfortable vertical spacing.
 *
 * On wider viewports the grid `repeat(auto-fit, minmax(220px, 1fr))` lets
 * paired fields (e.g. "tax-id scheme" + "tax-id") sit side-by-side, but
 * narrow viewports always stack.
 */
export const singleColumnFormStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

// ---------------------------------------------------------------------------
// Overflow menu (destructive action grouping)
// ---------------------------------------------------------------------------

export interface OverflowMenuItem {
  /** Item label shown in the dropdown. */
  readonly label: string;
  /** Click handler. */
  readonly onSelect: () => void;
  /** Visual emphasis: "danger" for destructive actions. */
  readonly tone?: "default" | "danger";
  /** Disabled flag. */
  readonly disabled?: boolean;
  /** Stable identifier used by tests. */
  readonly testid?: string;
}

export interface OverflowMenuProps {
  /** Items shown in the dropdown. */
  readonly items: ReadonlyArray<OverflowMenuItem>;
  /** Optional label for the trigger button. Defaults to "More". */
  readonly triggerLabel?: string;
  /** Stable identifier used by tests. */
  readonly id?: string;
}

/**
 * Minimal overflow / kebab menu used to host destructive actions away from
 * the primary Save / Cancel buttons in the M-5 action bar. Pure-react,
 * keyboard-friendly, JSDOM-safe.
 */
export function OverflowMenu({
  items,
  triggerLabel = "More",
  id,
}: OverflowMenuProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!open) return undefined;
    const handler = (e: MouseEvent): void => {
      const target = e.target as Node | null;
      if (containerRef.current && target && !containerRef.current.contains(target)) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") setOpen(false);
    };
    if (typeof window !== "undefined") {
      window.addEventListener("mousedown", handler);
      window.addEventListener("keydown", onKey);
    }
    return () => {
      if (typeof window !== "undefined") {
        window.removeEventListener("mousedown", handler);
        window.removeEventListener("keydown", onKey);
      }
    };
  }, [open]);

  return (
    <div
      ref={containerRef}
      style={overflowMenuContainerStyle}
      data-testid={id ? `${id}-root` : undefined}
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        style={overflowTriggerStyle}
        aria-haspopup="menu"
        aria-expanded={open}
        data-testid={id ? `${id}-trigger` : undefined}
      >
        {triggerLabel} ⋯
      </button>
      {open ? (
        <div
          role="menu"
          style={overflowMenuListStyle}
          data-testid={id ? `${id}-list` : undefined}
        >
          {items.map((item, idx) => (
            <button
              key={`${item.label}-${idx}`}
              type="button"
              role="menuitem"
              disabled={item.disabled}
              onClick={() => {
                setOpen(false);
                item.onSelect();
              }}
              style={
                item.tone === "danger"
                  ? overflowItemDangerStyle
                  : overflowItemStyle
              }
              data-testid={item.testid}
            >
              {item.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

const overflowMenuContainerStyle: CSSProperties = {
  position: "relative",
  display: "inline-block",
  marginRight: "auto", // push trigger to the left of Save / Cancel
};

const overflowTriggerStyle: CSSProperties = {
  minHeight: 44,
  minWidth: 44,
  padding: "8px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 6,
  color: "#475569",
  fontWeight: 600,
  fontSize: 13,
  cursor: "pointer",
};

const overflowMenuListStyle: CSSProperties = {
  position: "absolute",
  bottom: "calc(100% + 4px)",
  left: 0,
  minWidth: 200,
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 6,
  boxShadow: "0 4px 12px rgba(15, 23, 42, 0.12)",
  padding: 4,
  display: "flex",
  flexDirection: "column",
  gap: 2,
  zIndex: 10,
};

const overflowItemBaseStyle: CSSProperties = {
  appearance: "none",
  background: "transparent",
  border: 0,
  padding: "10px 12px",
  borderRadius: 4,
  fontSize: 13,
  textAlign: "left",
  cursor: "pointer",
  minHeight: 44,
};

const overflowItemStyle: CSSProperties = {
  ...overflowItemBaseStyle,
  color: "#0f172a",
};

const overflowItemDangerStyle: CSSProperties = {
  ...overflowItemBaseStyle,
  color: "#b91c1c",
  fontWeight: 600,
};
