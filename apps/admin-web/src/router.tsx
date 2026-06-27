import { createRouter } from "@tanstack/react-router";
import { LoadingScreen } from "@/components/LoadingScreen";
import { routeTree } from "@/routeTree";

export const router = createRouter({
  routeTree,
  defaultPendingComponent: () => <LoadingScreen label="Loading…" />,
  defaultPendingMs: 200,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
