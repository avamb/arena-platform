/**
 * The door of the shop area (spec 61, section 5). Every shop screen sits behind it (`shopGated`).
 *
 * - The organization has no shop module (and no orders left from one): the screen "The shop is not connected".
 * - The module ended but orders remain (`orders_only`): a banner, only the order screens open, every other
 *   shop address goes to the orders.
 * - Otherwise the shop as before.
 *
 * The mode comes from the person's memberships in `GET /v1/me` (`modules`, `shop_mode`), see `shopModeFor`.
 */
import { createContext, useContext, type ComponentType, type ReactNode } from "react";
import { Navigate, useRouterState } from "@tanstack/react-router";
import { ErrorBox, Loading } from "@/components/ui";
import { useMe } from "@/lib/auth";
import { useI18n } from "@/lib/i18n";
import { shopModeFor, shopRedirect, type ShopMode } from "@/lib/shopModule";
import { useOrg } from "@/screens/OrgLayout";

const ShopModeCtx = createContext<ShopMode>("full");

/** How much of the shop the organization has, for the screens inside the gate ("full" outside it). */
export function useShopMode(): ShopMode {
  return useContext(ShopModeCtx);
}

/** "The shop is not connected": what a person sees at a shop address of an organization without the module. */
export function ShopNotConnected() {
  const { t } = useI18n();
  return (
    <section aria-labelledby="shop-title">
      <h1 id="shop-title">{t("shop.title")}</h1>
      <p className="notice" role="status" data-testid="shop-not-connected">
        {t("shop.notConnected.body")}
      </p>
    </section>
  );
}

/** The banner of the orders-only mode. */
export function ShopOrdersOnlyBanner() {
  const { t } = useI18n();
  return (
    <p className="notice notice-warn" role="status" data-testid="shop-orders-only">
      {t("shop.ordersOnly.banner")}
    </p>
  );
}

/** The decision of the gate, apart from the queries: what to draw for a mode at an address. */
export function ShopGateView({ mode, pathname, orgId, children }: { mode: ShopMode; pathname: string; orgId: string; children?: ReactNode }) {
  if (mode === "none") return <ShopNotConnected />;
  if (shopRedirect(mode, pathname) === "orders") {
    return <Navigate to="/o/$orgId/shop/orders" params={{ orgId }} search={{}} replace />;
  }
  return (
    <ShopModeCtx.Provider value={mode}>
      {mode === "orders_only" && <ShopOrdersOnlyBanner />}
      {children}
    </ShopModeCtx.Provider>
  );
}

export function ShopGate({ children }: { children?: ReactNode }) {
  const { orgId } = useOrg();
  const me = useMe();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  if (me.data === undefined) {
    if (me.error !== null && me.error !== undefined) return <ErrorBox error={me.error} onRetry={() => void me.refetch()} />;
    return <Loading />;
  }
  return (
    <ShopGateView mode={shopModeFor(me.data.organization_memberships, orgId)} pathname={pathname} orgId={orgId}>
      {children}
    </ShopGateView>
  );
}

/** A shop screen behind the gate. */
export function shopGated(Screen: ComponentType): () => JSX.Element {
  function Gated() {
    return (
      <ShopGate>
        <Screen />
      </ShopGate>
    );
  }
  Gated.displayName = `ShopGated(${Screen.displayName ?? Screen.name})`;
  return Gated;
}
