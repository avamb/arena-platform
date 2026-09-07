import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useState, type CSSProperties, type FormEvent, type ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { RequirePermission } from "@/components/RequirePermission";
import { ResponsiveTable, type ResponsiveTableColumn } from "@/components/layout";
import { ApiError, authedFetch, createCustomerImport } from "@/lib/api/client";
import type {
  CreateCustomerImportRequest,
  CustomerImport,
  CustomerImportReport,
  CustomerImportRow,
  CustomerImportRowsResponse,
} from "@/lib/api/types";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/customer-imports",
  component: CustomerImportsRoute,
});

const CUSTOMER_IMPORTS_NAV_ENTRY = NAV_BY_PATH["/customer-imports"];
if (CUSTOMER_IMPORTS_NAV_ENTRY === undefined) {
  throw new Error(
    "customer-imports route: NAV_BY_PATH['/customer-imports'] missing",
  );
}

export type CustomerImportSourceLabel = CreateCustomerImportRequest["source_label"];

export const CUSTOMER_IMPORT_SOURCE_LABELS: readonly CustomerImportSourceLabel[] = [
  "bil24_orders_json",
  "wc_customers_csv",
  "gsheets_csv",
  "brevo_csv",
  "generic_csv",
] as const;

export type CustomerImportLegalBasis = CreateCustomerImportRequest["legal_basis"];

export const CUSTOMER_IMPORT_LEGAL_BASES: readonly CustomerImportLegalBasis[] = [
  "organizer_contract",
  "legitimate_interest",
  "explicit_consent",
] as const;

const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

interface CreateImportErrors {
  orgId?: string;
  fileMediaId?: string;
  mapping?: string;
  form?: string;
}

interface RowsFilterAction {
  readonly value: "" | "created" | "matched" | "merge_candidate" | "skipped";
  readonly label: string;
}

const ROW_ACTION_FILTERS: readonly RowsFilterAction[] = [
  { value: "", label: "All actions" },
  { value: "created", label: "Created" },
  { value: "matched", label: "Matched" },
  { value: "merge_candidate", label: "Merge candidate" },
  { value: "skipped", label: "Skipped" },
];

function CustomerImportsRoute() {
  return (
    <RequirePermission entry={CUSTOMER_IMPORTS_NAV_ENTRY}>
      <CustomerImportsPage />
    </RequirePermission>
  );
}

