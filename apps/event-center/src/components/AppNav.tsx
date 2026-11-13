/**
 * The shell of the event center (spec 62 §12.1, owner decision 2026-11-12):
 * a bar of five items — Events · Orders · Promotion · Entrance · Menu — at
 * the bottom of a phone screen and under the header on a wide one.
 * «Promotion» opens a sheet with the promotion items alone. «Menu» opens the
 * sheet with every section as a headed group, in the order of the Telegram
 * bot's menu: New event, My events, Orders and refunds, Promotion, Entrance
 * to the event, Shop, Settings, Help. What a role cannot use is not shown
 * (`navGroups`).
 */
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { fetchRefundQueue } from "@/lib/api/endpoints";
import type { OrgAccess } from "@/lib/auth";
import { badgeText, queueCount } from "@/lib/refundQueue";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/en";
import { navGroups, promotionItems, type NavGroup } from "@/lib/navMenu";
import { shopNavTarget, type ShopMode } from "@/lib/shopModule";
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

export type NavSection = "events" | "orders" | "promotion" | "entrance" | "menu";

/**
 * Which of the five items a location belongs to. Summary is under Events and
 * Refunds under Orders; promo codes, free tickets, links and the mailings are
 * Promotion; the entrance page is Entrance; everything else is under Menu.
 */
export function navSectionOf(pathname: string, _search?: unknown): NavSection {
  const rest = pathname.replace(/^\/o\/[^/]+/, "");
  const first = rest.split("/")[1] ?? "";
  if (first === "") return "events";
  if (first === "events") return rest.startsWith("/events/new") ? "menu" : "events";
  if (first === "summary") return "events";
  // Orders, the refund queue and the "refunds" tab of the orders list are all one item.
  if (first === "refunds" || first === "orders") return "orders";
  if (first === "promo" || first === "invitations" || first === "links" || first === "growth") return "promotion";
  if (first === "entrance") return "entrance";
  return "menu";
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
    promotion: (
      <>
        <path d="M3 11v3l12 5V6z" />
        <path d="M15 9a3 3 0 010 6M6 15l1.5 5" />
      </>
    ),
    entrance: (
      <>
        <path d="M5 21V4a1 1 0 011-1h9a1 1 0 011 1v17M3 21h18" />
        <circle cx="12.5" cy="12" r="0.8" />
      </>
    ),
    menu: (
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
 * The counter on «Orders» (EC-V5): failed or stuck refunds plus undelivered
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

type SheetMode = "promotion" | "menu";

/** The five-item bar. */
export function TabBar({ orgId, access, shopMode, orgCount, onLogout }: NavProps) {
  const { t } = useI18n();
  const location = useRouterState({ select: (s) => s.location });
  const section = navSectionOf(location.pathname, location.search);
  const [open, setOpen] = useState<SheetMode | null>(null);
  const canOrders = access.can("order.read");
  const canRefunds = access.can("refund.read");
  const hasPromotion = promotionItems(access.can).length > 0;
  const cur = (s: NavSection) => (section === s && open === null ? { "aria-current": "page" as const, className: "tab-item active" } : { className: "tab-item" });
  const toggle = (mode: SheetMode) => setOpen((v) => (v === mode ? null : mode));

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
            {canRefunds && <RefundsBadge orgId={orgId} />}
          </Link>
        )}
        {!canOrders && canRefunds && (
          <Link to="/o/$orgId/refunds" params={{ orgId }} {...cur("orders")} data-testid="nav-refunds">
            <Icon name="orders" />
            <span>{t("nav.refunds")}</span>
            <RefundsBadge orgId={orgId} />
          </Link>
        )}
        {hasPromotion && (
          <button
            type="button"
            className={section === "promotion" || open === "promotion" ? "tab-item active" : "tab-item"}
            aria-expanded={open === "promotion"}
            aria-controls="promotion-sheet"
            onClick={() => toggle("promotion")}
            data-testid="nav-promotion"
          >
            <Icon name="promotion" />
            <span>{t("nav.promotion")}</span>
          </button>
        )}
        <Link to="/o/$orgId/entrance" params={{ orgId }} {...cur("entrance")} data-testid="nav-entrance">
          <Icon name="entrance" />
          <span>{t("nav.entrance")}</span>
        </Link>
        <button
          type="button"
          className={section === "menu" || open === "menu" ? "tab-item active" : "tab-item"}
          aria-expanded={open === "menu"}
          aria-controls="more-sheet"
          onClick={() => toggle("menu")}
          data-testid="nav-more"
        >
          <Icon name="menu" />
          <span>{t("nav.more")}</span>
        </button>
      </nav>
      {open !== null && (
        <NavSheet
          mode={open}
          orgId={orgId}
          access={access}
          shopMode={shopMode}
          orgCount={orgCount}
          onLogout={onLogout}
          onClose={() => setOpen(null)}
        />
      )}
    </>
  );
}

/**
 * The sheet over the page: the whole Menu (every group, then the theme, the
 * language, the analytics choice and Sign out) or the Promotion group alone.
 */
