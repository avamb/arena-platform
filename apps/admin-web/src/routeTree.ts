import { Route as RootRoute } from "@/routes/__root";
import { Route as IndexRoute } from "@/routes/index";
import { Route as LoginRoute } from "@/routes/login";

/**
 * Manually-assembled route tree.
 *
 * We avoid TanStack's file-based codegen here so the scaffold has zero
 * generation steps in CI. Adding a route = import its Route export and
 * append it below.
 */
export const routeTree = RootRoute.addChildren([IndexRoute, LoginRoute]);