function CustomerImportsPage() {
  const [orgId, setOrgId] = useState("");
  const [sourceLabel, setSourceLabel] = useState<CustomerImportSourceLabel>(
    "bil24_orders_json",
  );
  const [fileMediaId, setFileMediaId] = useState("");
  const [mappingRaw, setMappingRaw] = useState("{}");
  const [legalBasis, setLegalBasis] = useState<CustomerImportLegalBasis>(
    "organizer_contract",
  );
  const [localErrors, setLocalErrors] = useState<CreateImportErrors>({});
  const [serverErrors, setServerErrors] = useState<CreateImportErrors>({});
  const [activeImport, setActiveImport] = useState<CustomerImport | null>(null);
  const [dryRunReport, setDryRunReport] = useState<CustomerImportReport | null>(null);
  const [applyReport, setApplyReport] = useState<CustomerImportReport | null>(null);
  const [lookupId, setLookupId] = useState("");
  const [lookupError, setLookupError] = useState<string | null>(null);
  const [rowsAction, setRowsAction] = useState<RowsFilterAction["value"]>("");

  const visibleErrors = { ...localErrors, ...serverErrors };

  const createMutation = useMutation<
    CustomerImport,
    ApiError,
    CreateCustomerImportRequest
  >({
    mutationFn: createCustomerImport,
    onSuccess: (data) => {
      setActiveImport(data);
      setDryRunReport(null);
      setApplyReport(null);
      setServerErrors({});
    },
    onError: (err) => {
      setServerErrors(mapCustomerImportServerError(err));
    },
  });

  const dryRunMutation = useMutation<CustomerImportReport, ApiError, string>({
    mutationFn: (id) =>
      authedFetch<CustomerImportReport>({
        method: "POST",
        path: `/v1/admin/customer-imports/${encodeURIComponent(id)}/dry-run`,
      }),
    onSuccess: async (report, id) => {
      setDryRunReport(report);
      await refetchImport(id);
    },
  });

  const applyMutation = useMutation<CustomerImportReport, ApiError, string>({
    mutationFn: (id) =>
      authedFetch<CustomerImportReport>({
        method: "POST",
        path: `/v1/admin/customer-imports/${encodeURIComponent(id)}/apply`,
      }),
    onSuccess: async (report, id) => {
      setApplyReport(report);
      await refetchImport(id);
      await rowsQuery.refetch();
    },
  });

  const importQuery = useQuery<CustomerImport, ApiError>({
    queryKey: ["admin", "customer-imports", activeImport?.id ?? ""],
    queryFn: () =>
      authedFetch<CustomerImport>({
        method: "GET",
        path: `/v1/admin/customer-imports/${encodeURIComponent(activeImport?.id ?? "")}`,
      }),
    enabled: activeImport !== null,
    retry: false,
  });

  async function refetchImport(id: string): Promise<void> {
    if (activeImport?.id !== id) {
      return;
    }
    const result = await importQuery.refetch();
    if (result.data !== undefined) {
      setActiveImport(result.data);
    }
  }

  const rowsQuery = useQuery<CustomerImportRowsResponse, ApiError>({
    queryKey: ["admin", "customer-imports", activeImport?.id ?? "", "rows", rowsAction],
    queryFn: () =>
      authedFetch<CustomerImportRowsResponse>({
        method: "GET",
        path: buildCustomerImportRowsPath(activeImport?.id ?? "", rowsAction),
      }),
    enabled: activeImport !== null,
    retry: false,
  });

  const displayedImport = importQuery.data ?? activeImport;

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerErrors({});

    const result = buildCreateCustomerImportBody(
      orgId,
      sourceLabel,
      fileMediaId,
      mappingRaw,
      legalBasis,
    );
    if (result.error !== undefined) {
      setLocalErrors(result.errors);
      return;
    }
    setLocalErrors({});
    createMutation.mutate(result.body);
  }

  function onLookup(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setLookupError(null);
    const trimmed = lookupId.trim();
    if (!UUID_RE.test(trimmed)) {
      setLookupError("Enter a valid import UUID.");
      return;
    }
    authedFetch<CustomerImport>({
      method: "GET",
      path: `/v1/admin/customer-imports/${encodeURIComponent(trimmed)}`,
    })
      .then((data) => {
        setActiveImport(data);
        setDryRunReport(null);
        setApplyReport(null);
      })
      .catch((err: unknown) => {
        setLookupError(err instanceof Error ? err.message : "Import not found.");
      });
  }

  return (
    <section aria-labelledby="customer-imports-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="customer-imports-heading" style={headingStyle}>
            Customer Imports
          </h1>
          <p style={subheadingStyle}>
            Register an uploaded customer/order export, dry-run it to preview
            resolution, then apply it. Applying is idempotent: re-applying the
            same file never creates duplicate customers.
          </p>
        </div>
      </header>

      <form onSubmit={onSubmit} style={formStyle} noValidate>
        <Field label="Organization ID" htmlFor="ci-org-id" error={visibleErrors.orgId} hint="Optional. Leave blank for a platform-wide import.">
          <input
            id="ci-org-id"
            type="text"
            value={orgId}
            onChange={(e) => setOrgId(e.target.value)}
            style={inputMonoStyle}
            autoComplete="off"
            spellCheck={false}
            data-testid="ci-org-id"
          />
        </Field>

        <Field label="Source format" htmlFor="ci-source-label" error={undefined} hint="Selects the parser used by dry-run/apply.">
          <select
            id="ci-source-label"
            value={sourceLabel}
            onChange={(e) => setSourceLabel(e.target.value as CustomerImportSourceLabel)}
            style={inputStyle}
            data-testid="ci-source-label"
          >
            {CUSTOMER_IMPORT_SOURCE_LABELS.map((label) => (
              <option key={label} value={label}>
                {formatSourceLabel(label)}
              </option>
            ))}
          </select>
        </Field>

        <Field label="File media ID" htmlFor="ci-file-media-id" error={visibleErrors.fileMediaId} hint="media_objects.id of the already-uploaded file.">
          <input
            id="ci-file-media-id"
            type="text"
            value={fileMediaId}
            onChange={(e) => setFileMediaId(e.target.value)}
            style={inputMonoStyle}
            autoComplete="off"
            spellCheck={false}
            data-testid="ci-file-media-id"
          />
        </Field>

        <Field label="Legal basis" htmlFor="ci-legal-basis" error={undefined} hint="GDPR basis recorded for this import.">
          <select
            id="ci-legal-basis"
            value={legalBasis}
            onChange={(e) => setLegalBasis(e.target.value as CustomerImportLegalBasis)}
            style={inputStyle}
            data-testid="ci-legal-basis"
          >
            {CUSTOMER_IMPORT_LEGAL_BASES.map((basis) => (
              <option key={basis} value={basis}>
                {formatLegalBasis(basis)}
              </option>
            ))}
          </select>
        </Field>

        <Field label="Column mapping (JSON)" htmlFor="ci-mapping" error={visibleErrors.mapping} hint="Optional parser overrides. Defaults to {}.">
          <textarea
            id="ci-mapping"
            value={mappingRaw}
            onChange={(e) => setMappingRaw(e.target.value)}
            style={textareaStyle}
            spellCheck={false}
            rows={4}
            data-testid="ci-mapping"
          />
        </Field>

        {visibleErrors.form !== undefined ? (
          <div style={formErrorStyle} role="alert" data-testid="ci-form-error">
            {visibleErrors.form}
          </div>
        ) : null}

        <div style={formActionsStyle}>
          <button
            type="submit"
            style={primaryButtonStyle}
            disabled={createMutation.isPending}
            data-testid="ci-submit"
          >
            {createMutation.isPending ? "Creating..." : "Create import"}
          </button>
        </div>
      </form>

      <section aria-labelledby="ci-lookup-heading" style={directoryStyle}>
        <h2 id="ci-lookup-heading" style={directoryHeadingStyle}>
          Open an existing import
        </h2>
        <form onSubmit={onLookup} style={searchFormStyle}>
          <input
            aria-label="Import ID"
            value={lookupId}
            onChange={(e) => setLookupId(e.target.value)}
            placeholder="Import UUID"
            style={inputMonoStyle}
            data-testid="ci-lookup-id"
          />
          <button type="submit" style={secondaryButtonStyle}>
            Open
          </button>
        </form>
        {lookupError !== null ? (
          <p role="alert" style={formErrorStyle}>
            {lookupError}
          </p>
        ) : null}
      </section>

      {displayedImport !== null && displayedImport !== undefined ? (
        <section aria-labelledby="ci-detail-heading" style={directoryStyle}>
          <div style={directoryHeaderStyle}>
            <div>
              <h2 id="ci-detail-heading" style={directoryHeadingStyle}>
                Import {displayedImport.id}
              </h2>
              <p style={subheadingStyle}>
                Status: {formatImportStatus(displayedImport.status)}
              </p>
            </div>
            <div style={drawerActionStyle}>
              <button
                type="button"
                style={secondaryButtonStyle}
                disabled={dryRunMutation.isPending}
                onClick={() => dryRunMutation.mutate(displayedImport.id)}
                data-testid="ci-dry-run"
              >
                {dryRunMutation.isPending ? "Running..." : "Dry-run"}
              </button>
              <button
                type="button"
                style={secondaryButtonStyle}
                disabled={applyMutation.isPending}
                onClick={() => applyMutation.mutate(displayedImport.id)}
                data-testid="ci-apply"
              >
                {applyMutation.isPending ? "Applying..." : "Apply"}
              </button>
            </div>
          </div>

          {dryRunMutation.isError ? (
            <p role="alert" style={formErrorStyle}>
              Dry-run failed: {dryRunMutation.error.message}
            </p>
          ) : null}
          {applyMutation.isError ? (
            <p role="alert" style={formErrorStyle}>
              Apply failed: {applyMutation.error.message}
            </p>
          ) : null}

          {dryRunReport !== null ? (
            <ReportSummary title="Dry-run report" report={dryRunReport} />
          ) : null}
          {applyReport !== null ? (
            <ReportSummary title="Apply report" report={applyReport} />
          ) : null}

          <div style={directoryHeaderStyle}>
            <h3 style={directoryHeadingStyle}>Rows</h3>
            <select
              aria-label="Filter rows by action"
              value={rowsAction}
              onChange={(e) => setRowsAction(e.target.value as RowsFilterAction["value"])}
              style={inputStyle}
              data-testid="ci-rows-action-filter"
            >
              {ROW_ACTION_FILTERS.map((filter) => (
                <option key={filter.value} value={filter.value}>
                  {filter.label}
                </option>
              ))}
            </select>
          </div>
          {rowsQuery.isPending ? <p role="status">Loading rows…</p> : null}
          {rowsQuery.isError ? (
            <p role="alert" style={formErrorStyle}>
              Unable to load rows: {rowsQuery.error.message}
            </p>
          ) : null}
          {rowsQuery.isSuccess ? <RowsTable rows={rowsQuery.data.rows} /> : null}
        </section>
      ) : null}
    </section>
  );
}

