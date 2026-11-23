/**
 * "Advertising → Meta" (spec 66, sections 4.1 and 4.6): the owner connects the organization's Meta business account,
 * chooses the dataset for the server-side purchases, the ad account and the Facebook Page, sees a live card of the
 * selected Page (read from Meta on each visit, nothing stored), and disconnects.
 *
 * States: the platform switch is off (nothing to connect), not connected, connected, error (Meta refused the token),
 * revoked. Connect is the server redirect to Meta's login (`login_url`, no SDK, no popup); the person comes back with
 * `?meta=finish` (boot code moved code and state into sessionStorage) or `?meta=error&reason=<code>`. On finish the page takes the stored code and state once (and deletes them) and posts code and state ONCE to the finish route in the signed-in session (no login CSRF:
 * the server checks that the state names this person and this organization). The access token never reaches the page.
 * The view is a pure component (`MetaAdsView`); the screen owns the queries and the actions.
 */
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearch } from "@tanstack/react-router";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { SelectField } from "@/components/FormField";
import { ScreenHelp } from "@/components/ScreenHelp";
import { ErrorBox, Loading } from "@/components/ui";
import {
  disconnectMeta,
  fetchMetaIntegration,
  fetchMetaPage,
  finishMetaConnect,
  selectMetaAssets,
  startMetaConnect,
  type MetaAsset,
  type MetaConnection,
  type MetaIntegration,
} from "@/lib/api/metaApi";
import { useMe } from "@/lib/auth";
import { formatDateTime, formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { REASON_KEY, finishReason, metaReturn, startRedirect, takeMetaFinish, type MetaReturn } from "@/lib/metaConnect";
import { browserZone } from "@/lib/shop";
import { useOrg } from "@/screens/OrgLayout";

const KINDS: readonly { kind: MetaAsset["kind"]; title: "meta.datasets" | "meta.adAccounts" | "meta.pages" }[] = [
  { kind: "dataset", title: "meta.datasets" },
  { kind: "ad_account", title: "meta.adAccounts" },
  { kind: "page", title: "meta.pages" },
];

export type MetaNotice = "notConfigured" | "saved" | null;

export type MetaSelectChange = { dataset_id?: string; ad_account_id?: string; page_id?: string };

export interface MetaAdsViewProps {
  data: MetaIntegration;
  /** What the callback told when the person came back. */
  returned: MetaReturn;
  /** The id of the signed-in user: the connection made by this person says «you». */
  meId?: string;
  notice: MetaNotice;
  connecting: boolean;
  /** A finish is in flight: no new connect. */
  finishing?: boolean;
  saving: boolean;
  failure: unknown;
  confirming: boolean;
  onConnect: () => void;
  onSelect: (change: MetaSelectChange) => void;
  /** The preview of the selected Page (read live from Meta); shown only for an active connection with a page. */
  renderPage?: (pageId: string) => ReactNode;
  onAskDisconnect: () => void;
  onDisconnect: () => Promise<void>;
  onCancelDisconnect: () => void;
}

function statusLabel(c: MetaConnection): "meta.status.active" | "meta.status.error" | "meta.status.revoked" {
  return c.status === "active" ? "meta.status.active" : c.status === "error" ? "meta.status.error" : "meta.status.revoked";
}

/** The page without queries: it shows exactly what it is given. */
export function MetaAdsView(p: MetaAdsViewProps) {
  const { t, locale } = useI18n();
  const { data } = p;
  const conn = data.connection;
  const messages = (
    <>
      {p.returned?.kind === "connected" && (
        <p className="notice notice-ok" role="status" data-testid="meta-returned-ok">
          {t("meta.result.connected")}
        </p>
      )}
      {p.returned?.kind === "error" && (
        <p className="notice notice-bad" role="alert" data-testid="meta-returned-error" data-reason={p.returned.reason}>
          {t(REASON_KEY[p.returned.reason])}
        </p>
      )}
      {p.notice === "notConfigured" && (
        <p className="notice notice-bad" role="alert" data-testid="meta-not-configured">
          {t("meta.notConfigured")}
        </p>
      )}
      {p.failure !== null && p.failure !== undefined && <ErrorNotice error={p.failure} testId="meta-error" />}
    </>
  );

  if (!data.enabled) {
    return (
      <>
        {messages}
        <p className="notice" role="status" data-testid="meta-off">
          {t("meta.off")}
        </p>
      </>
    );
  }

  const connectButton = (label: "meta.connect" | "meta.reconnect") =>
    p.finishing === true ? null : (
    <button type="button" className="btn btn-primary" disabled={p.connecting} onClick={p.onConnect} data-testid="meta-connect">
      {p.connecting ? t("meta.connecting") : t(label)}
    </button>
    );

  if (conn === null) {
    return (
      <>
        {messages}
        <p data-testid="meta-not-connected">{t("meta.notConnected")}</p>
        <p>{connectButton("meta.connect")}</p>
      </>
    );
  }

  const assetsOf = (kind: MetaAsset["kind"]) => conn.assets.filter((a) => a.kind === kind);
  const name = (a: MetaAsset) => (a.name !== "" ? a.name : t("meta.assetUnnamed"));
  const usable = conn.status === "active";
  const zone = browserZone();
  const selectedPageId = conn.assets.find((a) => a.kind === "page" && a.selected)?.meta_id ?? "";
  const PICKERS = {
    dataset: { label: "meta.pick.dataset", testId: "meta-pick-dataset", selected: conn.selected_dataset_id ?? "", change: (v: string) => ({ dataset_id: v }) },
    ad_account: { label: "meta.pick.adAccount", testId: "meta-pick-ad-account", selected: conn.selected_ad_account_id ?? "", change: (v: string) => ({ ad_account_id: v }) },
    page: { label: "meta.pick.page", testId: "meta-pick-page", selected: selectedPageId, change: (v: string) => ({ page_id: v }) },
  } as const;
  const picker = (kind: MetaAsset["kind"]) => {
    const list = assetsOf(kind);
    const cfg = PICKERS[kind];
    const selected = cfg.selected;
    return (
      <SelectField
        label={t(cfg.label)}
        value={selected}
        testId={cfg.testId}
        onChange={(v) => {
          if (v === "" || v === selected) return;
          p.onSelect(cfg.change(v));
        }}
      >
        <option value="" disabled>
          {t("meta.pick.none")}
        </option>
        {list.map((a) => (
          <option key={a.meta_id} value={a.meta_id}>
            {name(a)} ({a.meta_id})
          </option>
        ))}
      </SelectField>
    );
  };

  return (
    <>
      {messages}
      <p data-testid="meta-status" data-status={conn.status}>
        {t("meta.status.label")}: <span className={`badge badge-${usable ? "ok" : "bad"}`}>{t(statusLabel(conn))}</span>
      </p>
      {conn.status === "error" && (
        <p className="notice notice-bad" role="alert" data-testid="meta-last-error">
          {t("meta.errorText")}
          {conn.last_error !== null && conn.last_error !== "" && (
            <>
              {" "}
              <code>{conn.last_error}</code>
            </>
          )}
        </p>
      )}
      {conn.status === "revoked" && (
        <p className="notice" role="status" data-testid="meta-revoked">
          {t("meta.revokedText")}
        </p>
      )}
      {usable && (
        <dl className="kv" data-testid="meta-details">
          <dt>{t("meta.business")}</dt>
          <dd data-testid="meta-business">{conn.meta_business_id !== "" ? conn.meta_business_id : t("meta.businessNone")}</dd>
          {conn.connected_by !== null && conn.connected_by === p.meId && (
            <>
              <dt>{t("meta.connectedBy")}</dt>
              <dd data-testid="meta-connected-by">{t("meta.connectedByYou")}</dd>
            </>
          )}
          <dt>{t("meta.connectedAt")}</dt>
          <dd data-testid="meta-connected-at">{formatDateTime(conn.connected_at, zone, locale)}</dd>
          <dt>{t("meta.scopes")}</dt>
          <dd data-testid="meta-scopes">{conn.scopes.length > 0 ? conn.scopes.join(", ") : t("meta.scopesNone")}</dd>
        </dl>
      )}
      {usable && (
        <div data-testid="meta-pickers" aria-busy={p.saving}>
          <fieldset disabled={p.saving}>
            {picker("dataset")}
            {picker("ad_account")}
            {picker("page")}
          </fieldset>
          {p.saving && <p role="status">{t("meta.saving")}</p>}
          {p.notice === "saved" && !p.saving && (
            <p className="notice notice-ok" role="status" data-testid="meta-saved">
              {t("meta.saved")}
            </p>
          )}
        </div>
      )}
      {usable && selectedPageId !== "" && p.renderPage?.(selectedPageId)}
      {usable &&
        KINDS.map(({ kind, title }) => {
          const list = assetsOf(kind);
          return (
            <section key={kind} aria-labelledby={`meta-assets-${kind}`} data-testid={`meta-assets-${kind}`}>
              <h2 id={`meta-assets-${kind}`}>{t(title)}</h2>
              {list.length === 0 ? (
                <p>{t("meta.assetsNone")}</p>
              ) : (
                <ul>
                  {list.map((a) => (
                    <li key={a.meta_id} data-selected={a.selected ? "true" : undefined}>
                      {name(a)} ({a.meta_id}){a.currency !== null && a.currency !== "" ? `, ${a.currency}` : ""}
                      {a.selected ? " ✓" : ""}
                    </li>
                  ))}
                </ul>
              )}
            </section>
          );
        })}
      {usable ? (
        <p className="actions">
          <button type="button" className="btn" disabled={p.connecting} onClick={p.onAskDisconnect} data-testid="meta-disconnect">
            {t("meta.disconnect")}
          </button>
        </p>
      ) : (
        <p className="actions">{connectButton("meta.reconnect")}</p>
      )}
      {p.confirming && (
        <ConfirmPressDialog
          title={t("meta.disconnect.title")}
          explain={t("meta.disconnect.explain")}
          confirmLabel={t("meta.disconnect.confirm")}
          testId="meta-disconnect-confirm"
          onConfirm={p.onDisconnect}
          onCancel={p.onCancelDisconnect}
        />
      )}
    </>
  );
}

/**
 * The card of the selected Facebook Page: name, category, link, likes and picture, read live from Meta by the server
 * (nothing is stored). The picture comes as a data: URL, so the page loads no third-party image. On any error one
 * neutral line, never Meta's text.
 */
export function MetaPageCard({ orgId, pageId }: { orgId: string; pageId: string }) {
  const { t, locale } = useI18n();
  const query = useQuery({ queryKey: ["metaPage", orgId, pageId], queryFn: () => fetchMetaPage(orgId), retry: false, staleTime: 60_000 });
  return (
    <section aria-labelledby="meta-page-title" data-testid="meta-page-card">
      <h2 id="meta-page-title">{t("meta.page.title")}</h2>
      {query.isLoading && <p role="status">{t("meta.page.loading")}</p>}
      {query.isError && (
        <p className="notice" role="status" data-testid="meta-page-error">
          {t("meta.page.unavailable")}
        </p>
      )}
      {query.data !== undefined && (
        <div className="card" data-testid="meta-page-preview">
          {query.data.picture_data_url !== null && query.data.picture_data_url.startsWith("data:image/") && (
            <img src={query.data.picture_data_url} alt="" width={50} height={50} data-testid="meta-page-picture" />
          )}
          <p className="card-title" data-testid="meta-page-name">
            {query.data.name !== "" ? query.data.name : t("meta.assetUnnamed")}
          </p>
          <dl className="kv">
            {query.data.category !== null && (
              <>
                <dt>{t("meta.page.category")}</dt>
                <dd data-testid="meta-page-category">{query.data.category}</dd>
              </>
            )}
            {query.data.fan_count !== null && (
              <>
                <dt>{t("meta.page.fans")}</dt>
                <dd data-testid="meta-page-fans">{formatNumber(query.data.fan_count, locale)}</dd>
              </>
            )}
          </dl>
          {query.data.link !== null && query.data.link.startsWith("https://") && (
            <p>
              <a href={query.data.link} target="_blank" rel="noopener noreferrer" data-testid="meta-page-link">
                {t("meta.page.open")}
              </a>
            </p>
          )}
        </div>
      )}
    </section>
  );
}

export interface MetaAdsBodyProps {
  orgId: string;
  returned: MetaReturn;
  meId?: string;
  /** Where the browser goes, replaceable in a test. */
  navigate?: (url: string) => void;
}

/** One finish per login: React may run the effect twice, and Meta accepts a code only once. */
let lastFinish: { state: string; done: Promise<MetaIntegration> } | null = null;

function finishOnce(orgId: string, login: { code: string; state: string }): Promise<MetaIntegration> {
  if (lastFinish?.state === login.state) return lastFinish.done;
  const done = finishMetaConnect(orgId, login);
  lastFinish = { state: login.state, done };
  return done;
}

export function MetaAdsBody({ orgId, returned, meId, navigate }: MetaAdsBodyProps) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const key = ["metaIntegration", orgId];
  const query = useQuery({ queryKey: key, queryFn: () => fetchMetaIntegration(orgId), retry: false });
  const isFinish = returned?.kind === "finish";
  const [result, setResult] = useState<MetaReturn>(isFinish ? null : returned);
  const [finishing, setFinishing] = useState(isFinish);
  // StrictMode runs the effect twice with the same refs: the stored entry is taken once.
  const taken = useRef<{ login: { code: string; state: string } | null } | null>(null);
  useEffect(() => {
    if (!isFinish) return;
    if (taken.current === null) {
      const entry = takeMetaFinish();
      taken.current = { login: entry !== null && entry.org === orgId ? { code: entry.code, state: entry.state } : null };
    }
    const login = taken.current.login;
    if (login === null) {
      setResult({ kind: "error", reason: "no_code" });
      setFinishing(false);
      return;
    }
    finishOnce(orgId, login)
      .then(async (stored) => {
        // A GET started before the connection was stored must not overwrite it.
        await queryClient.cancelQueries({ queryKey: ["metaIntegration", orgId] });
        queryClient.setQueryData(["metaIntegration", orgId], stored);
        setResult({ kind: "connected" });
      })
      .catch((error: unknown) => setResult({ kind: "error", reason: finishReason(error) }))
      .finally(() => setFinishing(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per mount: the login is taken once
  }, []);
  const [connecting, setConnecting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState<MetaNotice>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const [confirming, setConfirming] = useState(false);

  const connect = async () => {
    setConnecting(true);
    setNotice(null);
    setFailure(null);
    try {
      const start = await startMetaConnect(orgId);
      const out = startRedirect(start, navigate ?? ((url) => window.location.assign(url)));
      if (out === "notConfigured") setNotice("notConfigured");
    } catch (error) {
      setFailure(error);
    } finally {
      setConnecting(false);
    }
  };

  const select = async (change: MetaSelectChange) => {
    setSaving(true);
    setNotice(null);
    setFailure(null);
    try {
      const stored = await selectMetaAssets(orgId, change);
      queryClient.setQueryData(key, stored);
      setNotice("saved");
    } catch (error) {
      setFailure(error);
    } finally {
      setSaving(false);
    }
  };

  const disconnect = async () => {
    try {
      const stored = await disconnectMeta(orgId);
      // A GET started before the disconnect must not overwrite the answer; the page card's cache goes too.
      await queryClient.cancelQueries({ queryKey: key });
      queryClient.setQueryData(key, stored);
      await queryClient.invalidateQueries({ queryKey: key });
      queryClient.removeQueries({ queryKey: ["metaPage", orgId] });
      setConfirming(false);
      setNotice(null);
      setFailure(null);
    } catch (error) {
      setConfirming(false);
      setFailure(error);
    }
  };

  return (
    <section aria-labelledby="meta-title" data-testid="meta-ads">
      <div className="screen-head">
        <h1 id="meta-title">{t("meta.title")}</h1>
        <ScreenHelp screenId="adsMeta" />
      </div>
      <p>{t("meta.intro")}</p>
      {finishing && (
        <p role="status" data-testid="meta-finishing">
          {t("meta.finishing")}
        </p>
      )}
      {query.isLoading && <Loading label={t("meta.loading")} />}
      {query.error !== null && <ErrorBox error={query.error} onRetry={() => void query.refetch()} />}
      {query.data !== undefined && (
        <MetaAdsView
          data={query.data}
          returned={result}
          meId={meId}
          notice={notice}
          connecting={connecting}
          finishing={finishing}
          saving={saving}
          failure={failure}
          confirming={confirming}
          onConnect={() => void connect()}
          onSelect={(c) => void select(c)}
          renderPage={(pageId) => <MetaPageCard orgId={orgId} pageId={pageId} />}
          onAskDisconnect={() => setConfirming(true)}
          onDisconnect={disconnect}
          onCancelDisconnect={() => setConfirming(false)}
        />
      )}
    </section>
  );
}

export function MetaAdsScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const me = useMe();
  const search = useSearch({ strict: false }) as { meta?: string; reason?: string };
  if (!access.can("org.update")) {
    return (
      <section aria-labelledby="meta-title" data-testid="meta-owner-only">
        <h1 id="meta-title">{t("meta.title")}</h1>
        <p className="notice" role="status">
          <strong>{t("meta.ownerOnly.title")}</strong> {t("meta.ownerOnly.text")}
        </p>
      </section>
    );
  }
  return <MetaAdsBody orgId={orgId} returned={metaReturn(search.meta, search.reason)} meId={me.data?.user.id} />;
}
