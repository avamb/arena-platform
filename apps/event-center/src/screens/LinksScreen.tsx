/**
 * The list of sales links (spec 43 section 5.3, QL-12): the bot's
 * `sales_link_list.go` on one page. Filters active / archive / all and one
 * event, newest first, paged by the API's cursor (a stack of cursors gives
 * the way back). The list shows scans and orders; revenue lives on the card
 * (the list answer of the API carries no money).
 */
import { useState } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { listSalesLinks } from "@/lib/api/endpoints";
import type { EventItem, SalesLink } from "@/lib/api/types";
import { formatNumber } from "@/lib/format";
import { useI18n } from "@/lib/i18n";
import { linksFilter, placementKey, type LinksFilter } from "@/lib/salesLinks";
import { Badge, Empty, ErrorBox, Loading } from "@/components/ui";
import { NoLinksAccess } from "@/screens/LinksNoAccess";
import { useOrg } from "@/screens/OrgLayout";
import { useEventsList } from "@/screens/EventsScreen";

export const LINKS_PAGE_SIZE = 20;

/** The target of a link in one line, with the event's name when the list knows it. */
export function whereText(link: SalesLink, events: readonly EventItem[], t: ReturnType<typeof useI18n>["t"]): string {
  const name = events.find((e) => e.id === link.target.event_id)?.name ?? "";
  switch (link.target.kind) {
    case "event":
      return t("links.where.event", { name });
    case "session":
      return t("links.where.sessionOnly", { name });
    case "promoter_page":
      return t("links.where.promoter");
    case "url":
      return t("links.where.url", { url: link.target.url ?? "" });
    default:
      return t("links.where.org");
  }
}

export function LinkStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return status === "archived" ? <Badge tone="neutral">{t("links.status.archived")}</Badge> : <Badge tone="ok">{t("links.status.active")}</Badge>;
}

function LinkRow({ orgId, link, events }: { orgId: string; link: SalesLink; events: readonly EventItem[] }) {
  const { t, locale } = useI18n();
  return (
    <li className="card" data-testid="link-row" data-link-code={link.code}>
      <Link to="/o/$orgId/links/$linkId" params={{ orgId, linkId: link.id }} search={{}} className="card-link">
        <span className="card-title" data-testid="link-label">
          {link.label}
        </span>
        <span className="card-meta">
          <LinkStatusBadge status={link.status} />
          <span>{t(placementKey(link.placement_kind))}</span>
        </span>
        <span className="muted" data-testid="link-where">
          {whereText(link, events, t)}
        </span>
        {link.destination_url !== "" && (
          <span className="muted link-dest" data-testid="link-dest">
            {t("links.card.dest")} {link.destination_url}
          </span>
        )}
        <span className="muted" data-testid="link-counts">
          {t("links.row.scans", { count: formatNumber(link.scan_count, locale) })} · {t("links.row.orders", { count: formatNumber(link.orders_count, locale) })}
        </span>
      </Link>
    </li>
  );
}

export function LinksScreen() {
  const { t } = useI18n();
  const { orgId, access } = useOrg();
  const navigate = useNavigate();
  const search = useSearch({ from: "/o/$orgId/links" });
  const filter = linksFilter(search);
  const canRead = access.can("sales_link.read");
  // The cursors of the pages before this one, so "previous page" asks the API again with the right one.
  const [stack, setStack] = useState<string[]>([]);
  const cursor = stack.length > 0 ? stack[stack.length - 1] : undefined;
  const events = useEventsList(orgId);
  const page = useQuery({
    queryKey: ["salesLinks", orgId, filter, search.event ?? "", cursor ?? ""],
    queryFn: () => listSalesLinks(orgId, { status: filter, eventId: search.event, cursor, limit: LINKS_PAGE_SIZE }),
    enabled: canRead,
  });

  if (!canRead) return <NoLinksAccess />;
  const go = (next: { f?: "archived" | "all"; event?: string }) => {
    setStack([]);
    void navigate({ to: "/o/$orgId/links", params: { orgId }, search: next });
  };
  const items = page.data?.items ?? [];
  const filters: LinksFilter[] = ["active", "archived", "all"];
  const emptyKey = search.event !== undefined ? "links.emptyFiltered" : filter === "archived" ? "links.emptyArchived" : "links.empty";

  return (
    <section aria-labelledby="links-title">
      <h1 id="links-title">{t("links.title")}</h1>
      <p className="muted">{t("links.intro")}</p>
      {access.can("sales_link.manage") && (
        <div className="actions">
          <Link to="/o/$orgId/links/new" params={{ orgId }} search={{}} className="btn btn-primary" data-testid="link-new">
            {t("links.new")}
          </Link>
        </div>
      )}
      <nav className="tabs" aria-label={t("links.f.filter")} data-testid="links-filters">
        {filters.map((f) => (
          <button
            key={f}
            type="button"
            className={`tab${filter === f ? " active" : ""}`}
            aria-pressed={filter === f}
            onClick={() => go({ ...(f === "active" ? {} : { f }), ...(search.event !== undefined ? { event: search.event } : {}) })}
            data-testid={`links-filter-${f}`}
          >
            {t(`links.f.${f}`)}
          </button>
        ))}
      </nav>
      <label htmlFor="links-event-filter">{t("links.f.event")}</label>
      <select
        id="links-event-filter"
        value={search.event ?? ""}
        onChange={(e) => go({ ...(search.f !== undefined ? { f: search.f } : {}), ...(e.target.value !== "" ? { event: e.target.value } : {}) })}
        data-testid="links-event-filter"
      >
        <option value="">{t("links.f.allEvents")}</option>
        {(events.data?.events ?? []).map((e) => (
          <option key={e.id} value={e.id}>
            {e.name}
          </option>
        ))}
      </select>

      {page.isLoading && <Loading />}
      {page.error !== null && <ErrorBox error={page.error} onRetry={() => void page.refetch()} />}
      {page.data !== undefined && items.length === 0 && <Empty>{t(emptyKey)}</Empty>}
      {items.length > 0 && <p className="muted">{t("links.row.revenueHint")}</p>}
      <ul className="cards" data-testid="links-list">
        {items.map((l) => (
          <LinkRow key={l.id} orgId={orgId} link={l} events={events.data?.events ?? []} />
        ))}
      </ul>
      {(stack.length > 0 || (page.data !== undefined && page.data.next_cursor !== "")) && (
        <nav className="pager" aria-label={t("links.page", { page: stack.length + 1 })}>
          <button type="button" className="btn" disabled={stack.length === 0} onClick={() => setStack((s) => s.slice(0, -1))} data-testid="links-prev">
            {t("events.prev")}
          </button>
          <span aria-live="polite" data-testid="links-page">
            {t("links.page", { page: stack.length + 1 })}
          </span>
          <button
            type="button"
            className="btn"
            disabled={page.data === undefined || page.data.next_cursor === ""}
            onClick={() => {
              const next = page.data?.next_cursor ?? "";
              if (next !== "") setStack((s) => [...s, next]);
            }}
            data-testid="links-next"
          >
            {t("events.nextPage")}
          </button>
        </nav>
      )}
    </section>
  );
}