function ReportSummary({ title, report }: { title: string; report: CustomerImportReport }) {
  return (
    <div style={reportStyle} data-testid="ci-report">
      <strong>{title}</strong>
      <span>
        rows {report.rows} · created {report.created} · matched {report.matched} · merge
        candidates {report.merge_candidates} · skipped {report.skipped}
      </span>
      {report.errors.length > 0 ? (
        <ul>
          {report.errors.map((message, i) => (
            <li key={`${i}-${message}`}>{message}</li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function RowsTable({ rows }: { rows: readonly CustomerImportRow[] }) {
  const columns: readonly ResponsiveTableColumn<CustomerImportRow>[] = [
    { id: "row_no", header: "Row", primary: true, renderCell: (row) => row.row_no },
    { id: "action", header: "Action", renderCell: (row) => row.action ?? "—" },
    { id: "reason", header: "Reason", renderCell: (row) => row.reason ?? "—" },
    { id: "resolved_customer_id", header: "Customer", renderCell: (row) => row.resolved_customer_id ?? "—" },
  ];
  return (
    <ResponsiveTable
      id="customer-import-rows-table"
      caption="Import rows"
      columns={columns}
      rows={rows}
      rowKey={(row) => row.id}
      empty={<p>No rows match this filter.</p>}
    />
  );
}

function Field({
  label,
  htmlFor,
  error,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error: string | undefined;
  hint: string;
  children: ReactNode;
}) {
  return (
    <div style={fieldStyle}>
      <label htmlFor={htmlFor} style={labelStyle}>
        {label}
      </label>
      {children}
      {error !== undefined ? (
        <div style={fieldErrorStyle} role="alert" data-testid={`${htmlFor}-error`}>
          {error}
        </div>
      ) : (
        <div style={hintStyle}>{hint}</div>
      )}
    </div>
  );
}

// ── Pure helpers (Vitest-covered) ──────────────────────────────────────────

export function validateCustomerImportOrgId(raw: string): string | null {
  const value = raw.trim();
  if (value === "") {
    return null;
  }
  if (!UUID_RE.test(value)) {
    return "Organization ID must be a UUID.";
  }
  return null;
}

export function validateCustomerImportFileMediaId(raw: string): string | null {
  const value = raw.trim();
  if (value === "") {
    return "File media ID is required.";
  }
  if (!UUID_RE.test(value)) {
    return "File media ID must be a UUID.";
  }
  return null;
}

export interface ParsedMapping {
  readonly value?: Record<string, unknown>;
  readonly error?: string;
}

export function parseCustomerImportMapping(raw: string): ParsedMapping {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return { value: {} };
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return { error: "Mapping must be valid JSON." };
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { error: "Mapping must be a JSON object." };
  }
  return { value: parsed as Record<string, unknown> };
}

export type BuildCreateCustomerImportBodyResult =
  | { readonly body: CreateCustomerImportRequest; readonly error?: undefined }
  | { readonly error: true; readonly errors: CreateImportErrors };

export function buildCreateCustomerImportBody(
  rawOrgId: string,
  sourceLabel: CustomerImportSourceLabel,
  rawFileMediaId: string,
  rawMapping: string,
  legalBasis: CustomerImportLegalBasis,
): BuildCreateCustomerImportBodyResult {
  const errors: CreateImportErrors = {};

  const orgIdError = validateCustomerImportOrgId(rawOrgId);
  if (orgIdError !== null) {
    errors.orgId = orgIdError;
  }
  const fileMediaIdError = validateCustomerImportFileMediaId(rawFileMediaId);
  if (fileMediaIdError !== null) {
    errors.fileMediaId = fileMediaIdError;
  }
  const mapping = parseCustomerImportMapping(rawMapping);
  if (mapping.error !== undefined) {
    errors.mapping = mapping.error;
  }

  if (Object.keys(errors).length > 0) {
    return { error: true, errors };
  }

  const orgId = rawOrgId.trim();
  const body: CreateCustomerImportRequest = {
    source_label: sourceLabel,
    file_media_id: rawFileMediaId.trim(),
    legal_basis: legalBasis,
    mapping: mapping.value ?? {},
  };
  if (orgId !== "") {
    body.org_id = orgId;
  }
  return { body };
}

export function mapCustomerImportServerError(err: ApiError): CreateImportErrors {
  if (err.details?.field === "org_id") {
    return { orgId: err.message };
  }
  if (err.details?.field === "file_media_id") {
    return { fileMediaId: err.message };
  }
  if (err.details?.field === "mapping") {
    return { mapping: err.message };
  }
  switch (err.code) {
    case "customer_import.invalid_org_id":
      return { orgId: err.message };
    case "customer_import.media_not_found":
    case "customer_import.invalid_file_media_id":
      return { fileMediaId: err.message };
    case "customer_import.invalid_mapping":
      return { mapping: err.message };
    case "permissions.denied":
      return { form: "Your account is missing superadmin.read." };
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      return { form: "An audit reason is required before creating an import." };
    case "dependency.database_unavailable":
      return { form: "Database is unavailable. Retry after the backend recovers." };
    default:
      return { form: `${err.message} (${err.code})` };
  }
}

export function formatImportStatus(status: CustomerImport["status"]): string {
  switch (status) {
    case "uploaded":
      return "Uploaded";
    case "dry_run_running":
      return "Dry-run running";
    case "dry_run_done":
      return "Dry-run done";
    case "applying":
      return "Applying";
    case "applied":
      return "Applied";
    case "failed":
      return "Failed";
    default:
      return status;
  }
}

export function formatSourceLabel(label: CustomerImportSourceLabel): string {
  switch (label) {
    case "bil24_orders_json":
      return "Bil24 orders (JSON)";
    case "wc_customers_csv":
      return "WooCommerce customers (CSV)";
    case "gsheets_csv":
      return "Google Sheets export (CSV)";
    case "brevo_csv":
      return "Brevo contacts (CSV)";
    case "generic_csv":
      return "Generic CSV";
    default:
      return label;
  }
}

export function formatLegalBasis(basis: CustomerImportLegalBasis): string {
  switch (basis) {
    case "organizer_contract":
      return "Organizer contract";
    case "legitimate_interest":
      return "Legitimate interest";
    case "explicit_consent":
      return "Explicit consent";
    default:
      return basis;
  }
}

export function buildCustomerImportRowsPath(
  importId: string,
  action: RowsFilterAction["value"],
): string {
  const base = `/v1/admin/customer-imports/${encodeURIComponent(importId)}/rows`;
  return action === "" ? base : `${base}?action=${encodeURIComponent(action)}`;
}

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
};

const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: 0,
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const formStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(260px, 1fr))",
  gap: 14,
  padding: 16,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const fieldStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const labelStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
};

const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const inputMonoStyle: CSSProperties = {
  ...inputStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const textareaStyle: CSSProperties = {
  ...inputMonoStyle,
  resize: "vertical",
  minHeight: 72,
};

const hintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
};

const fieldErrorStyle: CSSProperties = {
  fontSize: 11,
  color: "#b91c1c",
  fontWeight: 500,
};

const formErrorStyle: CSSProperties = {
  gridColumn: "1 / -1",
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const formActionsStyle: CSSProperties = {
  gridColumn: "1 / -1",
  display: "flex",
  justifyContent: "flex-end",
};

const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "7px 14px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const directoryStyle: CSSProperties = {
  padding: 16,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  display: "flex",
  flexDirection: "column",
  gap: 12,
};

const directoryHeaderStyle: CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  gap: 12,
  alignItems: "flex-start",
  flexWrap: "wrap",
};

const directoryHeadingStyle: CSSProperties = { margin: 0, fontSize: 16, fontWeight: 600 };
const searchFormStyle: CSSProperties = { display: "flex", gap: 8, flexWrap: "wrap" };
const secondaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "7px 14px",
  background: "#ffffff",
  border: "1px solid #94a3b8",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
  fontWeight: 600,
};
const drawerActionStyle: CSSProperties = { display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" };
const reportStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: 12,
  border: "1px solid #bae6fd",
  borderRadius: 6,
  background: "#f0f9ff",
  color: "#0c4a6e",
  fontSize: 12,
};
