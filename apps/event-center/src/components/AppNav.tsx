/**
 * The shell of the event center (spec 62 §12.1, owner decision 2026-11-06):
 * a bar of five items — Events · Orders · Refunds · Summary · More — at the
 * bottom of a phone screen and under the header on a wide one. "More" opens
 * every other section, Promo codes first, then the theme switch, the
 * language and Sign out.
 */
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { fetchRefundQueue } from "@/lib/api/endpoints";
import type { OrgAccess } from "@/lib/auth";
import { badgeText, queueCount } from "@/lib/refundQueue";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { shopNavTarget, showShopNav, type ShopMode } from "@/lib/shopModule";
import {
  THEME_MODES,
  a2hsDismissed,
  dismissA2hs,
  runsStandalone,
  setThemeMode,
  storedThemeMode,
  type ThemeMode,
} from "@/lib/theme";
import { LanguageSelect } from "@/screens/LanguageSelect";
import { AnalyticsOptOut } from "@/lib/analytics/AnalyticsOptOut";

export type NavSection = "events" | "orders" | "refunds" | "summary" | "more";

/** Which of the five items a location belongs to. Everything not in the first four is under "More". */
export function navSectionOf(pathname: string, search: unknown): NavSection {
  const rest = pathname.replace(/^\/o\/[^/]+/, "");
  if (rest === "" || rest === "/" || rest.startsWith("/events")) {
    return rest.startsWith("/events/new") ? "more" : "events";
  }
  if (rest.startsWith("/refunds")) return "refunds";
  if (rest.startsWith("/orders")) {
    const tab = typeof search === "object" && search !== null ? (search as { tab?: unknown }).tab : undefined;
    return tab === "refunds" ? "refunds" : "orders";
  }
  if (rest.startsWith("/summary")) return "summary";
  return "more";
}

/** The header "+ Event" button belongs to the Events list only: on every other screen it is noise. */
export function showNewEventButton(pathname: string): boolean {
  const rest = pathname.replace(/^\/o\/[^/]+/, "").replace(/\/+$/, "");
  return rest === "" || rest === "/events";
}

function Icon({ name }: { name: NavSection }) {
  const paths: Record<NavSection, ReactNode> = {
    events: (
      <>
        <rect x="3" y="5" width="18" height="16" rx="2" />
        <path d="M3 10h18M8 3v4M16 3v4" />
      </>
    ),
    orders: (
      <>
        <path d="M6 3h12v18l-3-2-3 2-3-2-3 2z" />
        <path d="M9 8h6M9 12h6" />
      </>
    ),
    refunds: (
      <>
        <path d="M9 14L4 9l5-5" />
        <path d="M4 9h10a6 6 0 010 12h-3" />
      </>
    ),
    summary: (
      <>
        <path d="M4 20V10M10 20V4M16 20v-7M22 20H2" />
      </>
    ),
    more: (
      <>
        <circle cx="5" cy="12" r="1.5" />
        <circle cx="12" cy="12" r="1.5" />
        <circle cx="19" cy="12" r="1.5" />
      </>
    ),
  };
  return (
    <svg className="tab-icon" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {paths[name]}
    </svg>
  );
}

/**
 * The counter on «Refunds» (EC-V5): failed or stuck refunds plus undelivered
 * letters, the same `count` the queue screen shows. It rides the queue's own
 * query, so opening the screen costs no second request, and it asks again
 * every minute. A failed read shows no number rather than a wrong one.
 */
export function RefundsBadge({ orgId }: { orgId: string }) {
  const { t } = useI18n();
  const queue = useQuery({
    queryKey: ["refund-queue", orgId],
    queryFn: () => fetchRefundQueue(orgId),
    refetchInterval: 60_000,
  });
  const text = badgeText(queueCount(queue.data));
  if (text === null) return null;
  return (
    <>
      <span className="tab-badge" data-testid="nav-refunds-badge" aria-hidden="true">
        {text}
      </span>
      <span className="visually-hidden">{t("nav.refunds.badge", { count: queueCount(queue.data) })}</span>
    </>
  );
}

interface NavProps {
  orgId: string;
  access: OrgAccess;
  shopMode: ShopMode;
  orgCount: number;
  onLogout: () => void;
}

