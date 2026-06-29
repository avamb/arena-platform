/**
 * Mobile full-screen scope picker (Wave M-2).
 *
 * Below the `md` breakpoint, the desktop <select>-based ScopeSelector is
 * replaced by a chip in the sticky header. Tapping the chip mounts this
 * full-screen sheet so the operator can pick a scope on a small viewport
 * (360 x 640+) with a comfortable hit target list, a back button, and a
 * type-to-filter search box.
 *
 * The sheet itself is intentionally self-contained (no ResponsiveDrawer
 * dependency) because:
 *   - it needs a search input rendered inside the sticky header, which
 *     ResponsiveDrawer does not support; and
 *   - the active scope must be visually selected (radiogroup semantics),
 *     which is also outside ResponsiveDrawer's contract.
 *
 * The component is purely presentational: the parent (ScopeSelector)
 * passes the scopes, active raw, and an onSelect callback. The list
 * renders only when `open` is true so closed state contributes no DOM.
 */
import {
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type ChangeEvent,
} from "react";
import type { Scope } from "@/lib/auth/scope";

/**
 * Pure filter helper -- substring match against `label`, `raw`, and
 * (when present) `id`. Case-insensitive, whitespace-trimmed. Returns the
 * original array when the query is empty so reference equality is
 * preserved by the caller's memoisation.
 */
export function filterScopes(
  scopes: readonly Scope[],
  query: string,
): readonly Scope[] {
  const q = query.trim().toLowerCase();
  if (q.length === 0) {
    return scopes;
  }
  return scopes.filter((s) => {
    if (s.label.toLowerCase().includes(q)) return true;
    if (s.raw.toLowerCase().includes(q)) return true;
    if (s.id !== null && s.id.toLowerCase().includes(q)) return true;
    return false;
  });
}

export interface MobileScopeSheetProps {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly scopes: readonly Scope[];
  readonly activeRaw: string | null;
  readonly onSelect: (raw: string) => void;
  /** Sheet title. Defaults to "Scope". */
  readonly title?: string;
  /** Placeholder for the search box. */
  readonly searchPlaceholder?: string;
  /** Accessible back-button label. Defaults to "Back". */
  readonly backLabel?: string;
  /** Stable identifier for tests. */
  readonly id?: string;
}

export function MobileScopeSheet(props: MobileScopeSheetProps): JSX.Element | null {
  const {
    open,
    onClose,
    scopes,
    activeRaw,
    onSelect,
    title = "Scope",
    searchPlaceholder = "Search scopes",
    backLabel = "Back",
    id = "scope-picker",
  } = props;

  const [query, setQuery] = useState<string>("");
  const closeBtnRef = useRef<HTMLButtonElement | null>(null);

  // Reset the search field whenever the sheet is reopened.
  useEffect(() => {
    if (open) {
      setQuery("");
    }
  }, [open]);

  // Esc to close + initial focus on the back button.
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

  const visible = filterScopes(scopes, query);

  const onQueryChange = (e: ChangeEvent<HTMLInputElement>): void => {
    setQuery(e.target.value);
  };

  return (
    <section
      role="dialog"
      aria-modal="true"
      aria-label={typeof title === "string" ? title : undefined}
      data-testid={`${id}-root`}
      data-layout="mobile"
      style={sheetStyle}
    >
      <header style={headerStyle}>
        <button
          ref={closeBtnRef}
          type="button"
          onClick={onClose}
          style={backBtnStyle}
          aria-label={backLabel}
          data-testid={`${id}-back`}
        >
          ← <span>{backLabel}</span>
        </button>
        <h2 style={titleStyle}>{title}</h2>
        <input
          type="search"
          value={query}
          onChange={onQueryChange}
          placeholder={searchPlaceholder}
          aria-label={searchPlaceholder}
          style={searchStyle}
          data-testid={`${id}-search`}
        />
      </header>
      <div
        role="radiogroup"
        aria-label={typeof title === "string" ? title : undefined}
        style={listStyle}
        data-testid={`${id}-list`}
      >
        {visible.length === 0 ? (
          <p style={emptyStyle} data-testid={`${id}-empty`}>
            No scopes match "{query}".
          </p>
        ) : (
          visible.map((s) => {
            const checked = s.raw === activeRaw;
            return (
              <button
                key={s.raw}
                type="button"
                role="radio"
                aria-checked={checked}
                onClick={() => {
                  onSelect(s.raw);
                  onClose();
                }}
                style={checked ? optionActiveStyle : optionStyle}
                data-testid={`${id}-option-${s.raw}`}
              >
                <span style={optionLabelStyle}>{s.label}</span>
                <span style={optionRawStyle} title={s.raw}>
                  {s.raw}
                </span>
              </button>
            );
          })
        )}
      </div>
    </section>
  );
}

const sheetStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  zIndex: 60,
  background: "#ffffff",
  display: "flex",
  flexDirection: "column",
  // Respect iOS browser chrome so the last list row is not occluded.
  paddingBottom: "env(safe-area-inset-bottom)",
};

const headerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
  position: "sticky",
  top: 0,
  background: "#ffffff",
};

const titleStyle: CSSProperties = {
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

const searchStyle: CSSProperties = {
  width: "100%",
  padding: "10px 12px",
  fontSize: 14,
  borderRadius: 6,
  border: "1px solid #cbd5e1",
  background: "#f8fafc",
  color: "#0f172a",
  outline: "none",
};

const listStyle: CSSProperties = {
  flex: 1,
  overflowY: "auto",
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "8px 12px 16px",
};

const optionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  alignItems: "flex-start",
  gap: 2,
  padding: "12px 14px",
  borderRadius: 6,
  border: "1px solid #e2e8f0",
  background: "#ffffff",
  color: "#0f172a",
  fontSize: 14,
  cursor: "pointer",
  textAlign: "left",
  // Generous hit target on mobile.
  minHeight: 56,
};

const optionActiveStyle: CSSProperties = {
  ...optionStyle,
  borderColor: "#1d4ed8",
  background: "#eff6ff",
};

const optionLabelStyle: CSSProperties = {
  fontWeight: 600,
};

const optionRawStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
  color: "#64748b",
  wordBreak: "break-all",
};

const emptyStyle: CSSProperties = {
  margin: "12px 4px",
  padding: "12px 14px",
  background: "#f1f5f9",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  fontSize: 13,
  color: "#475569",
};
