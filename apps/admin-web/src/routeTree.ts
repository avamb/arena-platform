import { Route as RootRoute } from "@/routes/__root";
import { Route as IndexRoute } from "@/routes/index";
import { Route as LoginRoute } from "@/routes/login";
import { Route as NetworksRoute } from "@/routes/networks";
import { Route as OrganizationsRoute } from "@/routes/organizations";
import {
  GeoRoute,
  OrdersRoute,
  RefundsRoute,
  TicketsRoute,
} from "@/routes/guarded";

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
  OrganizationsRoute,
  OrdersRoute,
  TicketsRoute,
  RefundsRoute,
  GeoRoute,
]);