/** The five-item bar. */
export function TabBar({ orgId, access, shopMode, orgCount, onLogout }: NavProps) {
  const { t } = useI18n();
  const location = useRouterState({ select: (s) => s.location });
  const section = navSectionOf(location.pathname, location.search);
  const [moreOpen, setMoreOpen] = useState(false);
  const canOrders = access.can("order.read");
  const canRefunds = access.can("refund.read");
  const cur = (s: NavSection) => (section === s && !moreOpen ? { "aria-current": "page" as const, className: "tab-item active" } : { className: "tab-item" });

  return (
    <>
      <nav aria-label={t("nav.main")} className="tabbar" data-testid="tabbar">
        <Link to="/o/$orgId/events" params={{ orgId }} {...cur("events")} data-testid="nav-events">
          <Icon name="events" />
          <span>{t("nav.events")}</span>
        </Link>
        {canOrders && (
          <Link to="/o/$orgId/orders" params={{ orgId }} search={{}} {...cur("orders")} data-testid="nav-orders">
            <Icon name="orders" />
            <span>{t("nav.orders")}</span>
          </Link>
        )}
        {canRefunds && (
          <Link to="/o/$orgId/refunds" params={{ orgId }} {...cur("refunds")} data-testid="nav-refunds">
            <Icon name="refunds" />
            <span>{t("nav.refunds")}</span>
            <RefundsBadge orgId={orgId} />
          </Link>
        )}
        <Link to="/o/$orgId/summary" params={{ orgId }} {...cur("summary")} data-testid="nav-summary">
          <Icon name="summary" />
          <span>{t("nav.summary")}</span>
        </Link>
        <button
          type="button"
          className={section === "more" || moreOpen ? "tab-item active" : "tab-item"}
          aria-expanded={moreOpen}
          aria-controls="more-sheet"
          onClick={() => setMoreOpen((v) => !v)}
          data-testid="nav-more"
        >
          <Icon name="more" />
          <span>{t("nav.more")}</span>
        </button>
      </nav>
      {moreOpen && (
        <MoreSheet
          orgId={orgId}
          access={access}
          shopMode={shopMode}
          orgCount={orgCount}
          onLogout={onLogout}
          onClose={() => setMoreOpen(false)}
        />
      )}
    </>
  );
}

