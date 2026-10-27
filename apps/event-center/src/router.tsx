/**
 * The route tree, assembled in code like admin-web's. Every organization
 * screen lives under /o/$orgId, so a link names its organization and can be
 * sent to a colleague.
 */
import { createRootRoute, createRoute, createRouter, redirect } from "@tanstack/react-router";
import { RootLayout } from "@/screens/RootLayout";
import { LoginScreen } from "@/screens/LoginScreen";
import { LinkLoginScreen } from "@/screens/LinkLoginScreen";
import { TgEditScreen } from "@/screens/TgEditScreen";
import { TgEnterScreen } from "@/screens/TgEnterScreen";
import { parseTgEnterSearch } from "@/lib/tgEnter";
import { ForgotPasswordScreen } from "@/screens/ForgotPasswordScreen";
import { SetPasswordScreen } from "@/screens/SetPasswordScreen";
import { OrgsScreen } from "@/screens/OrgsScreen";
import { OrgLayout } from "@/screens/OrgLayout";
import { EventsScreen } from "@/screens/EventsScreen";
import { EventScreen } from "@/screens/EventScreen";
import { SessionScreen } from "@/screens/SessionScreen";
import { SessionSeatsScreen } from "@/screens/SessionSeatsScreen";
import { parseSeatsSearch } from "@/lib/seatManager/embed";
import { OrdersScreen } from "@/screens/OrdersScreen";
import { OrderScreen } from "@/screens/OrderScreen";
import { NotificationsScreen } from "@/screens/NotificationsScreen";
import { BillingBalanceScreen } from "@/screens/BillingBalanceScreen";
import { BillingInvoicesScreen } from "@/screens/BillingInvoicesScreen";
import { BillingScreen } from "@/screens/BillingScreen";
import { InvitationsScreen } from "@/screens/InvitationsScreen";
import { PromotersScreen } from "@/screens/PromotersScreen";
import { PromoterScreen } from "@/screens/PromoterScreen";
import { NotFoundScreen } from "@/screens/NotFoundScreen";
import { OrgDataScreen } from "@/screens/OrgDataScreen";
import { parseEventsSearch, parseLoginSearch, parseOrdersSearch, parsePromotersSearch } from "@/lib/search";
import { parseBalanceSearch } from "@/lib/billingPacks";
import { PromoListScreen } from "@/screens/PromoListScreen";
import { PromoCreateScreen } from "@/screens/PromoCreateScreen";
import { PromoScreen } from "@/screens/PromoScreen";
import { TeamScreen } from "@/screens/TeamScreen";
import { EventWizardScreen } from "@/screens/EventWizardScreen";
import { parsePromoCardSearch, parsePromoListSearch } from "@/lib/promo";
import { parseCopyFrom } from "@/lib/promoScope";
import { LinksScreen } from "@/screens/LinksScreen";
import { LinkCreateScreen } from "@/screens/LinkCreateScreen";
import { LinkScreen } from "@/screens/LinkScreen";
import { parseLinkCardSearch, parseLinkNewSearch, parseLinksSearch } from "@/lib/salesLinks";

const rootRoute = createRootRoute({ component: RootLayout, notFoundComponent: NotFoundScreen });

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/orgs" });
  },
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: parseLoginSearch,
  component: LoginScreen,
});

// The bot's "Open in browser" link (spec 37 §4.3). Public: the one-time
// code is the proof; main.tsx has already taken it out of the address.
const linkLoginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login/link",
  component: LinkLoginScreen,
});

// The Telegram mini app page (spec 44, RT-12): opened by the bot's web_app
// button «Править в редакторе». Public: the one-time code is the proof.
const tgEditRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tg/edit",
  component: TgEditScreen,
});

// The entry of the Telegram mini app for every other screen (the bot's web_app
// button opens /tg?next=<path>). Public: Telegram's signed launch data is the proof.
const tgEnterRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tg",
  validateSearch: parseTgEnterSearch,
  component: function TgEnterRoute() {
    return <TgEnterScreen next={tgEnterRoute.useSearch()} />;
  },
});

const forgotRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/forgot-password",
  component: ForgotPasswordScreen,
});

