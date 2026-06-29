import type { CSSProperties, ChangeEvent } from "react";
import { useState } from "react";
import { useScope } from "@/lib/auth/ScopeContext";
import { useIsDesktop } from "@/components/layout/useMediaQuery";
import { MobileScopeSheet } from "@/components/MobileScopeSheet";

/**
 * Scope picker rendered in the shell header.
 *
 * Responsive behaviour (Wave M-2):
 *
 *   - At >= md (768 px): the desktop affordance -- an inline <select>
 *     dropdown -- is rendered exactly as before. With a single scope (or
 *     none) it collapses to a static label rather than a dropdown.
 *
 *   - Below md: collapses to a single tappable chip showing the active
 *     scope label. Tapping the chip opens a full-screen scope picker
 *     sheet (MobileScopeSheet) with a back button and a search box, so
 *     operators on a 360 x 640 viewport have a comfortable target list.
 *
 * `forceLayout` lets tests render either branch deterministically without
 * needing to stub matchMedia.
 */
export interface ScopeSelectorProps {
  readonly forceLayout?: "desktop" | "mobile";
}

export function ScopeSelector({ forceLayout }: ScopeSelectorProps = {}) {
  const { availableScopes, activeScope, setActiveScope } = useScope();
  const isDesktopAuto = useIsDesktop(true);
  const isDesktop = forceLayout
    ? forceLayout === "desktop"
    : isDesktopAuto;

  if (isDesktop) {
    return (
      <DesktopScopeSelector
        availableScopes={availableScopes}
        activeRaw={activeScope?.raw ?? null}
        activeLabel={activeScope?.label ?? null}
        setActiveScope={setActiveScope}
      />
    );
  }

  return (
    <MobileScopeChip
      availableScopes={availableScopes}
      activeRaw={activeScope?.raw ?? null}
      activeLabel={activeScope?.label ?? null}
      setActiveScope={setActiveScope}
    />
  );
}

interface InnerProps {
  readonly availableScopes: ReturnType<typeof useScope>["availableScopes"];
  readonly activeRaw: string | null;
  readonly activeLabel: string | null;
  readonly setActiveScope: (raw: string) => void;
}

function DesktopScopeSelector({
  availableScopes,
  activeRaw,
  setActiveScope,
}: InnerProps) {
  if (availableScopes.length === 0) {
    return (
      <div style={containerStyle} data-testid="scope-selector-empty">
        <span style={labelStyle}>Scope</span>
        <span style={valueStyle}>no scopes assigned</span>
      </div>
    );
  }

  if (availableScopes.length === 1) {
    const only = availableScopes[0];
    return (
      <div style={containerStyle} data-testid="scope-selector-single">
        <span style={labelStyle}>Scope</span>
        <span style={valueStyle} title={only?.raw}>
          {only?.label}
        </span>
      </div>
    );
  }

  const onChange = (e: ChangeEvent<HTMLSelectElement>): void => {
    setActiveScope(e.target.value);
  };

  return (
    <label style={containerStyle} data-testid="scope-selector">
      <span style={labelStyle}>Scope</span>
      <select
        value={activeRaw ?? ""}
        onChange={onChange}
        style={selectStyle}
        aria-label="Active authorization scope"
      >
        {availableScopes.map((s) => (
          <option key={s.raw} value={s.raw} title={s.raw}>
            {s.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function MobileScopeChip({
  availableScopes,
  activeRaw,
  activeLabel,
  setActiveScope,
}: InnerProps) {
  const [open, setOpen] = useState(false);

  if (availableScopes.length === 0) {
    return (
      <div style={chipStyle} data-testid="scope-selector-empty">
        <span style={labelStyle}>Scope</span>
        <span style={valueStyle}>no scopes assigned</span>
      </div>
    );
  }

  const chipText =
    activeLabel ?? (availableScopes[0]?.label ?? "Select scope");

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        style={chipButtonStyle}
        data-testid="scope-selector-chip"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`Active scope: ${chipText}. Tap to change.`}
      >
        <span style={labelStyle}>Scope</span>
        <span style={chipValueStyle}>{chipText}</span>
        <span aria-hidden="true" style={chipChevronStyle}>
          ▾
        </span>
      </button>
      <MobileScopeSheet
        open={open}
        onClose={() => setOpen(false)}
        scopes={availableScopes}
        activeRaw={activeRaw}
        onSelect={setActiveScope}
        title="Scope"
        searchPlaceholder="Search scopes"
        backLabel="Back"
      />
    </>
  );
}

const containerStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 8,
  padding: "4px 8px",
  background: "#f1f5f9",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  fontSize: 12,
};
const labelStyle: CSSProperties = {
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
  fontSize: 10,
};
const valueStyle: CSSProperties = { color: "#0f172a" };
const selectStyle: CSSProperties = {
  border: "1px solid #cbd5e1",
  background: "#ffffff",
  padding: "4px 6px",
  fontSize: 12,
  borderRadius: 3,
  color: "#0f172a",
};

const chipStyle: CSSProperties = {
  display: "inline-flex",
  alignItems: "center",
  gap: 8,
  padding: "8px 12px",
  background: "#f1f5f9",
  border: "1px solid #cbd5e1",
  borderRadius: 999,
  fontSize: 13,
  maxWidth: "100%",
};

const chipButtonStyle: CSSProperties = {
  ...chipStyle,
  cursor: "pointer",
  color: "#0f172a",
  // 44 px is the platform-conventional minimum tap target on iOS/Android.
  minHeight: 44,
};

const chipValueStyle: CSSProperties = {
  color: "#0f172a",
  fontWeight: 500,
  overflow: "hidden",
  textOverflow: "ellipsis",
  whiteSpace: "nowrap",
  maxWidth: 180,
};

const chipChevronStyle: CSSProperties = {
  color: "#64748b",
  fontSize: 11,
};
