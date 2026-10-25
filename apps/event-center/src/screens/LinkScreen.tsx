/**
 * The card of one sales link (spec 43 section 5.3, QL-12): the big QR with
 * its two downloads, the short address with "Copy", "Now leads to" and the
 * reason from the server's destination_reason, the statistics, a retarget
 * with the same target choice as the create, the label / placement / utm
 * edit, the archive and the way back. Every write needs sales_link.manage;
 * the API decides in the end.
 */
import { useId, useState, type FormEvent } from "react";
import { Link, useParams, useSearch } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetchSalesLink, listSessions, updateSalesLink } from "@/lib/api/endpoints";
import type { SalesLink, SalesLinkTarget, SalesLinkUpdateRequest } from "@/lib/api/types";
import { formatDateTime, formatNumber } from "@/lib/format";
import { useI18n, type Params } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import {
  PLACEMENTS,
  isPlacement,
  parseLabel,
  parseUtm,
  placementKey,
  reasonKey,
  retargetHelps,
  sameTarget,
  targetBody,
  targetComplete,
  utmFields,
  type Placement,
  type UtmKey,
} from "@/lib/salesLinks";
import { ConfirmPressDialog } from "@/components/ConfirmPressDialog";
import { ErrorNotice } from "@/components/ErrorNotice";
import { SalesLinkQr } from "@/components/SalesLinkQr";
import { SalesLinkStatsBlock } from "@/components/SalesLinkStatsBlock";
import { SalesLinkTargetPicker, type TargetChoice } from "@/components/SalesLinkTargetPicker";
import { ErrorBox, Loading } from "@/components/ui";
import { LinkStatusBadge, whereText } from "@/screens/LinksScreen";
import { NoLinksAccess } from "@/screens/LinksNoAccess";
import { useOrg } from "@/screens/OrgLayout";
import { useEventsList } from "@/screens/EventsScreen";

interface Notice {
  key: MessageKey;
  params?: Params;
}

function ShortAddress({ value }: { value: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
    } catch {
      /* the text is selectable: the person can copy it by hand */
    }
  };
  return (
    <div className="pay-ref" data-testid="link-short-box">
      <p className="muted">{t("links.card.short")}</p>
      <p>
        <code className="pay-ref-code mono" data-testid="link-short-url">
          {value}
        </code>
      </p>
      <button type="button" className="btn" onClick={() => void copy()} data-testid="link-copy">
        {copied ? t("links.card.copied") : t("links.card.copy")}
      </button>
      <span role="status" className="visually-hidden">
        {copied ? t("links.card.copied") : ""}
      </span>
    </div>
  );
}

