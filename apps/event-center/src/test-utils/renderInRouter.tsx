/**
 * Renders a component that holds router links (`Link`) to static markup: a throwaway
 * router with one root route, loaded once, then the node inside it. Links to routes
 * the throwaway tree does not know still print their address.
 */
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRouter } from "@tanstack/react-router";
import { I18nProvider, type Locale } from "@/lib/i18n";

export async function renderInRouter(node: ReactNode, locale: Locale = "en"): Promise<string> {
  const root = createRootRoute({ component: () => <>{node}</> });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  await router.load();
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <I18nProvider locale={locale}>
        <RouterProvider router={router} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}
