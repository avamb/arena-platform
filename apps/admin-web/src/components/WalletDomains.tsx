import { Fragment, type CSSProperties } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError, authedFetch } from "@/lib/api/client";

/** One row of GET .../payment-configs/{id}/payment-method-domains. */
export interface WalletDomain {
  readonly domain: string;
  readonly source: "platform" | "site" | string;
  readonly enabled: boolean | null;
  readonly apple_pay_status: string | null;
  readonly google_pay_status: string | null;
  readonly link_status: string | null;
  readonly status_detail?: string | null;
  readonly last_error?: string | null;
  readonly checked_at: string | null;
}

export interface WalletDomainsEnvelope {
  readonly payment_method_domains: readonly WalletDomain[];
}

export function walletDomainsQueryKey(orgId: string, configId: string) {
  return ["wallet_domains", orgId, configId] as const;
}

const badgeStyle: CSSProperties = {
  display: "inline-block",
  padding: "2px 8px",
  fontSize: 11,
  fontWeight: 600,
  borderRadius: 999,
  border: "1px solid transparent",
};

/** Stripe's "active" is green, "inactive" is red, anything unknown is "not checked". */
export function WalletStatus({ status, testId }: { status: string | null | undefined; testId?: string }) {
  if (status === "active") {
    return (
      <span
        style={{ ...badgeStyle, background: "#dcfce7", color: "#166534", borderColor: "#bbf7d0" }}
        data-testid={testId}
      >
        active
      </span>
    );
  }
  if (status === "inactive") {
    return (
      <span
        style={{ ...badgeStyle, background: "#fee2e2", color: "#7f1d1d", borderColor: "#fecaca" }}
        data-testid={testId}
      >
        inactive
      </span>
    );
  }
  return (
    <span
      style={{ ...badgeStyle, background: "#f1f5f9", color: "#475569", borderColor: "#e2e8f0" }}
      data-testid={testId}
    >
      not checked
    </span>
  );
}

function formatChecked(iso: string | null): string {
  if (!iso) return "not checked";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString().replace("T", " ").slice(0, 16) + " UTC";
}

const cellStyle: CSSProperties = { padding: "4px 8px", textAlign: "left", verticalAlign: "top" };

/** The table itself, free of queries so it renders on its own. */
export function WalletDomainsTable({
  configId,
  domains,
}: {
  configId: string;
  domains: readonly WalletDomain[];
}) {
  if (domains.length === 0) {
    return (
      <p style={{ margin: 0, fontSize: 12, color: "#64748b" }} data-testid={`wallet-domains-empty-${configId}`}>
        Domains register after a successful check.
      </p>
    );
  }
  return (
    <table style={{ borderCollapse: "collapse", fontSize: 12 }} data-testid={`wallet-domains-table-${configId}`}>
      <thead>
        <tr>
          <th style={cellStyle}>Domain</th>
          <th style={cellStyle}>Source</th>
          <th style={cellStyle}>Apple Pay</th>
          <th style={cellStyle}>Google Pay</th>
          <th style={cellStyle}>Link</th>
          <th style={cellStyle}>Checked</th>
        </tr>
      </thead>
      <tbody>
        {domains.map((d) => {
          const key = `${configId}-${d.domain}`;
          return (
            <Fragment key={key}>
              <tr data-testid={`wallet-domain-row-${d.domain}`}>
                <td style={cellStyle}>{d.domain}</td>
                <td style={cellStyle}>{d.source}</td>
                <td style={cellStyle}>
                  <WalletStatus status={d.apple_pay_status} testId={`wallet-apple-${d.domain}`} />
                </td>
                <td style={cellStyle}>
                  <WalletStatus status={d.google_pay_status} testId={`wallet-google-${d.domain}`} />
                </td>
                <td style={cellStyle}>
                  <WalletStatus status={d.link_status} testId={`wallet-link-${d.domain}`} />
                </td>
                <td style={cellStyle}>{formatChecked(d.checked_at)}</td>
              </tr>
              {d.status_detail ? (
                <tr>
                  <td colSpan={6} style={{ ...cellStyle, color: "#475569" }} data-testid={`wallet-domain-detail-${d.domain}`}>
                    {d.status_detail}
                  </td>
                </tr>
              ) : null}
              {d.last_error ? (
                <tr>
                  <td colSpan={6} style={{ ...cellStyle, color: "#b91c1c" }} data-testid={`wallet-domain-error-${d.domain}`}>
                    {d.last_error}
                  </td>
                </tr>
              ) : null}
            </Fragment>
          );
        })}
      </tbody>
    </table>
  );
}

/** The "Wallet domains" block of a Stripe config: loads the list, refreshed from the verify answer. */
export function WalletDomainsBlock({ orgId, configId }: { orgId: string; configId: string }) {
  const query = useQuery<WalletDomainsEnvelope, ApiError>({
    queryKey: walletDomainsQueryKey(orgId, configId),
    queryFn: () =>
      authedFetch<WalletDomainsEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/payment-configs/${configId}/payment-method-domains`,
      }),
    retry: false,
  });
  return (
    <div style={{ marginTop: 8 }} data-testid={`wallet-domains-${configId}`}>
      <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 4 }}>Wallet domains</div>
      {query.isLoading ? (
        <p style={{ margin: 0, fontSize: 12, color: "#64748b" }}>Loading…</p>
      ) : query.error ? (
        <p style={{ margin: 0, fontSize: 12, color: "#b91c1c" }} data-testid={`wallet-domains-load-error-${configId}`}>
          We did not load the wallet domains: {query.error.message}
        </p>
      ) : (
        <WalletDomainsTable configId={configId} domains={query.data?.payment_method_domains ?? []} />
      )}
    </div>
  );
}