function RetargetPanel({
  orgId,
  orgName,
  link,
  canSales,
  onDone,
  onClose,
}: {
  orgId: string;
  orgName: string;
  link: SalesLink;
  canSales: boolean;
  onDone: (saved: SalesLink, notice: Notice) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [choice, setChoice] = useState<TargetChoice>({ target: null, text: "" });
  const [same, setSame] = useState(false);
  const save = useMutation({
    mutationFn: (target: SalesLinkTarget) => updateSalesLink(orgId, link.id, { target: targetBody(target) }),
    onSuccess: (saved) => {
      // "A new page" only when the address really changed (the bot's rt_done / rt_done_same_dest / rt_done_no_dest).
      const key: MessageKey =
        saved.destination_url === "" ? "links.retarget.doneNoDest" : saved.destination_url !== link.destination_url ? "links.retarget.done" : "links.retarget.doneSame";
      onDone(saved, { key, params: { label: saved.label } });
    },
  });
  const ready = targetComplete(choice.target);
  return (
    <section className="panel" aria-labelledby="link-retarget-title" data-testid="link-retarget">
      <h2 id="link-retarget-title">{t("links.retarget.title")}</h2>
      <p>{t("links.retarget.explain")}</p>
      <SalesLinkTargetPicker orgId={orgId} orgName={orgName} initial={link.target} onChange={setChoice} canSales={canSales} disabled={save.isPending} />
      {same && (
        <p className="notice" role="status" data-testid="link-retarget-same">
          {t("links.retarget.same")}
        </p>
      )}
      {save.error !== null && <ErrorNotice error={save.error} testId="link-retarget-error" />}
      <div className="actions">
        <button
          type="button"
          className="btn btn-primary"
          disabled={!ready || save.isPending}
          onClick={() => {
            if (!targetComplete(choice.target)) return;
            if (sameTarget(targetBody(choice.target), targetBody(link.target))) {
              setSame(true);
              return;
            }
            setSame(false);
            save.mutate(choice.target);
          }}
          data-testid="link-retarget-go"
        >
          {t("links.retarget.go")}
        </button>
        <button type="button" className="btn" disabled={save.isPending} onClick={onClose}>
          {t("links.edit.cancel")}
        </button>
      </div>
    </section>
  );
}

function EditPanel({ orgId, link, onDone, onClose }: { orgId: string; link: SalesLink; onDone: (saved: SalesLink) => void; onClose: () => void }) {
  const { t } = useI18n();
  const ids = useId();
  const [label, setLabel] = useState(link.label);
  const [placement, setPlacement] = useState<Placement>(isPlacement(link.placement_kind) ? link.placement_kind : "other");
  const [utm, setUtm] = useState<Record<UtmKey, string>>(utmFields(link.utm));
  const [labelError, setLabelError] = useState<MessageKey | undefined>(undefined);
  const [utmError, setUtmError] = useState<{ key: UtmKey; error: MessageKey } | undefined>(undefined);
  const save = useMutation({
    mutationFn: (body: SalesLinkUpdateRequest) => updateSalesLink(orgId, link.id, body),
    onSuccess: onDone,
  });
  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const lab = parseLabel(label);
    const labels = parseUtm(utm);
    setLabelError(lab.ok ? undefined : lab.error);
    setUtmError(labels.ok ? undefined : { key: labels.key, error: labels.error });
    if (!lab.ok || !labels.ok) return;
    // The whole utm set every time: an absent key would keep, null would erase (AGENTS.md trap).
    save.mutate({ label: lab.value, placement_kind: placement, utm: labels.value });
  };
  return (
    <form className="panel" onSubmit={onSubmit} noValidate aria-labelledby="link-edit-title" data-testid="link-edit">
      <h2 id="link-edit-title">{t("links.edit.title")}</h2>
      <label htmlFor={`${ids}-label`}>{t("links.f.label")}</label>
      <input
        id={`${ids}-label`}
        type="text"
        autoComplete="off"
        maxLength={160}
        value={label}
        onChange={(e) => setLabel(e.target.value)}
        disabled={save.isPending}
        aria-invalid={labelError !== undefined}
        data-testid="link-edit-label"
      />
      {labelError !== undefined && (
        <p className="field-error" role="alert">
          {t(labelError)}
        </p>
      )}
      <fieldset>
        <legend>{t("links.card.placement")}</legend>
        {PLACEMENTS.map((p) => (
          <label key={p} className="check">
            <input type="radio" name={`${ids}-placement`} checked={placement === p} onChange={() => setPlacement(p)} disabled={save.isPending} data-testid={`link-edit-pl-${p}`} />
            <span>{t(`links.pl.${p}`)}</span>
          </label>
        ))}
      </fieldset>
      <details className="link-utm" open data-testid="link-edit-utm">
        <summary>{t("links.f.utm")}</summary>
        <p id={`${ids}-utm-hint`} className="muted">
          {t("links.f.utmEditHint")}
        </p>
        {(Object.keys(utm) as UtmKey[]).map((key) => (
          <div key={key}>
            <label htmlFor={`${ids}-${key}`} className="mono">
              {key}
            </label>
            <input
              id={`${ids}-${key}`}
              type="text"
              autoComplete="off"
              spellCheck={false}
              maxLength={64}
              value={utm[key]}
              onChange={(e) => setUtm((u) => ({ ...u, [key]: e.target.value }))}
              disabled={save.isPending}
              aria-describedby={`${ids}-utm-hint`}
              aria-invalid={utmError?.key === key}
              data-testid={`link-edit-${key}`}
            />
          </div>
        ))}
        {utmError !== undefined && (
          <p className="field-error" role="alert">
            {t(utmError.error, { key: utmError.key })}
          </p>
        )}
      </details>
      {save.error !== null && <ErrorNotice error={save.error} testId="link-edit-error" />}
      <div className="actions">
        <button type="submit" className="btn btn-primary" disabled={save.isPending} data-testid="link-edit-save">
          {t("links.edit.save")}
        </button>
        <button type="button" className="btn" disabled={save.isPending} onClick={onClose}>
          {t("links.edit.cancel")}
        </button>
      </div>
    </form>
  );
}