function NavSheet({ mode, orgId, access, shopMode, orgCount, onLogout, onClose }: NavProps & { mode: SheetMode; onClose: () => void }) {
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

  const all = navGroups(access, shopMode, orgCount);
  const groups: NavGroup[] = mode === "menu" ? all : all.filter((g) => g.id === "promotion");
  const menu = mode === "menu";
  const title: MessageKey = menu ? "nav.moreTitle" : "nav.section.promotion";
  const item = { className: "more-item", onClick: onClose, activeProps: { "aria-current": "page" as const, className: "active" } };

  const link = (id: string, label: string): ReactNode => {
    switch (id) {
      case "nav-new-event":
        return (
          <Link to="/o/$orgId/events/new" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "menu-events":
        return (
          <Link to="/o/$orgId/events" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-summary":
        return (
          <Link to="/o/$orgId/summary" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "menu-orders":
        return (
          <Link to="/o/$orgId/orders" params={{ orgId }} search={{}} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "menu-refunds":
        return (
          <Link to="/o/$orgId/refunds" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-promo":
        return (
          <Link to="/o/$orgId/promo" params={{ orgId }} search={{}} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-invitations":
        return (
          <Link to="/o/$orgId/invitations" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-links":
        return (
          <Link to="/o/$orgId/links" params={{ orgId }} search={{}} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-growth":
        return (
          <Link to="/o/$orgId/growth" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-scanner":
        return (
          <Link to="/o/$orgId/entrance" params={{ orgId }} activeOptions={{ includeHash: true }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-guest-list":
        return (
          <Link to="/o/$orgId/entrance" params={{ orgId }} hash="guest-list" activeOptions={{ includeHash: true }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-shop":
        return shopNavTarget(shopMode) === "orders" ? (
          <Link to="/o/$orgId/shop/orders" params={{ orgId }} search={{}} {...item} data-testid={id}>
            {label}
          </Link>
        ) : (
          <Link to="/o/$orgId/shop" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-org-data":
        return (
          <Link to="/o/$orgId/org-data" params={{ orgId }} activeOptions={{ includeHash: true }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-payments":
        return (
          <Link to="/o/$orgId/org-data" params={{ orgId }} hash="payments" activeOptions={{ includeHash: true }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-team":
        return (
          <Link to="/o/$orgId/team" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-promoters":
        return (
          <Link to="/o/$orgId/promoters" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-venues":
        return (
          <Link to="/o/$orgId/venues" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-notifications":
        return (
          <Link to="/o/$orgId/notifications" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-billing":
        return (
          <Link to="/o/$orgId/billing" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-switch-org":
        return (
          <Link to="/orgs" className="more-item" onClick={onClose} data-testid={id}>
            {label}
          </Link>
        );
      case "nav-help":
        return (
          <Link to="/o/$orgId/help" params={{ orgId }} {...item} data-testid={id}>
            {label}
          </Link>
        );
      default:
        return null;
    }
  };

  return (
    <div className="more-backdrop" onClick={onClose} data-testid="more-backdrop">
      <div
        id={menu ? "more-sheet" : "promotion-sheet"}
        ref={sheet}
        className="more-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="more-title"
        onClick={(e) => e.stopPropagation()}
        data-testid={menu ? "more-sheet" : "promotion-sheet"}
      >
        <div className="more-head">
          <h2 id="more-title">{t(title)}</h2>
          {menu && (
            <Link to="/o/$orgId/help" params={{ orgId }} className="btn btn-link" onClick={onClose} data-testid="screen-help-menu">
              {t("screenHelp.link")}
            </Link>
          )}
          <button type="button" className="btn btn-link" onClick={onClose} data-testid={menu ? "more-close" : "promotion-close"}>
            {t("nav.close")}
          </button>
        </div>
        <nav aria-label={t(title)}>
          {groups.map((g) => (
            <section key={g.id} className="more-group" aria-labelledby={menu ? `more-group-${g.id}` : undefined} data-testid={`more-group-${g.id}`}>
              {menu && (
                <h3 id={`more-group-${g.id}`} className="more-group-title">
                  {t(g.titleKey)}
                </h3>
              )}
              <ul className="more-list">
                {g.items.map((e) => (
                  <li key={e.id}>{link(e.id, t(e.labelKey))}</li>
                ))}
              </ul>
            </section>
          ))}
        </nav>
        {menu && (
          <div className="more-tools">
            <ThemeSwitch />
            <LanguageSelect />
            <AnalyticsOptOut />
            <button type="button" className="btn more-logout" onClick={onLogout} data-testid="logout">
              {t("nav.logout")}
            </button>
          </div>
        )}
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

/** The breakpoint where the tab bar moves under the header (styles.css, min-width: 900px). */
export const WIDE_QUERY = "(min-width: 900px)";

function isWide(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(WIDE_QUERY).matches;
}

/** True on a wide screen, and follows a resize. */
function useWideScreen(): boolean {
  const [wide, setWide] = useState(isWide);
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(WIDE_QUERY);
    const on = () => setWide(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return wide;
}

/**
 * "Add to home screen": shown once on a phone, closed for good with one press.
 * A wide screen is a computer, where the note makes no sense: it never shows.
 */
export function A2hsNote() {
  const { t } = useI18n();
  const wide = useWideScreen();
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

  if (!shown || wide) return null;
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
