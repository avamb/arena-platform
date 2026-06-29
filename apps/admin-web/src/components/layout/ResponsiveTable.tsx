import type { CSSProperties, ReactNode } from "react";
import { Fragment } from "react";
import { useIsDesktop } from "./useMediaQuery";

/**
 * Shared column contract for {@link ResponsiveTable}.
 *
 * The same {@link renderCell} fn drives both the desktop `<td>` and the
 * mobile card body, so columns stay in sync across viewports without
 * authors having to maintain two templates.
 *
 *  - `id`             stable identifier for keyed renders.
 *  - `header`         label shown in the desktop `<th>` AND as the
 *                     dt-style label inside the mobile card.
 *  - `renderCell`     produces the value for a given row.
 *  - `align`          desktop-only text alignment.
 *  - `width`          desktop-only optional explicit column width.
 *  - `hideOnMobile`   when true the column is dropped entirely from the
 *                     mobile card (e.g. raw IDs, secondary timestamps).
 *  - `primary`        when true, on mobile the value is rendered as a
 *                     card title (large, no label) instead of a dt/dd.
 *                     Mark at most one column as primary per table.
 */
export interface ResponsiveTableColumn<TRow> {
  readonly id: string;
  readonly header: ReactNode;
  readonly renderCell: (row: TRow, index: number) => ReactNode;
  readonly align?: "left" | "right" | "center";
  readonly width?: number | string;
  readonly hideOnMobile?: boolean;
  readonly primary?: boolean;
}

export interface ResponsiveTableProps<TRow> {
  /** Stable identifier used by tests / dev tools. */
  readonly id?: string;
  /** Accessible caption announced by screen readers. */
  readonly caption?: ReactNode;
  /** Column definitions. Same list drives both desktop & mobile. */
  readonly columns: readonly ResponsiveTableColumn<TRow>[];
  /** Row data. */
  readonly rows: readonly TRow[];
  /** Key extractor; defaults to the row index when omitted. */
  readonly rowKey?: (row: TRow, index: number) => string;
  /** Optional click handler for a row (used by both desktop & mobile). */
  readonly onRowClick?: (row: TRow, index: number) => void;
  /** Rendered when `rows` is empty. */
  readonly empty?: ReactNode;
  /**
   * Force a specific layout. Defaults to a matchMedia-driven choice
   * (`desktop` >= md, `mobile` < md). Tests pass an explicit value to
   * avoid relying on the JSDOM matchMedia stub.
   */
  readonly forceLayout?: "desktop" | "mobile";
}

/**
 * Responsive data table primitive (Wave M-1).
 *
 * Renders a real `<table>` at >= md (768 px) and a stacked card list below
 * that cut. Same column contract drives both layouts so the row data and
 * formatting stay identical across viewports.
 *
 * Styling is intentionally inline (CSSProperties) to match the rest of
 * apps/admin-web -- this project does not ship Tailwind yet. The visual
 * choices are defensive (system font, neutral palette) so consumers can
 * wrap the primitive in surface-specific containers without fighting
 * cascade order.
 */
export function ResponsiveTable<TRow>(props: ResponsiveTableProps<TRow>): JSX.Element {
  const {
    id,
    caption,
    columns,
    rows,
    rowKey,
    onRowClick,
    empty,
    forceLayout,
  } = props;
  const isDesktopAuto = useIsDesktop(true);
  const isDesktop = forceLayout ? forceLayout === "desktop" : isDesktopAuto;
  const keyFor = (row: TRow, index: number): string =>
    rowKey ? rowKey(row, index) : String(index);

  if (rows.length === 0 && empty !== undefined) {
    return (
      <div
        data-testid={id ? `${id}-empty` : undefined}
        data-responsive-table-empty="true"
        style={emptyContainerStyle}
        role="status"
      >
        {empty}
      </div>
    );
  }

  if (isDesktop) {
    return (
      <table
        data-testid={id}
        data-layout="desktop"
        style={tableStyle}
        role="table"
      >
        {caption !== undefined ? <caption style={captionStyle}>{caption}</caption> : null}
        <thead>
          <tr>
            {columns.map((col) => (
              <th
                key={col.id}
                scope="col"
                style={{
                  ...thStyle,
                  textAlign: col.align ?? "left",
                  width: col.width,
                }}
              >
                {col.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr
              key={keyFor(row, index)}
              data-row-index={index}
              onClick={onRowClick ? () => onRowClick(row, index) : undefined}
              style={onRowClick ? clickableRowStyle : undefined}
            >
              {columns.map((col) => (
                <td
                  key={col.id}
                  style={{
                    ...tdStyle,
                    textAlign: col.align ?? "left",
                  }}
                >
                  {col.renderCell(row, index)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    );
  }

  // Mobile: stacked card list. Same data, same renderCell contract.
  return (
    <ul
      data-testid={id}
      data-layout="mobile"
      style={cardListStyle}
      aria-label={typeof caption === "string" ? caption : undefined}
    >
      {rows.map((row, index) => {
        const primary = columns.find((c) => c.primary === true);
        const secondaries = columns.filter(
          (c) => c.primary !== true && c.hideOnMobile !== true,
        );
        return (
          <li
            key={keyFor(row, index)}
            data-row-index={index}
            onClick={onRowClick ? () => onRowClick(row, index) : undefined}
            style={onRowClick ? { ...cardStyle, cursor: "pointer" } : cardStyle}
          >
            {primary ? (
              <div style={cardTitleStyle}>{primary.renderCell(row, index)}</div>
            ) : null}
            <dl style={cardDlStyle}>
              {secondaries.map((col) => (
                <Fragment key={col.id}>
                  <dt style={cardDtStyle}>{col.header}</dt>
                  <dd style={cardDdStyle}>{col.renderCell(row, index)}</dd>
                </Fragment>
              ))}
            </dl>
          </li>
        );
      })}
    </ul>
  );
}

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 4,
};

const captionStyle: CSSProperties = {
  captionSide: "top",
  textAlign: "left",
  padding: "8px 12px",
  fontSize: 12,
  color: "#475569",
};

const thStyle: CSSProperties = {
  padding: "8px 12px",
  background: "#f1f5f9",
  borderBottom: "1px solid #e2e8f0",
  fontWeight: 600,
  fontSize: 12,
  color: "#334155",
};

const tdStyle: CSSProperties = {
  padding: "8px 12px",
  borderBottom: "1px solid #f1f5f9",
  verticalAlign: "top",
};

const clickableRowStyle: CSSProperties = {
  cursor: "pointer",
};

const cardListStyle: CSSProperties = {
  listStyle: "none",
  margin: 0,
  padding: 0,
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const cardStyle: CSSProperties = {
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  padding: 12,
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const cardTitleStyle: CSSProperties = {
  fontSize: 15,
  fontWeight: 600,
  color: "#0f172a",
  lineHeight: 1.3,
};

const cardDlStyle: CSSProperties = {
  margin: 0,
  display: "grid",
  gridTemplateColumns: "max-content 1fr",
  columnGap: 12,
  rowGap: 4,
  fontSize: 13,
};

const cardDtStyle: CSSProperties = {
  color: "#64748b",
  fontSize: 11,
  textTransform: "uppercase",
  letterSpacing: 0.4,
  alignSelf: "center",
};

const cardDdStyle: CSSProperties = {
  margin: 0,
  color: "#0f172a",
};

const emptyContainerStyle: CSSProperties = {
  padding: 16,
  background: "#f8fafc",
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  color: "#475569",
  fontSize: 13,
  textAlign: "center",
};
