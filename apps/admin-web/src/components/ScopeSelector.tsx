import type { CSSProperties, ChangeEvent } from "react";
import { useScope } from "@/lib/auth/ScopeContext";

/**
 * Scope picker rendered in the shell header.
 *
 * When the caller has only one scope (or none), the selector renders
 * as a static label rather than a dropdown -- nothing to switch to.
 * Direct UUIDs in scope strings are truncated for legibility (the full
 * value is exposed via the title attribute for debugging).
 */
export function ScopeSelector() {
  const { availableScopes, activeScope, setActiveScope } = useScope();

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
        value={activeScope?.raw ?? ""}
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
