import type { CSSProperties } from "react";

interface LoadingScreenProps {
  readonly label?: string;
}

/**
 * Indeterminate loading screen used while initial app data resolves.
 *
 * Deliberately minimal -- no marketing copy, no illustrations. The admin
 * shell is an operational tool: communicate state, then get out of the way.
 */
export function LoadingScreen({ label = "Loading admin workspace…" }: LoadingScreenProps) {
  return (
    <div role="status" aria-live="polite" style={containerStyle}>
      <span style={spinnerStyle} aria-hidden="true" />
      <span>{label}</span>
    </div>
  );
}

const containerStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  gap: 12,
  height: "100vh",
  fontFamily: "system-ui, -apple-system, Segoe UI, sans-serif",
  fontSize: 14,
  color: "#475569",
};

const spinnerStyle: CSSProperties = {
  width: 14,
  height: 14,
  border: "2px solid #cbd5e1",
  borderTopColor: "#0f172a",
  borderRadius: "50%",
  display: "inline-block",
  animation: "admin-spinner-rotate 0.9s linear infinite",
};
