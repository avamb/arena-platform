import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, authedFetch } from "@/lib/api/client";

interface SiteDomainsEnvelope {
  readonly domains: readonly string[];
}

/** One host per line; blank lines and surrounding spaces are dropped. */
export function parseSiteDomains(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter((l) => l.length > 0);
}

/** The editor's body, free of queries so it renders on its own. */
export function SiteDomainsForm({
  text,
  onText,
  onSave,
  saving,
  error,
  saved,
  disabled,
}: {
  text: string;
  onText: (v: string) => void;
  onSave: () => void;
  saving: boolean;
  error: string | null;
  saved: boolean;
  disabled?: boolean;
}) {
  return (
    <section style={{ marginTop: 16 }} data-testid="channels-site-domains-section">
      <h3 style={{ margin: "0 0 4px", fontSize: 14 }}>Sites with the widget</h3>
      <p style={{ margin: "0 0 6px", fontSize: 11, color: "#64748b" }}>
        One site host per line, for example tickets.example.com. Apple Pay and Google Pay
        are registered for each of them.
      </p>
      <textarea
        value={text}
        onChange={(e) => onText(e.target.value)}
        rows={4}
        style={{ width: "100%", fontFamily: "monospace", fontSize: 12 }}
        disabled={disabled}
        data-testid="channels-site-domains-input"
      />
      {error ? (
        <p style={{ margin: "4px 0", fontSize: 12, color: "#b91c1c" }} role="alert" data-testid="channels-site-domains-error">
          {error}
        </p>
      ) : null}
      {saved && !error ? (
        <p style={{ margin: "4px 0", fontSize: 12, color: "#166534" }} data-testid="channels-site-domains-saved">
          Saved.
        </p>
      ) : null}
      <button
        type="button"
        onClick={onSave}
        disabled={saving || disabled}
        data-testid="channels-site-domains-save"
      >
        {saving ? "Saving…" : "Save sites"}
      </button>
    </section>
  );
}

export function SiteDomainsEditor({
  orgId,
  channelId,
  canUpdate,
}: {
  orgId: string;
  channelId: string;
  canUpdate: boolean;
}) {
  const queryClient = useQueryClient();
  const path = `/v1/organizations/${orgId}/channels/${channelId}/site-domains`;
  const queryKey = ["site-domains", orgId, channelId];
  const [edit, setEdit] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const query = useQuery<SiteDomainsEnvelope, ApiError>({
    queryKey,
    queryFn: () => authedFetch<SiteDomainsEnvelope>({ method: "GET", path }),
    retry: false,
  });
  const text = edit ?? (query.data?.domains ?? []).join("\n");

  const mutation = useMutation<SiteDomainsEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<SiteDomainsEnvelope>({ method: "PUT", path, body: { domains: parseSiteDomains(text) } }),
    onSuccess: (data) => {
      setError(null);
      setSaved(true);
      setEdit(null);
      queryClient.setQueryData(queryKey, data);
    },
    onError: (err) => {
      setSaved(false);
      setError(err.message || "We did not save the sites.");
    },
  });

  return (
    <SiteDomainsForm
      text={text}
      onText={(v) => {
        setEdit(v);
        setSaved(false);
      }}
      onSave={() => mutation.mutate()}
      saving={mutation.isPending}
      error={error ?? (query.error ? `We did not load the sites: ${query.error.message}` : null)}
      saved={saved}
      disabled={!canUpdate || query.isLoading}
    />
  );
}
