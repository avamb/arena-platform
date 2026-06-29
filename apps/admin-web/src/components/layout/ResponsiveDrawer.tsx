import type { CSSProperties, ReactNode } from "react";
import { useEffect, useRef } from "react";
import { useIsDesktop } from "./useMediaQuery";

export interface ResponsiveDrawerProps {
  /** Whether the drawer is currently open. */
  readonly open: boolean;
  /** Called when the operator dismisses the drawer (Esc / scrim / back). */
  readonly onClose: () => void;
  /** Drawer title shown in the header. */
  readonly title: ReactNode;
  /** Optional dense subtitle / breadcrumb. */
  readonly subtitle?: ReactNode;
  /** Drawer body. */
  readonly children: ReactNode;
  /** Optional sticky footer (action bar). */
  readonly footer?: ReactNode;
  /** Stable identifier used by tests. */
  readonly id?: string;
  /** Desktop drawer width in px (default 480). Ignored < md. */
  readonly desktopWidthPx?: number;
  /** Accessible label for the close/back button. Defaults to "Close". */
  readonly closeLabel?: string;
  /**
   * Force a specific layout. Defaults to a matchMedia-driven choice
   * (`desktop` >= md, `mobile` < md). Tests pass an explicit value to
   * avoid relying on the JSDOM matchMedia stub.
   */
  readonly forceLayout?: "desktop" | "mobile";
}

/**
 * Responsive drawer primitive (Wave M-1).
 *
 * Behaviour:
 *
 *  - At >= md (768 px) the drawer slides in from the right as a 480 px
 *    panel, with a translucent scrim covering the remainder of the
 *    viewport. Esc / scrim click closes.
 *
 *  - Below md the drawer expands to a full-screen sheet that occupies
 *    100vw x 100vh. The header has a back-arrow button on the left
 *    instead of an `x` close button on the right, so the affordance
 *    matches the platform convention of mobile detail screens.
 *
 * The component is purely client-side; it returns `null` when closed so
 * test rendering stays cheap. When open, focus is moved to the close /
 * back button so keyboard users land somewhere sensible.
 */
export function ResponsiveDrawer(props: ResponsiveDrawerProps): JSX.Element | null {
  const {
    open,
    onClose,
    title,
    subtitle,
    children,
    footer,
    id,
    desktopWidthPx = 480,
    closeLabel = "Close",
    forceLayout,
  } = props;
  const isDesktopAuto = useIsDesktop(true);
  const isDesktop = forceLayout ? forceLayout === "desktop" : isDesktopAuto;
  const closeBtnRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (!open) return undefined;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === "Escape") {
        onClose();
      }
    };
    if (typeof window !== "undefined") {
      window.addEventListener("keydown", onKey);
    }
    closeBtnRef.current?.focus();
    return () => {
      if (typeof window !== "undefined") {
        window.removeEventListener("keydown", onKey);
      }
    };
  }, [open, onClose]);

  if (!open) {
    return null;
  }

  if (isDesktop) {
    return (
      <div
        data-testid={id ? `${id}-root` : undefined}
        data-layout="desktop"
        style={desktopOverlayStyle}
        role="presentation"
      >
        <div
          data-testid={id ? `${id}-scrim` : undefined}
          style={scrimStyle}
          onClick={onClose}
          aria-hidden="true"
        />
        <aside
          role="dialog"
          aria-modal="true"
          aria-label={typeof title === "string" ? title : undefined}
          data-testid={id ? `${id}-panel` : undefined}
          style={{ ...desktopPanelStyle, width: desktopWidthPx }}
        >
          <header style={desktopHeaderStyle}>
            <div style={titleBlockStyle}>
              <h2 style={titleStyle}>{title}</h2>
              {subtitle !== undefined ? (
                <div style={subtitleStyle}>{subtitle}</div>
              ) : null}
            </div>
            <button
              ref={closeBtnRef}
              type="button"
              onClick={onClose}
              style={closeBtnStyle}
              aria-label={closeLabel}
              data-testid={id ? `${id}-close` : undefined}
            >
              ×
            </button>
          </header>
          <div style={bodyStyle}>{children}</div>
          {footer !== undefined ? (
            <footer style={footerStyle}>{footer}</footer>
          ) : null}
        </aside>
      </div>
    );
  }

  // Mobile: full-screen sheet with a left-aligned back button.
  return (
    <section
      role="dialog"
      aria-modal="true"
      aria-label={typeof title === "string" ? title : undefined}
      data-testid={id ? `${id}-root` : undefined}
      data-layout="mobile"
      style={mobileSheetStyle}
    >
      <header style={mobileHeaderStyle}>
        <button
          ref={closeBtnRef}
          type="button"
          onClick={onClose}
          style={backBtnStyle}
          aria-label={closeLabel}
          data-testid={id ? `${id}-back` : undefined}
        >
          ← <span style={backBtnLabelStyle}>{closeLabel}</span>
        </button>
        <div style={mobileTitleBlockStyle}>
          <h2 style={mobileTitleStyle}>{title}</h2>
          {subtitle !== undefined ? (
            <div style={subtitleStyle}>{subtitle}</div>
          ) : null}
        </div>
      </header>
      <div style={bodyStyle}>{children}</div>
      {footer !== undefined ? (
        <footer style={footerStyle}>{footer}</footer>
      ) : null}
    </section>
  );
}

const desktopOverlayStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  zIndex: 40,
  display: "flex",
  justifyContent: "flex-end",
};

const scrimStyle: CSSProperties = {
  position: "absolute",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
};

const desktopPanelStyle: CSSProperties = {
  position: "relative",
  height: "100vh",
  background: "#ffffff",
  boxShadow: "-4px 0 24px rgba(15, 23, 42, 0.18)",
  display: "flex",
  flexDirection: "column",
  zIndex: 1,
};

const desktopHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  padding: "16px 20px",
  borderBottom: "1px solid #e2e8f0",
  gap: 12,
};

const titleBlockStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  minWidth: 0,
};

const titleStyle: CSSProperties = {
  margin: 0,
  fontSize: 16,
  fontWeight: 600,
  color: "#0f172a",
};

const subtitleStyle: CSSProperties = {
  fontSize: 12,
  color: "#64748b",
};

const closeBtnStyle: CSSProperties = {
  background: "transparent",
  border: 0,
  fontSize: 22,
  lineHeight: 1,
  color: "#475569",
  cursor: "pointer",
  padding: 4,
};

const bodyStyle: CSSProperties = {
  flex: 1,
  overflowY: "auto",
  padding: "16px 20px",
};

const footerStyle: CSSProperties = {
  padding: "12px 20px",
  borderTop: "1px solid #e2e8f0",
  background: "#f8fafc",
  display: "flex",
  justifyContent: "flex-end",
  gap: 8,
};

const mobileSheetStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  zIndex: 50,
  background: "#ffffff",
  display: "flex",
  flexDirection: "column",
};

const mobileHeaderStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
};

const mobileTitleBlockStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 2,
};

const mobileTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 18,
  fontWeight: 600,
  color: "#0f172a",
};

const backBtnStyle: CSSProperties = {
  background: "transparent",
  border: 0,
  padding: "4px 0",
  color: "#1d4ed8",
  fontSize: 14,
  fontWeight: 500,
  cursor: "pointer",
  alignSelf: "flex-start",
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
};

const backBtnLabelStyle: CSSProperties = {
  textDecoration: "none",
};
