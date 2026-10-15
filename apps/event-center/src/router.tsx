/**
 * The route tree, assembled in code like admin-web's. Every organization
 * screen lives under /o/$orgId, so a link names its organization and can be
 * sent to a colleague.
 */
import { createRootRoute, createRoute, createRouter, redirect } from "@tanstack/react-router";
import { RootLayout } from "@/screens/RootLayout";
import { LoginScreen } from "@/screens/LoginScreen";
import { LinkLoginScreen } from "@/screens/LinkLoginScreen";
import { ForgotPasswordScreen } from "@/screens/ForgotPasswordScreen";
import { SetPasswordScreen } from "@/screens/SetPasswordScreen";
import { OrgsScreen } from "@/screens/OrgsScreen";
import { OrgLayout } from "@/screens/OrgLayout";
import { EventsScreen } from "@/screens/EventsScreen";
import { EventScreen } from "@/screens/EventScreen";
import { SessionScreen } from "@/screens/SessionScreen";
import { OrdersScreen } from "@/screens/OrdersScreen";
import { OrderScreen } from "@/screens/OrderScreen";
import { NotificationsScreen } from "@/screens/NotificationsScreen";
import { TeamScreen } from "@/screens/TeamScreen";
import { NotFoundScreen } from "@/screens/NotFoundScreen";
import { parseEventsSearch, parseLoginSearch, parseOrdersSearch } from "@/lib/search";

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

// 4-5
const teamRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: "/team",
  component: TeamScreen,
});

export const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  linkLoginRoute,
  forgotRoute,
  resetPasswordRoute,
  acceptInviteRoute,
  orgsRoute,
  orgRoute.addChildren([
    orgIndexRoute,
    eventsRoute,
    eventRoute,
    sessionRoute,
    ordersRoute,
    orderRoute,
    notificationsRoute,
    teamRoute,
  ]),
]);

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