export function LinkScreen() {
  const { t, locale } = useI18n();
  const { orgId, orgName, access } = useOrg();
  const { linkId } = useParams({ from: "/o/$orgId/links/$linkId" });
  const search = useSearch({ from: "/o/$orgId/links/$linkId" });
  const queryClient = useQueryClient();
  const canRead = access.can("sales_link.read");
  const canManage = access.can("sales_link.manage");
  const [notice, setNotice] = useState<Notice | null>(search.created === true ? { key: "links.created" } : null);
  const [panel, setPanel] = useState<"none" | "retarget" | "edit">("none");
  const [archiving, setArchiving] = useState(false);

  const link = useQuery({ queryKey: ["salesLink", orgId, linkId], queryFn: () => fetchSalesLink(orgId, linkId), enabled: canRead });
  const events = useEventsList(orgId);
  const eventId = link.data?.target.event_id ?? null;
  // The date of a session link, for its label on the card.
  const sessions = useQuery({
    queryKey: ["sessions", orgId, eventId ?? ""],
    queryFn: () => listSessions(orgId, eventId ?? ""),
    enabled: canRead && link.data?.target.kind === "session" && eventId !== null,
  });

  const remember = (saved: SalesLink) => {
    queryClient.setQueryData(["salesLink", orgId, linkId], saved);
    void queryClient.invalidateQueries({ queryKey: ["salesLinks", orgId] });
  };
  const status = useMutation({
    mutationFn: (next: "active" | "archived") => updateSalesLink(orgId, linkId, { status: next }),
    onSuccess: (saved) => {
      remember(saved);
      setNotice({ key: saved.status === "archived" ? "links.archived.done" : "links.restored.done" });
    },
  });

  if (!canRead) return <NoLinksAccess />;
  const back = (
    <p>
      <Link to="/o/$orgId/links" params={{ orgId }} search={{}}>
        {t("links.back")}
      </Link>
    </p>
  );
  if (link.isLoading) return <Loading />;
  if (link.error !== null || link.data === undefined) {
    return (
      <>
        {back}
        <ErrorBox error={link.error} onRetry={() => void link.refetch()} />
      </>
    );
  }
  const item = link.data;
  const why = reasonKey(item);
  const archived = item.status === "archived";
  const sessionWhen = (() => {
    if (item.target.kind !== "session") return "";
    const s = sessions.data?.sessions.find((x) => x.id === item.target.session_id);
    return s === undefined ? "" : formatDateTime(s.start_at, null, locale);
  })();
  const where =
    item.target.kind === "session" && sessionWhen !== ""
      ? t("links.where.session", { name: events.data?.events.find((e) => e.id === eventId)?.name ?? "", when: sessionWhen })
      : whereText(item, events.data?.events ?? [], t);

  return (
    <article aria-labelledby="link-title">
      {back}
      <h1 id="link-title" data-testid="link-title">
        {item.label}
      </h1>
      <p className="card-meta">
        <LinkStatusBadge status={item.status} />
        <span data-testid="link-placement">{t(placementKey(item.placement_kind))}</span>
      </p>
      {notice !== null && (
        <p className="notice notice-ok" role="status" data-testid="link-notice">
          {t(notice.key, notice.params)}
        </p>
      )}

      <SalesLinkQr orgId={orgId} link={item} />
      <ShortAddress value={item.short_url} />

      <dl className="facts-grid" data-testid="link-facts">
        <dt>{t("links.card.target")}</dt>
        <dd data-testid="link-where">{where}</dd>
        <dt>{t("links.card.dest")}</dt>
        <dd data-testid="link-dest">
          {item.destination_url !== "" ? (
            <a href={item.destination_url} target="_blank" rel="noopener noreferrer">
              {item.destination_url}
            </a>
          ) : (
            t("links.card.noDest")
          )}
        </dd>
        <dt>{t("links.card.scans")}</dt>
        <dd data-testid="link-scans">{formatNumber(item.scan_count, locale)}</dd>
        <dt>{t("links.card.orders")}</dt>
        <dd data-testid="link-orders">{formatNumber(item.orders_count, locale)}</dd>
        <dt>{t("links.card.lastScan")}</dt>
        <dd>{item.last_scanned_at !== null && item.last_scanned_at !== undefined ? formatDateTime(item.last_scanned_at, null, locale) : t("links.card.never")}</dd>
        <dt>{t("links.card.created")}</dt>
        <dd>{formatDateTime(item.created_at, null, locale)}</dd>
      </dl>
      {why !== null && (
        <p className={`notice${archived ? "" : " notice-warn"}`} role="status" data-testid="link-why">
          {t(why)}
        </p>
      )}

      <div className="actions">
        {canManage && !archived && (
          <button type="button" className={`btn${retargetHelps(item) ? " btn-primary" : ""}`} aria-expanded={panel === "retarget"} onClick={() => setPanel(panel === "retarget" ? "none" : "retarget")} data-testid="link-retarget-open">
            {t("links.retarget")}
          </button>
        )}
        {canManage && (
          <button type="button" className="btn" aria-expanded={panel === "edit"} onClick={() => setPanel(panel === "edit" ? "none" : "edit")} data-testid="link-edit-open">
            {t("links.edit")}
          </button>
        )}
        {canManage && !archived && (
          <button type="button" className="btn" onClick={() => setArchiving(true)} data-testid="link-archive">
            {t("links.archive")}
          </button>
        )}
        {canManage && archived && (
          <button type="button" className="btn" disabled={status.isPending} onClick={() => status.mutate("active")} data-testid="link-restore">
            {t("links.restore")}
          </button>
        )}
        {eventId !== null && (
          <Link to="/o/$orgId/links" params={{ orgId }} search={{ event: eventId }} className="btn" data-testid="link-event-list">
            {t("links.card.toEventLinks")}
          </Link>
        )}
      </div>
      {archived && canManage && <p className="muted">{t("links.retarget.archived")}</p>}
      {status.error !== null && <ErrorNotice error={status.error} testId="link-status-error" />}

      {panel === "retarget" && canManage && (
        <RetargetPanel
          key={item.updated_at}
          orgId={orgId}
          orgName={orgName}
          link={item}
          canSales={access.can("order.read")}
          onDone={(saved, n) => {
            remember(saved);
            setNotice(n);
            setPanel("none");
          }}
          onClose={() => setPanel("none")}
        />
      )}
      {panel === "edit" && canManage && (
        <EditPanel
          key={item.updated_at}
          orgId={orgId}
          link={item}
          onDone={(saved) => {
            remember(saved);
            setNotice({ key: "links.edit.done" });
            setPanel("none");
          }}
          onClose={() => setPanel("none")}
        />
      )}

      <SalesLinkStatsBlock orgId={orgId} linkId={item.id} />

      {archiving && (
        <ConfirmPressDialog
          title={t("links.archive.title")}
          subject={item.label}
          explain={t("links.archive.explain")}
          confirmLabel={t("links.archive.go")}
          testId="link-archive-confirm"
          onConfirm={async () => {
            await status.mutateAsync("archived");
            setArchiving(false);
          }}
          onCancel={() => setArchiving(false)}
        />
      )}
    </article>
  );
}
