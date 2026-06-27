/**
 * SAUI-12 -- legacy-derived module route placeholders.
 *
 * One createRoute per module path declared in LEGACY_MODULE_PLACEHOLDERS.
 * Each Route renders <LegacyModulePlaceholderRoute /> which wraps the
 * shell in <RequirePermission /> so direct-URL navigation by an
 * unprivileged operator hits the canonical 403 surface and the shell is
 * never rendered.
 *
 * Mock data: NONE. See LegacyModulePlaceholder.tsx for the rendering
 * contract.
 */
import { createRoute } from "@tanstack/react-router";
import { Route as RootRoute } from "./__root";
import { LegacyModulePlaceholderRoute } from "@/components/LegacyModulePlaceholder";
import {
  LEGACY_MODULE_PLACEHOLDERS_BY_PATH,
  legacyModuleForPath,
} from "@/lib/admin/legacyModules";
import type { NavRoutePath } from "@/lib/auth/navConfig";

function placeholderRoute(path: NavRoutePath) {
  const module = legacyModuleForPath(path);
  return createRoute({
    getParentRoute: () => RootRoute,
    path,
    component: () => <LegacyModulePlaceholderRoute module={module} />,
  });
}

// Routes are exported individually so routeTree.ts keeps the same
// "import Route as X" idiom used by every other route file.
export const EventsRoute = placeholderRoute("/events");
// /venues graduated to a real CRUD route in src/routes/venues.tsx
// (feature #242). The Route export now lives there.
export const ChannelsRoute = placeholderRoute("/channels");
export const PaymentsRoute = placeholderRoute("/payments");
export const ReportsRoute = placeholderRoute("/reports");
export const ContentRoute = placeholderRoute("/content");
export const PosRoute = placeholderRoute("/pos");

// Sanity check at module load: every path in LEGACY_MODULE_PLACEHOLDERS
// has a Route export above. Catches typos faster than a 404 in browser.
const REGISTERED_PATHS = new Set<NavRoutePath>([
  "/events",
  "/channels",
  "/payments",
  "/reports",
  "/content",
  "/pos",
]);
for (const path of Object.keys(LEGACY_MODULE_PLACEHOLDERS_BY_PATH)) {
  if (!REGISTERED_PATHS.has(path as NavRoutePath)) {
    throw new Error(
      `legacyPlaceholders.tsx: missing Route export for ${path}. ` +
        `Add a placeholderRoute("${path}") call or remove the entry from ` +
        `LEGACY_MODULE_PLACEHOLDERS.`,
    );
  }
}