// The pages a password letter opens (arena-worker sends organizers here when
// EVENT_CENTER_PUBLIC_URL is set). Public: the one-time token is the proof.
const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/reset-password",
  component: () => <SetPasswordScreen mode="reset" />,
});

const acceptInviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/accept-invite",
  component: () => <SetPasswordScreen mode="invite" />,
});

const orgsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/orgs", component: OrgsScreen });

const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: "/o/$orgId", component: OrgLayout });

const orgIndexRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/",
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/o/$orgId/events", params: { orgId: params.orgId } });
  },
});

const eventsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/events",
  validateSearch: parseEventsSearch,
  component: EventsScreen,
});

const eventRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/events/$eventId",
  component: EventScreen,
});

const sessionRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/events/$eventId/sessions/$sessionId",
  component: SessionScreen,
});

// S2b/S2c: the hall of a seated session (paint, close, lay out). ?embed=1 for the Telegram mini app.
const sessionSeatsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/events/$eventId/sessions/$sessionId/seats",
  validateSearch: parseSeatsSearch,
  component: SessionSeatsScreen,
});

const ordersRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/orders",
  validateSearch: parseOrdersSearch,
  component: OrdersScreen,
});

const orderRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/orders/$orderId",
  component: OrderScreen,
});

const notificationsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/notifications",
  component: NotificationsScreen,
});

// B2-7: billing usage (read-only). Reachable by the address for everyone, the
// screen itself explains it is the owner's.
const billingRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/billing",
  component: BillingScreen,
});

// B3-3/B4: balance and packs (also the Stripe return address) and invoices.
// Both explain themselves to a person without billing.read.
const billingBalanceRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/billing/balance",
  validateSearch: parseBalanceSearch,
  component: BillingBalanceScreen,
});

const billingInvoicesRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/billing/invoices",
  component: BillingInvoicesScreen,
});

// 4-4: invitations.
const invitationsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/invitations",
  component: InvitationsScreen,
});

// 4-6
const promotersRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/promoters",
  validateSearch: parsePromotersSearch,
  component: PromotersScreen,
});

const promoterRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/promoters/$promoterId",
  component: PromoterScreen,
});

// 4-3: promo codes. "new" is listed before "$codeId" so it is never read as an id.
const promoRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/promo",
  validateSearch: parsePromoListSearch,
  component: PromoListScreen,
});

const promoNewRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/promo/new",
  validateSearch: parseCopyFrom,
  component: PromoCreateScreen,
});

const promoCodeRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/promo/$codeId",
  validateSearch: parsePromoCardSearch,
  component: PromoScreen,
});

// 4-5
const teamRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/team",
  component: TeamScreen,
});

// 4-7: the owner's "Organization data". Reachable by the address for everyone,
// the screen itself explains it is the owner's.
const orgDataRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/org-data",
  component: OrgDataScreen,
});

// 6-1a: the event wizard. "new" is a static segment, so it wins over "$eventId".
// The screen itself shows "not found" unless the build has VITE_FEATURE_WIZARD=1.
const eventNewRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/events/new",
  component: EventWizardScreen,
});

// QL-12: sales links and QR codes (spec 43). "new" is listed before "$linkId" so it is never read as an id.
const linksRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/links",
  validateSearch: parseLinksSearch,
  component: LinksScreen,
});

const linkNewRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/links/new",
  validateSearch: parseLinkNewSearch,
  component: LinkCreateScreen,
});

const linkRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/links/$linkId",
  validateSearch: parseLinkCardSearch,
  component: LinkScreen,
});

export const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  linkLoginRoute,
  tgEditRoute,
  tgEnterRoute,
  forgotRoute,
  resetPasswordRoute,
  acceptInviteRoute,
  orgsRoute,
  orgRoute.addChildren([
    orgIndexRoute,
    eventsRoute,
    eventRoute,
    sessionRoute,
    sessionSeatsRoute,
    ordersRoute,
    orderRoute,
    notificationsRoute,
    invitationsRoute,
    billingRoute,
    billingBalanceRoute,
    billingInvoicesRoute,
    promotersRoute,
    promoterRoute,
    promoRoute,
    promoNewRoute,
    promoCodeRoute,
    teamRoute,
    orgDataRoute,
    eventNewRoute,
    linksRoute,
    linkNewRoute,
    linkRoute,
  ]),
]);

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
