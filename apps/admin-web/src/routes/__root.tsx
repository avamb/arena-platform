import { createRootRoute } from "@tanstack/react-router";
import { Suspense, lazy } from "react";
import { AppLayout } from "@/components/AppLayout";
import { LoadingScreen } from "@/components/LoadingScreen";
import { config } from "@/lib/config";

const TanStackRouterDevtools = config.isDevelopment
  ? lazy(() =>
      import("@tanstack/router-devtools").then((m) => ({
        default: m.TanStackRouterDevtools,
      })),
    )
  : (): null => null;

export const Route = createRootRoute({
  component: RootComponent,
  notFoundComponent: NotFound,
});

function RootComponent() {
  return (
    <>
      <AppLayout />
      <Suspense fallback={null}>
        <TanStackRouterDevtools />
      </Suspense>
    </>
  );
}

function NotFound() {
  return (
    <div style={{ padding: 24 }}>
      <h1 style={{ marginTop: 0 }}>404 — Route not found</h1>
      <p>The admin workspace has no page at this URL.</p>
    </div>
  );
}

// Re-export so the route tree can lazily render a fallback while children
// resolve. Avoids a flash of blank content between transitions.
export { LoadingScreen as RouteLoadingFallback };
