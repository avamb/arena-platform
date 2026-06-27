/**
 * Shared CSS-in-JS style tokens for SuperAdmin support consoles
 * (SAUI-10). Lifted from the SAUI-06 organizations explorer so the
 * three new modules render with the same visual vocabulary -- toolbar
 * card, table, drawer, error/empty boxes, related-data tiles -- without
 * each route file repeating ~200 lines of style object literals.
 *
 * Keep these values aligned with organizations.tsx. If a future visual
 * refresh ships, edit here and audit the four consumers
 * (organizations / orders / tickets / refunds).
 */
import type { CSSProperties } from "react";

export const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

export const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
};

export const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: -0.2,
};

export const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

export const refreshWrapStyle: CSSProperties = { display: "flex", gap: 8 };
export const refreshButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

export const toolbarStyle: CSSProperties = {
  display: "flex",
  gap: 12,
  alignItems: "flex-end",
  flexWrap: "wrap",
  padding: "8px 12px",
  background: "#f8fafc",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
};

export const fieldGroupStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  fontSize: 12,
  color: "#475569",
};

export const fieldLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

export const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
  minWidth: 200,
};

export const inputInvalidStyle: CSSProperties = {
  ...inputStyle,
  border: "1px solid #f87171",
  background: "#fef2f2",
};

export const selectStyle: CSSProperties = {
  fontSize: 13,
  padding: "6px 8px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

export const buttonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

export const buttonPrimaryStyle: CSSProperties = {
  ...buttonStyle,
  background: "#0f172a",
  border: "1px solid #0f172a",
  color: "#ffffff",
};

export const pageNavStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 8,
  fontSize: 12,
  color: "#475569",
  marginLeft: "auto",
};

export const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

export const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
};

export const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "10px 12px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

export const trStyle: CSSProperties = {};
export const trActiveStyle: CSSProperties = { background: "#eff6ff" };

export const tdStyle: CSSProperties = {
  padding: "10px 12px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
  verticalAlign: "top",
};

export const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  color: "#334155",
};

export const rowNameButtonStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  padding: 0,
  color: "#0369a1",
  fontSize: 12,
  fontWeight: 500,
  cursor: "pointer",
  textAlign: "left",
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
};

export const rowActionButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

export const statusBoxStyle: CSSProperties = {
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};

export const errorBoxStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 16,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  fontSize: 12,
};

export const errorParaStyle: CSSProperties = { margin: 0, fontSize: 12 };
export const errorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};
export const errorRetryStyle: CSSProperties = {
  alignSelf: "flex-start",
  fontSize: 12,
  padding: "6px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

export const drawerWrapStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
  padding: 16,
  border: "1px solid #e2e8f0",
  borderRadius: 8,
  background: "#ffffff",
};

export const drawerHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 12,
};

export const drawerEyebrowStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  textTransform: "uppercase",
  letterSpacing: 0.5,
};

export const drawerTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 18,
  fontWeight: 600,
  color: "#0f172a",
};

export const drawerCloseStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  fontSize: 24,
  lineHeight: 1,
  cursor: "pointer",
  color: "#64748b",
  padding: "0 4px",
};

export const drawerSectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

export const drawerSectionTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
  textTransform: "uppercase",
  letterSpacing: 0.5,
};

export const drawerHelpStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.45,
};

export const metaListStyle: CSSProperties = {
  margin: 0,
  display: "grid",
  gridTemplateColumns: "minmax(140px, max-content) 1fr",
  rowGap: 6,
  columnGap: 12,
  fontSize: 12,
};
export const metaRowStyle: CSSProperties = { display: "contents" };
export const metaKeyStyle: CSSProperties = { margin: 0, color: "#64748b" };
export const metaValStyle: CSSProperties = {
  margin: 0,
  color: "#0f172a",
  wordBreak: "break-word",
};
export const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};
export const mutedStyle: CSSProperties = { color: "#94a3b8" };

export const relatedGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
  gap: 8,
};
export const relatedTileStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "10px 12px",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  textDecoration: "none",
  color: "#0f172a",
};
export const relatedTileDisabledStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "10px 12px",
  borderRadius: 6,
  background: "#f8fafc",
  border: "1px dashed #cbd5e1",
  color: "#475569",
};
export const relatedTileLabelStyle: CSSProperties = {
  fontSize: 13,
  fontWeight: 600,
};
export const relatedTileHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
};
export const relatedTileGapBadgeStyle: CSSProperties = {
  alignSelf: "flex-start",
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  background: "#fef3c7",
  color: "#78350f",
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

export const statusBadgeStyle: CSSProperties = {
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  background: "#e0e7ff",
  color: "#3730a3",
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

export const errorBadgeStyle: CSSProperties = {
  ...statusBadgeStyle,
  background: "#fee2e2",
  color: "#7f1d1d",
};

export const successBadgeStyle: CSSProperties = {
  ...statusBadgeStyle,
  background: "#dcfce7",
  color: "#166534",
};

export const warnBadgeStyle: CSSProperties = {
  ...statusBadgeStyle,
  background: "#fef3c7",
  color: "#78350f",
};

export const gapNoteStyle: CSSProperties = {
  padding: 12,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.45,
};
