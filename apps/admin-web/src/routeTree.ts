import { Route as RootRoute } from "@/routes/__root";
import { Route as IndexRoute } from "@/routes/index";
import { Route as LoginRoute } from "@/routes/login";
import { Route as NetworksRoute } from "@/routes/networks";
import { Route as NetworkDetailRoute } from "@/routes/networkDetail";
import { Route as OrganizationsRoute } from "@/routes/organizations";
import { Route as OrdersRoute } from "@/routes/orders";
import { Route as TicketsRoute } from "@/routes/tickets";
import { Route as RefundsRoute } from "@/routes/refunds";
import { Route as AuditRoute } from "@/routes/audit";
import { Route as ObservabilityRoute } from "@/routes/observability";
import { Route as VenuesRoute } from "@/routes/venues";
import { Route as ChannelsRoute } from "@/routes/channels";
import {
  EventsRoute,
  PaymentsRoute,
  ReportsRoute,
  ContentRoute,
  PosRoute,
} from "@/routes/legacyPlaceholders";
import { GeoRoute } from "@/routes/guarded";

/**
 * Manually-assembled route tree.
 *
 * We avoid TanStack's file-based codegen here so the scaffold has zero
 * generation steps in CI. Adding a route = import its Route export and
 * append it below.
 *
 * SAUI-03 added the guarded surfaces below. Each one is gated by
 * <RequirePermission /> inside its component; the route registration
 * here is unconditional so direct URLs always resolve (rendering the
 * explicit 403 UI when the caller lacks the required permission).
 */
export const routeTree = RootRoute.addChildren([
  IndexRoute,
  LoginRoute,
  NetworksRoute,
  NetworkDetailRoute,
  OrganizationsRoute,
  EventsRoute,
  VenuesRoute,
  OrdersRoute,
  TicketsRoute,
  RefundsRoute,
  ChannelsRoute,
  PaymentsRoute,
  ReportsRoute,
  ContentRoute,
  PosRoute,
  AuditRoute,
  ObservabilityRoute,
  GeoRoute,
]);