/** The sections behind "More", in the order of spec 62 §12.1. */
function MoreSheet({ orgId, access, shopMode, orgCount, onLogout, onClose }: NavProps & { onClose: () => void }) {
  const { t } = useI18n();
  const sheet = useRef<HTMLDivElement>(null);

  useEffect(() => {
    sheet.current?.querySelector<HTMLElement>("a, button")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const item = { className: "more-item", onClick: onClose, activeProps: { "aria-current": "page" as const, className: "active" } };

  return (
    <div className="more-backdrop" onClick={onClose} data-testid="more-backdrop">
      <div
        id="more-sheet"
        ref={sheet}
        className="more-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="more-title"
        onClick={(e) => e.stopPropagation()}
        data-testid="more-sheet"
      >
        <div className="more-head">
          <h2 id="more-title">{t("nav.moreTitle")}</h2>
          <button type="button" className="btn btn-link" onClick={onClose} data-testid="more-close">
            {t("nav.close")}
          </button>
        </div>
        <nav aria-label={t("nav.moreTitle")}>
          <ul className="more-list">
            {access.can("promo.read") && (
              <li>
                <Link to="/o/$orgId/promo" params={{ orgId }} search={{}} {...item} data-testid="nav-promo">
                  {t("nav.promo")}
                </Link>
              </li>
            )}
            {access.can("complimentary.read") && (
              <li>
                <Link to="/o/$orgId/invitations" params={{ orgId }} {...item} data-testid="nav-invitations">
                  {t("nav.invitations")}
                </Link>
              </li>
            )}
            {access.can("promoter.read") && (
              <li>
                <Link to="/o/$orgId/promoters" params={{ orgId }} {...item} data-testid="nav-promoters">
                  {t("nav.promoters")}
                </Link>
              </li>
            )}
            {access.can("sales_link.read") && (
              <li>
                <Link to="/o/$orgId/links" params={{ orgId }} search={{}} {...item} data-testid="nav-links">
                  {t("nav.links")}
                </Link>
              </li>
            )}
            {(access.can("promo.read") || access.can("order.read")) && (
              <li>
                <Link to="/o/$orgId/growth" params={{ orgId }} {...item} data-testid="nav-growth">
                  {t("nav.growth")}
                </Link>
              </li>
            )}
            <li>
              <Link to="/o/$orgId/notifications" params={{ orgId }} {...item} data-testid="nav-notifications">
                {t("nav.notifications")}
              </Link>
            </li>
            {access.can("membership.read") && (
              <li>
                <Link to="/o/$orgId/team" params={{ orgId }} {...item} data-testid="nav-team">
                  {t("nav.team")}
                </Link>
              </li>
            )}
            {showShopNav(shopMode, access.can("shop.read")) && (
              <li>
                {shopNavTarget(shopMode) === "orders" ? (
                  <Link to="/o/$orgId/shop/orders" params={{ orgId }} search={{}} {...item} data-testid="nav-shop">
                    {t("nav.shop")}
                  </Link>
                ) : (
                  <Link to="/o/$orgId/shop" params={{ orgId }} {...item} data-testid="nav-shop">
                    {t("nav.shop")}
                  </Link>
                )}
              </li>
            )}
            {access.can("billing.read") && (
              <li>
                <Link to="/o/$orgId/billing" params={{ orgId }} {...item} data-testid="nav-billing">
                  {t("nav.billing")}
                </Link>
              </li>
            )}
            {access.can("org.update") && (
              <li>
                <Link to="/o/$orgId/org-data" params={{ orgId }} {...item} data-testid="nav-org-data">
                  {t("nav.orgData")}
                </Link>
              </li>
            )}
            {orgCount > 1 && (
              <li>
                <Link to="/orgs" className="more-item" onClick={onClose} data-testid="nav-switch-org">
                  {t("nav.switchOrg")}
                </Link>
              </li>
            )}
          </ul>
        </nav>
        <div className="more-tools">
          <ThemeSwitch />
          <LanguageSelect />
          <AnalyticsOptOut />
          <button type="button" className="btn more-logout" onClick={onLogout} data-testid="logout">
            {t("nav.logout")}
          </button>
        </div>
      </div>
    </div>
  );
}

const THEME_KEYS: Record<ThemeMode, MessageKey> = { system: "theme.system", light: "theme.light", dark: "theme.dark" };

/** Light / dark / as on the device; the choice is kept in this browser. */
export function ThemeSwitch() {
  const { t } = useI18n();
  const [mode, setMode] = useState<ThemeMode>(() => storedThemeMode());
  return (
    <fieldset className="theme-switch" data-testid="theme-switch">
      <legend>{t("theme.label")}</legend>
      <div className="theme-options">
        {THEME_MODES.map((m) => (
          <button
            key={m}
            type="button"
            className={mode === m ? "btn btn-primary" : "btn"}
            aria-pressed={mode === m}
            onClick={() => {
              setThemeMode(m);
              setMode(m);
            }}
            data-testid={`theme-${m}`}
          >
            {t(THEME_KEYS[m])}
          </button>
        ))}
      </div>
    </fieldset>
  );
}

interface InstallPromptEvent extends Event {
  prompt: () => Promise<void>;
}

/** "Add to home screen": shown once, closed for good with one press. */
export function A2hsNote() {
  const { t } = useI18n();
  const [shown, setShown] = useState(() => !a2hsDismissed() && !runsStandalone());
  const [install, setInstall] = useState<InstallPromptEvent | null>(null);

  useEffect(() => {
    if (!shown) return;
    const onPrompt = (e: Event) => {
      e.preventDefault();
      setInstall(e as InstallPromptEvent);
    };
    window.addEventListener("beforeinstallprompt", onPrompt);
    return () => window.removeEventListener("beforeinstallprompt", onPrompt);
  }, [shown]);

  if (!shown) return null;
  const close = () => {
    dismissA2hs();
    setShown(false);
  };
  return (
    <aside className="a2hs notice" aria-label={t("a2hs.text")} data-testid="a2hs">
      <p className="a2hs-text">
        {t("a2hs.text")} {install === null && <span className="muted">{t("a2hs.hint")}</span>}
      </p>
      <div className="a2hs-actions">
        {install !== null && (
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => {
              void install.prompt().finally(close);
            }}
            data-testid="a2hs-install"
          >
            {t("a2hs.install")}
          </button>
        )}
        <button type="button" className="btn" onClick={close} data-testid="a2hs-dismiss">
          {t("a2hs.dismiss")}
        </button>
      </div>
    </aside>
  );
}
